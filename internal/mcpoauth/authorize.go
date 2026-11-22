package mcpoauth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const maxPendingPerOwner = 3

func (b *Broker) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name            string   `json:"client_name"`
		Redirects       []string `json:"redirect_uris"`
		TokenMethod     string   `json:"token_endpoint_auth_method"`
		ApplicationType string   `json:"application_type"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) ||
		(input.TokenMethod != "" && input.TokenMethod != authNone) ||
		(input.ApplicationType != "" && input.ApplicationType != "native") ||
		len(input.Redirects) == 0 || len(input.Redirects) > 4 {
		oauthError(w, "invalid_client_metadata")
		return
	}
	for _, redirect := range input.Redirects {
		if parseLoopbackRedirect(redirect) == nil {
			oauthError(w, "invalid_redirect_uri")
			return
		}
	}
	name := strings.TrimSpace(input.Name)
	if len(name) > 96 || strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		oauthError(w, "invalid_client_metadata")
		return
	}
	if name == "" {
		name = "MCP client"
	}
	registration := client{ID: "mcp:" + uuid.NewString(), Name: name, Redirects: input.Redirects}
	jsonResponse(w, http.StatusCreated, map[string]any{
		"client_id":                  b.encode("MCP client", registration),
		"client_name":                name,
		"redirect_uris":              input.Redirects,
		"token_endpoint_auth_method": authNone,
		"grant_types":                []string{authorizationCodeGrant},
		"response_types":             []string{fieldCode},
		fieldScope:                   scope,
	})
}

func single(values url.Values, key string) string {
	if len(values[key]) != 1 {
		return ""
	}
	return values[key][0]
}

func validChallenge(value string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(raw) == 32
}

func (b *Broker) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	redirect, clientID := single(query, "redirect_uri"), single(query, "client_id")
	state, challenge := single(query, "state"), single(query, "code_challenge")
	var registration client
	validClient := b.decode("MCP client", clientID, &registration) &&
		registeredLoopbackRedirect(redirect, registration.Redirects)
	if !validClient ||
		single(query, "response_type") != fieldCode || single(query, "code_challenge_method") != pkceMethod ||
		!validChallenge(challenge) || state == "" || len(state) > 512 ||
		len(query[fieldScope]) > 1 ||
		(query.Get(fieldScope) != "" && query.Get(fieldScope) != scope) ||
		!b.validResource(query) {
		oauthError(w, "invalid_request")
		return
	}
	ticket := loginRequest{
		ID:        uuid.NewString(),
		ClientKey: hash(clientID),
		Client:    registration.ID,
		Name:      registration.Name,
		Redirect:  redirect,
		State:     state,
		Challenge: challenge,
		Verifier:  nonce(),
		Expires:   time.Now().Add(flowLifetime),
	}
	sealed := b.encode("MCP login", ticket)
	if len(sealed) > maxLoginCookie {
		oauthError(w, "invalid_request")
		return
	}
	http.SetCookie(w, bindingCookie(ticket.ID, sealed, int(flowLifetime/time.Second)))
	upstream := url.Values{
		"response_type": {fieldCode}, "client_id": {b.options.ClientID}, "redirect_uri": {b.callbackURL()},
		fieldScope: {strings.Join(b.options.Scopes, " ")}, "state": {ticket.ID},
		"code_challenge": {hash(ticket.Verifier)}, "code_challenge_method": {pkceMethod},
	}
	redirectTo(w, r, b.options.UpstreamIssuer+"/oauth/v2/authorize?"+upstream.Encode())
}

func (b *Broker) validResource(query url.Values) bool {
	return len(query["resource"]) == 0 || single(query, "resource") == b.options.Resource
}

func bindingCookie(id, value string, age int) *http.Cookie {
	return &http.Cookie{
		Name: "__Host-loom-mcp-" + id, Value: value, Path: "/", MaxAge: age,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

func redirectTo(w http.ResponseWriter, r *http.Request, destination string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func (b *Broker) callbackURL() string { return b.issuer + prefix + "/callback" }

// boundFlow must be called with the broker lock held.
func (b *Broker) boundFlow(r *http.Request) (flow, bool) {
	id := single(r.URL.Query(), "state")
	f, ok := b.flows[id]
	if !ok || !f.deadline.After(time.Now()) {
		return flow{}, false
	}
	cookie, err := r.Cookie(bindingCookie(id, "", 0).Name)
	return f, err == nil && subtle.ConstantTimeCompare([]byte(hash(cookie.Value)), []byte(f.binding)) == 1
}

func (b *Broker) callback(w http.ResponseWriter, r *http.Request) {
	id := single(r.URL.Query(), "state")
	cookie, err := r.Cookie(bindingCookie(id, "", 0).Name)
	var ticket loginRequest
	if err != nil || len(cookie.Value) > maxLoginCookie ||
		!b.decode("MCP login", cookie.Value, &ticket) || ticket.ID != id || !ticket.Expires.After(time.Now()) {
		oauthError(w, "invalid_request")
		return
	}
	statusURL := prefix + "/status?state=" + url.QueryEscape(id)
	busy, release := b.reserveExchange(id)
	if busy {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{fieldError: "temporarily_unavailable"})
		return
	}
	if release == nil {
		redirectTo(w, r, statusURL)
		return
	}
	defer release()
	f := flow{loginRequest: ticket, binding: hash(cookie.Value), deadline: ticket.Expires}
	if approvalErr := b.requestApproval(r.Context(), f, single(r.URL.Query(), fieldCode)); approvalErr != nil {
		b.redirectDecision(w, r, f, "")
		return
	}
	redirectTo(w, r, statusURL)
}

// reserveExchange bounds concurrent upstream calls, with no slots held by idle browsers.
func (b *Broker) reserveExchange(id string) (bool, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(time.Now())
	_, active := b.exchanges[id]
	if _, recorded := b.flows[id]; recorded {
		return false, nil
	}
	if active || len(b.exchanges) >= maxExchanges {
		return true, nil
	}
	b.exchanges[id] = struct{}{}
	return false, func() {
		b.mu.Lock()
		delete(b.exchanges, id)
		b.mu.Unlock()
	}
}

func (b *Broker) requestApproval(ctx context.Context, f flow, code string) error {
	ctx, cancel := context.WithDeadline(ctx, f.deadline)
	defer cancel()
	upstream, expiry, err := b.exchange(ctx, f, code)
	if err != nil {
		return err
	}
	owner, err := b.options.Identify(ctx, upstream)
	if err != nil || owner.Subject == "" || owner.Org == "" || owner.Issuer != b.options.UpstreamIssuer {
		return errors.New("MCP user could not be verified")
	}
	// Resolve the confirmer before sending the prompt so a fast click is accepted.
	f.owner, f.upstream, f.upstreamExpiry = owner, upstream, expiry
	f.deadline = minTime(f.deadline, expiry)
	f.stage = waiting
	f.approvalID = uuid.NewString()
	f.confirmer = b.options.Confirmer()
	if f.confirmer == "" {
		return errors.New("MCP Telegram confirmer unavailable")
	}
	b.mu.Lock()
	b.prune(time.Now())
	if len(b.flows) >= maxFlows {
		b.mu.Unlock()
		return errors.New("too many authorized MCP connections")
	}
	pending := 0
	for _, other := range b.flows {
		if other.owner == owner && other.stage == waiting && other.deadline.After(time.Now()) {
			pending++
		}
	}
	if pending >= maxPendingPerOwner {
		b.mu.Unlock()
		return errors.New("too many pending MCP connections")
	}
	if !f.deadline.After(time.Now()) {
		b.mu.Unlock()
		return errors.New("MCP authorization expired")
	}
	b.flows[f.ID] = f
	b.mu.Unlock()
	err = b.options.Notify(ctx, f.confirmer, Approval{
		ID: f.approvalID, Issuer: owner.Issuer, Subject: owner.Subject, Org: owner.Org,
		Client: f.Name, Redirect: f.Redirect, ExpiresAt: f.deadline,
	})
	if err == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if current, exists := b.flows[f.ID]; exists && current.stage != waiting {
		// A decision may arrive before the delivery response.
		return nil
	}
	delete(b.flows, f.ID)
	return err
}

// Decide accepts only a delegated decision from the bound Telegram confirmer.
func (b *Broker) Decide(id string, owner Owner, actor, choice string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	flowID, f := b.findApproval(id)
	if flowID == "" || !f.deadline.After(time.Now()) || actor == "" || f.confirmer != actor || f.owner != owner {
		return errors.New("MCP connection approval unavailable")
	}
	next := denied
	if choice == "approve" {
		next = approved
	} else if choice != "reject" {
		return errors.New("invalid MCP connection decision")
	}
	if f.stage != waiting && f.stage != next {
		return errors.New("MCP connection already decided")
	}
	f.stage = next
	b.flows[flowID] = f
	return nil
}

func (b *Broker) findApproval(id string) (string, flow) {
	for flowID, f := range b.flows {
		if f.approvalID != "" && f.approvalID == id {
			return flowID, f
		}
	}
	return "", flow{}
}

func (b *Broker) status(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	f, ok := b.boundFlow(r)
	if !ok || f.returned {
		b.mu.Unlock()
		oauthError(w, "invalid_request")
		return
	}
	if f.stage != approved && f.stage != denied {
		b.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="2">`+
			`<title>Loom MCP</title><h1>Check Telegram</h1><p>Approve or reject this MCP client in the bot.</p>`)
		return
	}
	var code string
	if f.stage == approved {
		code = nonce()
		f.deadline = minTime(f.deadline, time.Now().Add(codeLifetime))
		b.codes[code] = f
	}
	// Retain a tombstone until the browser ticket expires to prevent reuse of its approval ID.
	b.flows[f.ID] = flow{
		binding: f.binding, stage: f.stage, returned: true, deadline: f.Expires,
		owner: f.owner, approvalID: f.approvalID, confirmer: f.confirmer,
	}
	b.mu.Unlock()
	b.redirectDecision(w, r, f, code)
}

func (b *Broker) redirectDecision(w http.ResponseWriter, r *http.Request, f flow, code string) {
	query := url.Values{"state": {f.State}, "iss": {b.issuer}}
	if code == "" {
		query.Set(fieldError, "access_denied")
	} else {
		query.Set(fieldCode, code)
	}
	http.SetCookie(w, bindingCookie(f.ID, "", -1))
	redirectTo(w, r, f.Redirect+"?"+query.Encode())
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
