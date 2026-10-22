// Package modules discovers trusted deployment services and authorizes each RPC.
package modules

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/complynx/loom/internal/sandbox"
)

//go:embed manifest.schema.json
var manifestSchema []byte
var identifier = regexp.MustCompile("^[a-z][a-z0-9_.-]{0,127}$")
var ErrUnavailable = errors.New("module unavailable")
var ErrForbidden = errors.New("module permission denied")
var ErrWriteDisabled = errors.New("write requires durable operation coordinator")

const (
	fieldProtocolVersion       = "protocol_version"
	fieldRunID                 = "run_id"
	fieldOperationID           = "operation_id"
	effectRead                 = "read"
	fieldArguments             = "arguments"
	fieldResult                = "result"
	fieldArtifacts             = "artifacts"
	stateSucceeded             = "succeeded"
	stateUnknown               = "unknown"
	stateFailed                = "failed"
	maxSchemaDepth             = 48
	defaultRequestTimeout      = 10 * time.Second
	defaultDynamicCacheCeiling = time.Minute
	defaultFreshTTL            = 5 * time.Minute
	defaultManifestLimit       = 256 << 10
	defaultDialTimeout         = 5 * time.Second
	dynamicDiscoveryLimit      = 256 << 10
	dynamicDiscoveryTimeout    = 5 * time.Second
	maxDynamicMethods          = 200
	maxDynamicCacheEntries     = 1024
	maxArgumentBytes           = 65536
	maxDocumentNodes           = 16384
	maxDocumentDepth           = 32
	maxObjectFields            = 256
	maxArrayItems              = 4096
	defaultPreparationTTL      = 5 * time.Minute
)

