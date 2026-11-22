// Package mcpoauth brokers OAuth for local MCP clients through a fixed upstream callback.
package mcpoauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	prefix                 = "/oauth/mcp"
	scope                  = "mcp"
	flowLifetime           = 5 * time.Minute
	codeLifetime           = time.Minute
	maxFlows               = 256
	maxExchanges           = 16
	maxBody                = 16 << 10
	maxLoginCookie         = 3600
	accessEnvelopePrefix   = "loom-mcp1."
	fieldError             = "error"
	authNone               = "none"
	authorizationCodeGrant = "authorization_code"
	fieldCode              = "code"
	fieldScope             = "scope"
	pkceMethod             = "S256"
	upstreamTimeout        = 10 * time.Second
)

// Approval identifies the exact client and redirect that the user sees in Telegram.
type Approval struct {
	ID        string    `json:"id"`
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	Org       string    `json:"organization"`
	Client    string    `json:"client"`
	Redirect  string    `json:"redirect_uri"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Owner struct {
	Issuer, Subject, Org string
}

type Options struct {
	Resource, UpstreamIssuer, ClientID, ClientSecret string
	Scopes                                           []string
	Key                                              []byte
	HTTP                                             *http.Client
	Identify                                         func(context.Context, string) (Owner, error)
	Confirmer                                        func() string
	Notify                                           func(context.Context, string, Approval) error
}

type client struct {
	ID, Name  string
	Redirects []string
}

type stage uint8

const (
	waiting stage = iota
	approved
	denied
)

// loginRequest is sealed into the browser cookie until upstream identity is verified.
type loginRequest struct {
	ID, ClientKey, Client, Name string
	Redirect, State, Challenge  string
	Verifier                    string
	Expires                     time.Time
}

type flow struct {
	loginRequest

	binding        string
	deadline       time.Time
	stage          stage
	returned       bool
	owner          Owner
	approvalID     string
	confirmer      string
	upstream       string
	upstreamExpiry time.Time
}

type Broker struct {
	options   Options
	issuer    string
	seal      cipher.AEAD
	mu        sync.Mutex
	flows     map[string]flow
	codes     map[string]flow
	exchanges map[string]struct{}
}

func New(options Options) (*Broker, error) {
	u, err := url.Parse(options.Resource)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		options.ClientID == "" || options.ClientSecret == "" ||
		options.Identify == nil || options.Confirmer == nil || options.Notify == nil || len(options.Key) != 32 {
		return nil, errors.New("invalid MCP OAuth broker configuration")
	}
	issuer, err := url.Parse(options.UpstreamIssuer)
	if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" ||
		issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" ||
		(issuer.Path != "" && issuer.Path != "/") || len(options.Scopes) == 0 {
		return nil, errors.New("MCP OAuth broker requires an HTTPS upstream issuer and scopes")
	}
	options.UpstreamIssuer = strings.TrimRight(options.UpstreamIssuer, "/")
	block, err := aes.NewCipher(options.Key)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	httpClient := http.Client{}
	if options.HTTP != nil {
		httpClient = *options.HTTP
	}
	httpClient.Timeout = upstreamTimeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	options.HTTP = &httpClient
	return &Broker{
		options: options, issuer: u.Scheme + "://" + u.Host, seal: seal,
		flows: map[string]flow{}, codes: map[string]flow{}, exchanges: map[string]struct{}{},
	}, nil
}

func (b *Broker) Issuer() string { return b.issuer }

func (b *Broker) Scope() string { return scope }

func (b *Broker) Attach(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", b.metadata)
	mux.HandleFunc("POST "+prefix+"/register", b.register)
	mux.HandleFunc("GET "+prefix+"/authorize", b.authorize)
	mux.HandleFunc("GET "+prefix+"/callback", b.callback)
	mux.HandleFunc("GET "+prefix+"/status", b.status)
	mux.HandleFunc("POST "+prefix+"/token", b.token)
}

func (b *Broker) metadata(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"issuer":                 b.issuer,
		"authorization_endpoint": b.issuer + prefix + "/authorize",
		"token_endpoint":         b.issuer + prefix + "/token",
		"registration_endpoint":  b.issuer + prefix + "/register",
		"authorization_response_iss_parameter_supported": true,
		"response_types_supported":                       []string{fieldCode},
		"grant_types_supported":                          []string{authorizationCodeGrant},
		"token_endpoint_auth_methods_supported":          []string{authNone},
		"code_challenge_methods_supported":               []string{pkceMethod},
		"scopes_supported":                               []string{scope},
	})
}

func nonce() string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (b *Broker) encode(purpose string, value any) string {
	raw, _ := json.Marshal(value)
	// #nosec G407 -- NewGCMWithRandomNonce generates and prepends a fresh nonce.
	return base64.RawURLEncoding.EncodeToString(b.seal.Seal(nil, nil, raw, []byte(b.options.Resource+" "+purpose)))
}

func (b *Broker) decode(purpose, encoded string, dst any) bool {
	if len(encoded) > maxBody {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	raw, err = b.seal.Open(nil, nil, raw, []byte(b.options.Resource+" "+purpose))
	return err == nil && json.Unmarshal(raw, dst) == nil
}

type accessToken struct {
	Upstream string
	Client   string
	Expiry   time.Time
}

// Unwrap is used by the MCP authenticator before normal upstream introspection.
func (b *Broker) Unwrap(token string) (string, string, bool) {
	encoded, ok := strings.CutPrefix(token, accessEnvelopePrefix)
	var claims accessToken
	if !ok || !b.decode("MCP access", encoded, &claims) || !claims.Expiry.After(time.Now()) ||
		claims.Client == "" || claims.Upstream == "" {
		return "", "", false
	}
	return claims.Upstream, claims.Client, true
}

func (b *Broker) prune(now time.Time) {
	for id, f := range b.flows {
		if f.deadline.After(now) {
			continue
		}
		if f.Expires.After(now) {
			b.flows[id] = flow{stage: denied, returned: true, deadline: f.Expires}
			continue
		}
		delete(b.flows, id)
	}
	for id, f := range b.codes {
		if !f.deadline.After(now) {
			delete(b.codes, id)
		}
	}
}

func parseLoopbackRedirect(value string) *url.URL {
	if len(value) > 1024 || strings.ContainsAny(value, "\\#? \t\r\n") {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path == "" {
		return nil
	}
	host := u.Hostname()
	if !net.ParseIP(host).IsLoopback() && !strings.EqualFold(host, "localhost") {
		return nil
	}
	if rawPort := u.Port(); rawPort != "" {
		port, portErr := strconv.Atoi(rawPort)
		if portErr != nil || port <= 0 || port > 65535 {
			return nil
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil
	}
	return u
}

// A native client can select a new loopback port for each authorization.
func registeredLoopbackRedirect(redirect string, registered []string) bool {
	actual := parseLoopbackRedirect(redirect)
	if actual == nil || actual.Port() == "" {
		return false
	}
	for _, uri := range registered {
		u := parseLoopbackRedirect(uri)
		if u != nil && strings.EqualFold(u.Hostname(), actual.Hostname()) && u.EscapedPath() == actual.EscapedPath() {
			return true
		}
	}
	return false
}

func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func oauthError(w http.ResponseWriter, code string) {
	jsonResponse(w, http.StatusBadRequest, map[string]string{fieldError: code})
}