type Principal struct {
	UserID, Subject, Issuer, Organization, Actor, PolicyRevision string
	Permissions                                                  []string
	Token                                                        string
}
type Tool struct {
	Summary                   string          `json:"Summary,omitempty"`
	ModuleSummary             string          `json:"ModuleSummary,omitempty"`
	ServiceSummary            string          `json:"ServiceSummary,omitempty"`
	SupportsOperationAdoption bool            `json:"SupportsOperationAdoption,omitempty"`
	ServiceID                 string          `json:"ServiceID"`
	Module                    string          `json:"Module"`
	Method                    string          `json:"Method"`
	Description               string          `json:"Description"`
	Effect                    string          `json:"Effect"`
	ConfirmationReplyMode     string          `json:"ConfirmationReplyMode,omitempty"`
	ConfirmationMode          string          `json:"ConfirmationMode"`
	InputSchema               json.RawMessage `json:"InputSchema"`
}
type Confirmation struct {
	ReplyMode   string `json:"reply_mode,omitempty"`
	Mode        string `json:"mode"`
	PreparePath string `json:"prepare_path,omitempty"`
}
type Method struct {
	Summary                    string          `json:"summary,omitempty"`
	ID                         string          `json:"id"`
	Description                string          `json:"description"`
	Permissions                []string        `json:"permissions"`
	Path                       string          `json:"path"`
	Effect                     string          `json:"effect"`
	TimeoutMS                  int             `json:"timeout_ms"`
	MaxResultBytes             int64           `json:"max_result_bytes"`
	InputSchema                json.RawMessage `json:"input_schema"`
	OutputSchema               json.RawMessage `json:"output_schema"`
	Idempotency                bool            `json:"idempotency"`
	StatusPath                 string          `json:"status_path,omitempty"`
	CancelPath                 string          `json:"cancel_path,omitempty"`
	DeduplicationWindowSeconds int64           `json:"deduplication_window_seconds,omitempty"`
	Confirmation               Confirmation    `json:"confirmation"`
	ArtifactMIMETypes          []string        `json:"artifact_mime_types"`
	ExternalDisclosure         bool            `json:"external_disclosure"`
}
type Module struct {
	Summary         string   `json:"summary,omitempty"`
	ID              string   `json:"id"`
	Description     string   `json:"description"`
	Permissions     []string `json:"permissions"`
	Discovery       string   `json:"discovery"`
	Methods         []Method `json:"methods,omitempty"`
	DiscoveryPath   string   `json:"discovery_path,omitempty"`
	CacheTTLSeconds int      `json:"cache_ttl_seconds,omitempty"`
}
type CallerAuth struct {
	Type      string `json:"type"`
	Issuer    string `json:"issuer,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Audience  string `json:"audience,omitempty"`
	SecretRef string `json:"secret_ref,omitempty"`
}
type Caller struct {
	Permissions []string   `json:"permissions"`
	Auth        CallerAuth `json:"auth"`
}
type Manifest struct {
	Summary         string   `json:"summary,omitempty"`
	ProtocolVersion int      `json:"protocol_version"`
	ServiceID       string   `json:"service_id"`
	Revision        string   `json:"revision"`
	Description     string   `json:"description"`
	Caller          *Caller  `json:"caller,omitempty"`
	Modules         []Module `json:"modules"`
}

// Endpoint addresses come from trusted Docker metadata, never model input or DNS.
type Endpoint struct{ ContainerID, URI, Address string }
type Source interface {
	Discover(context.Context) ([]Endpoint, error)
}
type SourceFunc func(context.Context) ([]Endpoint, error)

func (f SourceFunc) Discover(ctx context.Context) ([]Endpoint, error) { return f(ctx) }

type Options struct {
	WrapTransport func(http.RoundTripper) http.RoundTripper
	// OnCallerBindings persists trusted identities before publishing the snapshot.
	OnCallerBindings                    func(context.Context, []CallerBinding) error
	RequestTimeout, DynamicCacheCeiling time.Duration
	ImportArtifact                      ArtifactImporter
	FreshTTL, PollInterval              time.Duration
	ManifestLimit                       int64
}
type registered struct {
	inspectPath string
	endpoint    Endpoint
	manifest    Manifest
	hash        string
	fresh       time.Time
	client      *http.Client
}
type dynamicEntry struct {
	methods []Method
	expires time.Time
}
type Registry struct {
	refreshMu      sync.Mutex
	lastSuccess    time.Time
	lastRefreshErr error
	source         Source
	opts           Options
	schema         *jsonschema.Schema
	mu             sync.RWMutex
	services       map[string]registered
	dynamic        map[string]dynamicEntry
}
type deniedLoader struct{}

func (deniedLoader) Load(string) (any, error) {
	return nil, errors.New("external schema references disabled")
}

// Preserve exact JSON numbers for schema bounds and downstream dispatch.
func decodeJSONDocument(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var doc any
	if err := decodeJSONDocument(raw, &doc); err != nil {
		return nil, err
	}
	if err := checkDepth(doc, 0); err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(deniedLoader{})
	if err := c.AddResource("https://clx.invalid/schema", doc); err != nil {
		return nil, err
	}
	return c.Compile("https://clx.invalid/schema")
}
func checkDepth(v any, n int) error {
	if n > maxSchemaDepth {
		return errors.New("schema nesting too deep")
	}
	switch x := v.(type) {
	case map[string]any:
		for _, v := range x {
			if err := checkDepth(v, n+1); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range x {
			if err := checkDepth(v, n+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func NewRegistry(source Source, opts Options) (*Registry, error) {
	if source == nil {
		return nil, errors.New("module source required")
	}
	s, err := compileSchema(manifestSchema)
	if err != nil {
		return nil, err
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.DynamicCacheCeiling <= 0 {
		opts.DynamicCacheCeiling = defaultDynamicCacheCeiling
	}
	if opts.FreshTTL <= 0 {
		opts.FreshTTL = defaultFreshTTL
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Minute
	}
	if opts.ManifestLimit <= 0 {
		opts.ManifestLimit = defaultManifestLimit
	}
	return &Registry{
		source:   source,
		opts:     opts,
		schema:   s,
		services: map[string]registered{},
		dynamic:  map[string]dynamicEntry{},
	}, nil
}
func (r *Registry) Run(ctx context.Context) {
	_ = r.Refresh(ctx)
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.Refresh(ctx)
		}
	}
}
func (r *Registry) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.opts.RequestTimeout)
	defer cancel()
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	endpoints, err := r.source.Discover(ctx)
	if err != nil {
		r.mu.Lock()
		r.lastRefreshErr = err
		r.mu.Unlock()
		return err
	}
	r.mu.RLock()
	old := r.services
	r.mu.RUnlock()
	next := map[string]registered{}
	conflicts := map[string]bool{}
	observed := map[string]string{}
	var failures []error
	// A successful source list removes disappeared containers even if another fetch fails.
	present := map[string]Endpoint{}
	for _, ep := range endpoints {
		present[ep.ContainerID] = ep
	}
	for id, s := range old {
		if current, ok := present[s.endpoint.ContainerID]; ok && current == s.endpoint {
			next[id] = s
		}
	}
	failures = r.refreshEndpoints(ctx, endpoints, next, conflicts, observed)
	for id := range conflicts {
		delete(next, id)
		failures = append(failures, fmt.Errorf("manifest replica conflict: %s", id))
	}
	refreshErr := errors.Join(failures...)
	if refreshErr == nil && r.opts.OnCallerBindings != nil {
		bindings := []CallerBinding{}
		for _, service := range next {
			if caller := service.manifest.Caller; caller != nil {
				bindings = append(bindings, CallerBinding{
					ServiceID:   service.manifest.ServiceID,
					Permissions: append([]string(nil), caller.Permissions...),
					Auth:        caller.Auth,
				})
			}
		}
		if bindingErr := r.opts.OnCallerBindings(ctx, bindings); bindingErr != nil {
			r.mu.Lock()
			r.services = map[string]registered{}
			r.lastRefreshErr = bindingErr
			r.mu.Unlock()
			return bindingErr
		}
	}
	r.mu.Lock()
	r.services = next
	r.lastRefreshErr = refreshErr
	if r.lastRefreshErr == nil {
		r.lastSuccess = time.Now()
	}
	r.mu.Unlock()
	return errors.Join(failures...)
}
func (r *Registry) decodeManifest(raw []byte) (Manifest, error) {
	var m Manifest
	var doc any
	if err := decodeJSONDocument(raw, &doc); err != nil {
		return m, err
	}
	if err := r.schema.Validate(doc); err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	seen := map[string]bool{}
	for _, module := range m.Modules {
		if seen[module.ID] || reserved(module.ID) || module.ID == "history" || module.ID == fieldArtifacts ||
			module.ID == "diagnostics" || module.ID == "memory" || module.ID == "files" {
			return m, errors.New("duplicate or reserved module id")
		}
		seen[module.ID] = true
		if module.Discovery == "dynamic" {
			if err := validPath(module.DiscoveryPath); err != nil {
				return m, err
			}
		}
		if err := validateMethods(module.Methods); err != nil {
			return m, err
		}
	}
	return m, nil
}
func reserved(s string) bool { return s == "__proto__" || s == "constructor" || s == "prototype" }
func validateMethods(methods []Method) error {
	seen := map[string]bool{}
	for _, method := range methods {
		if !identifier.MatchString(method.ID) || reserved(method.ID) || seen[method.ID] {
			return errors.New("duplicate or reserved method id")
		}
		seen[method.ID] = true
		if err := validateMethod(method); err != nil {
			return err
		}
	}
	return nil
}

func validateMethod(method Method) error {
	if method.Confirmation.ReplyMode == "protocol" &&
		(method.Confirmation.Mode == "none" || method.Effect == effectRead) {
		return errors.New("protocol confirmation requires a confirmed write")
	}
	if method.Confirmation.ReplyMode != "" && method.Confirmation.ReplyMode != "protocol" {
		return errors.New("confirmation reply_mode must be protocol or omitted")
	}
	if method.TimeoutMS < 1 || method.TimeoutMS > 300000 || method.MaxResultBytes < 1 ||
		method.MaxResultBytes > 10<<20 {
		return errors.New("invalid method limits")
	}
	if method.Effect != effectRead && method.Effect != "write" && method.Effect != "delete" {
		return errors.New("invalid method effect")
	}
	for _, path := range []string{method.Path, method.StatusPath, method.CancelPath, method.Confirmation.PreparePath} {
		if path == "" {
			continue
		}
		if err := validPath(path); err != nil {
			return err
		}
	}
	if _, err := compileSchema(method.InputSchema); err != nil {
		return err
	}
	if _, err := compileSchema(method.OutputSchema); err != nil {
		return err
	}
	if method.Effect != effectRead &&
		(!method.Idempotency || method.StatusPath == "" || method.DeduplicationWindowSeconds < 86400) {
		return errors.New("unsafe write contract")
	}
	return nil
}

func validPath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\") {
		return errors.New("invalid module path")
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return err
	}
	if strings.Contains(decoded, "%") || strings.ContainsAny(decoded, "?#\\") {
		return errors.New("encoded module path rejected")
	}
	for segment := range strings.SplitSeq(decoded, "/") {
		if segment == ".." || segment == "." {
			return errors.New("path traversal rejected")
		}
	}
	return nil
}
func validateURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" {
		return nil, errors.New("invalid registered URI")
	}
	if pathErr := validPath(u.EscapedPath()); pathErr != nil {
		return nil, pathErr
	}
	return u, nil
}
func pinnedClient(ep Endpoint) (*http.Client, error) {
	u, err := validateURI(ep.URI)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(ep.Address) == nil {
		return nil, errors.New("destination must be Docker metadata IP")
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport must be *http.Transport")
	}
	tr := baseTransport.Clone()
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: defaultDialTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(ep.Address, port))
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("module redirects forbidden") },
	}, nil
}
func readResponse(response *http.Response, limit int64) ([]byte, error) {
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("module HTTP status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("module response too large")
	}
	return raw, nil
}
func allowed(p Principal, categories []string) bool {
	set := map[string]bool{}
	for _, s := range p.Permissions {
		set[s] = true
	}
	for _, s := range categories {
		if !set[s] {
			return false
		}
	}
	return p.Subject != "" && p.Issuer != ""
}
func (r *Registry) snapshot() []registered {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]registered, 0, len(r.services))
	for _, s := range r.services {
		if time.Since(s.fresh) <= r.opts.FreshTTL {
			all = append(all, s)
		}
	}
	return all
}
func cacheKey(p Principal, s registered, module Module) string {
	permissions := append([]string(nil), p.Permissions...)
	sort.Strings(permissions)
	token := sha256.Sum256([]byte(p.Token))
	raw, _ := json.Marshal(
		[]any{
			p.Issuer,
			p.Subject,
			p.Organization,
			p.Actor,
			p.PolicyRevision,
			permissions,
			s.manifest.ServiceID,
			s.hash,
			module.ID,
			hex.EncodeToString(token[:]),
		},
	)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r *Registry) methods(ctx context.Context, p Principal, s registered, module Module) ([]Method, error) {
	if module.Discovery == "static" {
		return module.Methods, nil
	}
	key := cacheKey(p, s, module)
	r.mu.RLock()
	cached, ok := r.dynamic[key]
	r.mu.RUnlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.methods, nil
	}
	if p.Token == "" {
		return nil, ErrForbidden
	}
	raw, err := invoke(
		ctx,
		s,
		p,
		module.DiscoveryPath,
		json.RawMessage("{}"),
		dynamicDiscoveryLimit,
		dynamicDiscoveryTimeout,
	)
	if err != nil {
		return nil, err
	}
	var response struct {
		Methods []Method `json:"methods"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return nil, err
	}
	var trailing any
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		return nil, errors.New("discovery must contain one JSON value")
	}
	if len(response.Methods) > maxDynamicMethods {
		return nil, errors.New("too many dynamic methods")
	}
	for _, method := range response.Methods {
		if shapeErr := r.validateMethodShape(method); shapeErr != nil {
			return nil, shapeErr
		}
	}
	if err = validateMethods(response.Methods); err != nil {
		return nil, err
	}
	ttl := time.Duration(module.CacheTTLSeconds) * time.Second
	ttl = min(ttl, r.opts.DynamicCacheCeiling)
	if ttl > 0 {
		r.mu.Lock()
		if len(r.dynamic) >= maxDynamicCacheEntries {
			for key := range r.dynamic {
				delete(r.dynamic, key)
				break
			}
		}
		r.dynamic[key] = dynamicEntry{methods: response.Methods, expires: time.Now().Add(ttl)}
		r.mu.Unlock()
	}
	return response.Methods, nil
}
func (r *Registry) List(ctx context.Context, p Principal) ([]Tool, error) {
	tools := []Tool{}
	seen := map[string]string{}
	for _, s := range r.snapshot() {
		for _, module := range s.manifest.Modules {
			if !allowed(p, module.Permissions) {
				continue
			}
			if ambiguousModule(seen, module.ID, s.manifest.ServiceID) {
				return nil, errors.New("ambiguous module id")
			}
			seen[module.ID] = s.manifest.ServiceID
			methods, err := r.methods(ctx, p, s, module)
			if err != nil {
				return nil, err
			}
			for _, method := range methods {
				if method.Effect == effectRead && allowed(p, method.Permissions) {
					tools = append(
						tools,
						Tool{
							Summary:        DiscoverySummary(method.Summary, method.Description),
							ModuleSummary:  DiscoverySummary(module.Summary, module.Description),
							ServiceSummary: DiscoverySummary(s.manifest.Summary, s.manifest.Description),
							ServiceID:      s.manifest.ServiceID,
							Module:         module.ID,
							Method:         method.ID,
							Description:    method.Description,
							InputSchema:    method.InputSchema,
						},
					)
				}
			}
		}
	}
	sortTools(tools)
	return tools, nil
}
func (r *Registry) lookup(ctx context.Context, p Principal, moduleName, methodName string) (registered, Method, error) {
	return r.lookupWithFresh(ctx, p, moduleName, methodName, false)
}

func (r *Registry) lookupFresh(
	ctx context.Context,
	p Principal,
	moduleName, methodName string,
) (registered, Method, error) {
	return r.lookupWithFresh(ctx, p, moduleName, methodName, true)
}

func (r *Registry) lookupWithFresh(
	ctx context.Context,
	p Principal,
	moduleName, methodName string,
	fresh bool,
) (registered, Method, error) {
	var found *registered
	var module Module
	for _, s := range r.snapshot() {
		for _, m := range s.manifest.Modules {
			if m.ID == moduleName && allowed(p, m.Permissions) {
				if found != nil {
					return registered{}, Method{}, ErrUnavailable
				}
				candidate := s
				found = &candidate
				module = m
			}
		}
	}
	if found == nil {
		return registered{}, Method{}, ErrUnavailable
	}
	if fresh && module.Discovery == "dynamic" {
		r.mu.Lock()
		delete(r.dynamic, cacheKey(p, *found, module))
		r.mu.Unlock()
	}
	methods, err := r.methods(ctx, p, *found, module)
	if err != nil {
		return registered{}, Method{}, err
	}
	for _, m := range methods {
		if m.ID != methodName {
			continue
		}
		if !allowed(p, m.Permissions) {
			return registered{}, Method{}, ErrForbidden
		}
		return *found, m, nil
	}
	return registered{}, Method{}, ErrUnavailable
}

func invoke(
	ctx context.Context,
	s registered,
	p Principal,
	path string,
	payload json.RawMessage,
	limit int64,
	timeout time.Duration,
) (json.RawMessage, error) {
	if err := validPath(path); err != nil {
		return nil, err
	}
	u, err := validateURI(s.endpoint.URI)
	if err != nil {
		return nil, err
	}
	u.Path = path
	u.RawPath = ""
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	raw, err := readResponse(resp, limit)
	_ = resp.Body.Close()
	return raw, err
}
func (r *Registry) Broker(p Principal, runID string, guard func(context.Context) error) sandbox.Broker {
	return r.BrokerLive(func(context.Context) (Principal, error) { return p, nil }, runID, guard)
}

// BrokerLive refreshes user rights and credentials before every read.
func (r *Registry) BrokerLive(
	principal func(context.Context) (Principal, error),
	runID string,
	guard func(context.Context) error,
) sandbox.Broker {
	return sandbox.BrokerFunc(
		func(ctx context.Context, module, method string, args json.RawMessage) (json.RawMessage, error) {
			return r.readCall(ctx, principal, runID, guard, module, method, args)
		},
	)
}

func (r *Registry) readCall(ctx context.Context, principal func(context.Context) (Principal, error), runID string,
	guard func(context.Context) error, module, method string, args json.RawMessage) (json.RawMessage, error) {
	if len(args) > maxArgumentBytes {
		return nil, errors.New("module arguments exceed 65536 bytes")
	}
	if guardErr := checkGuard(ctx, guard); guardErr != nil {
		return nil, guardErr
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	s, m, err := r.lookup(ctx, p, module, method)
	if err != nil {
		return nil, err
	}
	if m.Effect != effectRead {
		return nil, ErrWriteDisabled
	}
	doc, err := validateReadArguments(m.InputSchema, args)
	if err != nil {
		return nil, err
	}
	if err = checkGuard(ctx, guard); err != nil {
		return nil, err
	}
	operationID := uuid.NewString()
	envelope, _ := json.Marshal(
		map[string]any{fieldProtocolVersion: 1, fieldOperationID: operationID, fieldRunID: runID, fieldArguments: doc},
	)
	raw, err := invoke(
		ctx,
		s,
		p,
		m.Path,
		envelope,
		min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)),
		time.Duration(m.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return nil, err
	}
	var response struct {
		Status      string          `json:"status"`
		OperationID string          `json:"operation_id"`
		Result      json.RawMessage `json:"result"`
		Artifacts   json.RawMessage `json:"artifacts"`
		Error       json.RawMessage `json:"error"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.OperationID != operationID {
		return nil, errors.New("module operation ID mismatch")
	}
	if response.Status != stateSucceeded {
		return nil, errors.New("read method did not succeed")
	}
	output, err := compileSchema(m.OutputSchema)
	if err != nil {
		return nil, err
	}
	if err = decodeJSONDocument(response.Result, &doc); err != nil {
		return nil, err
	}
	if err = validateDocument(doc); err != nil {
		return nil, err
	}
	if err = output.Validate(doc); err != nil {
		return nil, fmt.Errorf("invalid module result: %w", err)
	}
	artifacts, err := r.importArtifacts(ctx, p, s, module, runID, m, response.Artifacts, guard, principal)
	if err != nil {
		return nil, err
	}
	if len(artifacts) > 0 {
		return json.Marshal(map[string]any{fieldResult: response.Result, fieldArtifacts: artifacts})
	}
	return response.Result, nil
}

// CallerBinding exposes deployment-approved identity metadata, never a credential.
type CallerBinding struct {
	ServiceID   string
	Permissions []string
	Auth        CallerAuth
}

func (r *Registry) CallerBindings() []CallerBinding {
	result := []CallerBinding{}
	for _, service := range r.snapshot() {
		if service.manifest.Caller != nil {
			caller := service.manifest.Caller
			result = append(
				result,
				CallerBinding{
					ServiceID:   service.manifest.ServiceID,
					Permissions: append([]string(nil), caller.Permissions...),
					Auth:        caller.Auth,
				},
			)
		}
	}
	return result
}
func (r *Registry) CheckCaller(serviceID string) error {
	for _, service := range r.snapshot() {
		if service.manifest.ServiceID == serviceID && service.manifest.Caller != nil {
			return nil
		}
	}
	return ErrUnavailable
}

func (r *Registry) validateMethodShape(method Method) error {
	var schemaDoc map[string]any
	if err := decodeJSONDocument(manifestSchema, &schemaDoc); err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(deniedLoader{})
	if err := c.AddResource("https://clx.invalid/manifest", schemaDoc); err != nil {
		return err
	}
	schema, err := c.Compile("https://clx.invalid/manifest#/$defs/method")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(method)
	if err != nil {
		return err
	}
	var doc any
	if err = decodeJSONDocument(raw, &doc); err != nil {
		return err
	}
	return schema.Validate(doc)
}

// ListActions exposes supported writes for a durable structured tool. JS bindings use List only.
func (r *Registry) ListActions(ctx context.Context, p Principal) ([]Tool, error) {
	tools := []Tool{}
	seen := map[string]string{}
	for _, s := range r.snapshot() {
		for _, module := range s.manifest.Modules {
			if !allowed(p, module.Permissions) {
				continue
			}
			if ambiguousModule(seen, module.ID, s.manifest.ServiceID) {
				return nil, errors.New("ambiguous module id")
			}
			seen[module.ID] = s.manifest.ServiceID
			methods, err := r.methods(ctx, p, s, module)
			if err != nil {
				return nil, err
			}
			for _, method := range methods {
				if method.Effect != effectRead && allowed(p, method.Permissions) {
					tools = append(
						tools,
						Tool{
							Summary:                   DiscoverySummary(method.Summary, method.Description),
							ModuleSummary:             DiscoverySummary(module.Summary, module.Description),
							ServiceSummary:            DiscoverySummary(s.manifest.Summary, s.manifest.Description),
							ServiceID:                 s.manifest.ServiceID,
							Module:                    module.ID,
							Method:                    method.ID,
							Description:               method.Description,
							InputSchema:               method.InputSchema,
							Effect:                    method.Effect,
							ConfirmationMode:          method.Confirmation.Mode,
							ConfirmationReplyMode:     method.Confirmation.ReplyMode,
							SupportsOperationAdoption: s.inspectPath != "",
						},
					)
				}
			}
		}
	}
	sortTools(tools)
	return tools, nil
}

// Bound data before schema checks so a script cannot force unbounded validation work.
func validateDocument(value any) error {
	w := documentWalker{}
	return w.walk(value, 0)
}

type documentWalker struct{ nodes int }

func (w *documentWalker) walk(value any, depth int) error {
	w.nodes++
	if w.nodes > maxDocumentNodes || depth > maxDocumentDepth {
		return errors.New("module JSON complexity limit exceeded")
	}
	switch value := value.(type) {
	case map[string]any:
		if len(value) > maxObjectFields {
			return errors.New("module JSON object too large")
		}
		for _, v := range value {
			if err := w.walk(v, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(value) > maxArrayItems {
			return errors.New("module JSON array too large")
		}
		for _, v := range value {
			if err := w.walk(v, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// TrustReady distinguishes a fresh empty deployment from unknown discovery state.
func (r *Registry) TrustReady() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.lastRefreshErr != nil || r.lastSuccess.IsZero() || time.Since(r.lastSuccess) > r.opts.FreshTTL {
		return ErrUnavailable
	}
	return nil
}

func validateReadArguments(schemaRaw, raw json.RawMessage) (any, error) {
	input, err := compileSchema(schemaRaw)
	if err != nil {
		return nil, err
	}
	var doc any
	if err = decodeJSONDocument(raw, &doc); err != nil {
		return nil, err
	}
	if err = validateDocument(doc); err != nil {
		return nil, err
	}
	if err = input.Validate(doc); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	return doc, nil
}

func checkGuard(ctx context.Context, guard func(context.Context) error) error {
	if guard == nil {
		return nil
	}
	return guard(ctx)
}

func (r *Registry) refreshEndpoints(ctx context.Context, endpoints []Endpoint,
	next map[string]registered, conflicts map[string]bool, observed map[string]string) []error {
	var failures []error
	for _, ep := range endpoints {
		u, e := validateURI(ep.URI)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		client, e := pinnedClient(ep)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if r.opts.WrapTransport != nil {
			client.Transport = r.opts.WrapTransport(client.Transport)
		}
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		response, e := client.Do(request)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		raw, e := readResponse(response, r.opts.ManifestLimit)
		_ = response.Body.Close()
		if e != nil {
			failures = append(failures, e)
			continue
		}
		manifest, inspectPath, e := r.decodeEndpointManifest(response.Header, raw)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		// A valid replacement retires the prior identity at this deployment endpoint.
		retireEndpointIdentities(next, ep, manifest.ServiceID)
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		if prior, ok := observed[manifest.ServiceID]; ok && prior != hash {
			conflicts[manifest.ServiceID] = true
		}
		observed[manifest.ServiceID] = hash
		next[manifest.ServiceID] = registered{
			inspectPath: inspectPath,
			endpoint:    ep,
			manifest:    manifest,
			hash:        hash,
			fresh:       time.Now(),
			client:      client,
		}
	}
	return failures
}

func sortTools(tools []Tool) {
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Module == tools[j].Module {
			return tools[i].Method < tools[j].Method
		}
		return tools[i].Module < tools[j].Module
	})
}

func ambiguousModule(seen map[string]string, moduleID, serviceID string) bool {
	previous, ok := seen[moduleID]
	return ok && previous != serviceID
}

func retireEndpointIdentities(next map[string]registered, ep Endpoint, serviceID string) {
	for id, prior := range next {
		if id != serviceID && prior.endpoint == ep {
			delete(next, id)
		}
	}
}
