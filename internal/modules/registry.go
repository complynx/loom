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

	"github.com/complynx/clx-scripter/internal/sandbox"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed manifest.schema.json
var manifestSchema []byte
var identifier = regexp.MustCompile("^[a-z][a-z0-9_.-]{0,127}$")
var ErrUnavailable = errors.New("module unavailable")
var ErrForbidden = errors.New("module permission denied")
var ErrWriteDisabled = errors.New("write requires durable operation coordinator")

type Principal struct {
	UserID, Subject, Issuer, Organization, Actor, PolicyRevision string
	Permissions                                                  []string
	Token                                                        string
}
type Tool struct {
	ServiceID, Module, Method, Description, Effect, ConfirmationMode string
	InputSchema                                                      json.RawMessage
}
type Confirmation struct {
	Mode        string `json:"mode"`
	PreparePath string `json:"prepare_path,omitempty"`
}
type Method struct {
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
	// OnCallerBindings persists trusted identities before publishing the snapshot.
	OnCallerBindings                    func(context.Context, []CallerBinding) error
	RequestTimeout, DynamicCacheCeiling time.Duration
	ImportArtifact                      ArtifactImporter
	FreshTTL, PollInterval              time.Duration
	ManifestLimit                       int64
}
type registered struct {
	endpoint Endpoint
	manifest Manifest
	hash     string
	fresh    time.Time
	client   *http.Client
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
func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
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
	if n > 48 {
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
		opts.RequestTimeout = 10 * time.Second
	}
	if opts.DynamicCacheCeiling <= 0 {
		opts.DynamicCacheCeiling = 60 * time.Second
	}
	if opts.FreshTTL <= 0 {
		opts.FreshTTL = 5 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Minute
	}
	if opts.ManifestLimit <= 0 {
		opts.ManifestLimit = 256 << 10
	}
	return &Registry{source: source, opts: opts, schema: s, services: map[string]registered{}, dynamic: map[string]dynamicEntry{}}, nil
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
	if err != nil {
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
		manifest, e := r.decodeManifest(raw)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		// A valid replacement retires the prior identity at this deployment endpoint.
		for id, prior := range next {
			if id != manifest.ServiceID && prior.endpoint == ep {
				delete(next, id)
			}
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		if prior, ok := observed[manifest.ServiceID]; ok && prior != hash {
			conflicts[manifest.ServiceID] = true
		}
		observed[manifest.ServiceID] = hash
		next[manifest.ServiceID] = registered{endpoint: ep, manifest: manifest, hash: hash, fresh: time.Now(), client: client}
	}
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
		if err := r.opts.OnCallerBindings(ctx, bindings); err != nil {
			r.mu.Lock()
			r.services = map[string]registered{}
			r.lastRefreshErr = err
			r.mu.Unlock()
			return err
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
	if err := json.Unmarshal(raw, &doc); err != nil {
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
		if seen[module.ID] || reserved(module.ID) || module.ID == "history" || module.ID == "artifacts" {
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
		if method.TimeoutMS < 1 || method.TimeoutMS > 300000 || method.MaxResultBytes < 1 || method.MaxResultBytes > 10<<20 {
			return errors.New("invalid method limits")
		}
		if method.Effect != "read" && method.Effect != "write" && method.Effect != "delete" {
			return errors.New("invalid method effect")
		}
		for _, p := range []string{method.Path, method.StatusPath, method.CancelPath, method.Confirmation.PreparePath} {
			if p != "" {
				if err := validPath(p); err != nil {
					return err
				}
			}
		}
		if _, err := compileSchema(method.InputSchema); err != nil {
			return err
		}
		if _, err := compileSchema(method.OutputSchema); err != nil {
			return err
		}
		if method.Effect != "read" && (!method.Idempotency || method.StatusPath == "" || method.DeduplicationWindowSeconds < 86400) {
			return errors.New("unsafe write contract")
		}
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
	for _, segment := range strings.Split(decoded, "/") {
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
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid registered URI")
	}
	if err := validPath(u.EscapedPath()); err != nil {
		return nil, err
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
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ep.Address, port))
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("module redirects forbidden") }}, nil
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
	raw, _ := json.Marshal([]any{p.Issuer, p.Subject, p.Organization, p.Actor, p.PolicyRevision, permissions, s.manifest.ServiceID, s.hash, module.ID, hex.EncodeToString(token[:])})
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
	raw, err := invoke(ctx, s, p, module.DiscoveryPath, json.RawMessage("{}"), 256<<10, 5*time.Second)
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
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("discovery must contain one JSON value")
	}
	if len(response.Methods) > 200 {
		return nil, errors.New("too many dynamic methods")
	}
	for _, method := range response.Methods {
		if err := r.validateMethodShape(method); err != nil {
			return nil, err
		}
	}
	if err = validateMethods(response.Methods); err != nil {
		return nil, err
	}
	ttl := time.Duration(module.CacheTTLSeconds) * time.Second
	if ttl > r.opts.DynamicCacheCeiling {
		ttl = r.opts.DynamicCacheCeiling
	}
	if ttl > 0 {
		r.mu.Lock()
		if len(r.dynamic) >= 1024 {
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
			if previous, ok := seen[module.ID]; ok && previous != s.manifest.ServiceID {
				return nil, errors.New("ambiguous module id")
			}
			seen[module.ID] = s.manifest.ServiceID
			methods, err := r.methods(ctx, p, s, module)
			if err != nil {
				return nil, err
			}
			for _, method := range methods {
				if method.Effect == "read" && allowed(p, method.Permissions) {
					tools = append(tools, Tool{ServiceID: s.manifest.ServiceID, Module: module.ID, Method: method.ID, Description: method.Description, InputSchema: method.InputSchema})
				}
			}
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Module == tools[j].Module {
			return tools[i].Method < tools[j].Method
		}
		return tools[i].Module < tools[j].Module
	})
	return tools, nil
}
func (r *Registry) lookup(ctx context.Context, p Principal, moduleName, methodName string) (registered, Method, error) {
	return r.lookupWithFresh(ctx, p, moduleName, methodName, false)
}
func (r *Registry) lookupFresh(ctx context.Context, p Principal, moduleName, methodName string) (registered, Method, error) {
	return r.lookupWithFresh(ctx, p, moduleName, methodName, true)
}
func (r *Registry) lookupWithFresh(ctx context.Context, p Principal, moduleName, methodName string, fresh bool) (registered, Method, error) {
	var found *registered
	var module Module
	for _, s := range r.snapshot() {
		for _, m := range s.manifest.Modules {
			if m.ID == moduleName && allowed(p, m.Permissions) {
				if found != nil {
					return registered{}, Method{}, ErrUnavailable
				}
				copy := s
				found = &copy
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
		if m.ID == methodName {
			if !allowed(p, m.Permissions) {
				return registered{}, Method{}, ErrForbidden
			}
			return *found, m, nil
		}
	}
	return registered{}, Method{}, ErrUnavailable
}
func invoke(ctx context.Context, s registered, p Principal, path string, payload json.RawMessage, limit int64, timeout time.Duration) (json.RawMessage, error) {
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
func (r *Registry) BrokerLive(principal func(context.Context) (Principal, error), runID string, guard func(context.Context) error) sandbox.Broker {
	return sandbox.BrokerFunc(func(ctx context.Context, module, method string, args json.RawMessage) (json.RawMessage, error) {
		if len(args) > 65536 {
			return nil, errors.New("module arguments exceed 65536 bytes")
		}
		if guard != nil {
			if err := guard(ctx); err != nil {
				return nil, err
			}
		}
		p, err := principal(ctx)
		if err != nil {
			return nil, err
		}
		s, m, err := r.lookup(ctx, p, module, method)
		if err != nil {
			return nil, err
		}
		if m.Effect != "read" {
			return nil, ErrWriteDisabled
		}
		input, err := compileSchema(m.InputSchema)
		if err != nil {
			return nil, err
		}
		var doc any
		if err = json.Unmarshal(args, &doc); err != nil {
			return nil, err
		}
		if err = validateDocument(doc); err != nil {
			return nil, err
		}
		if err = input.Validate(doc); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if guard != nil {
			if err = guard(ctx); err != nil {
				return nil, err
			}
		}
		operationID := uuid.NewString()
		envelope, _ := json.Marshal(map[string]any{"protocol_version": 1, "operation_id": operationID, "run_id": runID, "arguments": doc})
		raw, err := invoke(ctx, s, p, m.Path, envelope, min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)), time.Duration(m.TimeoutMS)*time.Millisecond)
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
		if response.Status != "succeeded" {
			return nil, errors.New("read method did not succeed")
		}
		output, err := compileSchema(m.OutputSchema)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(response.Result, &doc); err != nil {
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
			result, err := json.Marshal(map[string]any{"result": response.Result, "artifacts": artifacts})
			if err != nil {
				return nil, err
			}
			return result, nil
		}
		return response.Result, nil
	})
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
			result = append(result, CallerBinding{ServiceID: service.manifest.ServiceID, Permissions: append([]string(nil), caller.Permissions...), Auth: caller.Auth})
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
	if err := json.Unmarshal(manifestSchema, &schemaDoc); err != nil {
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
	if err = json.Unmarshal(raw, &doc); err != nil {
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
			if previous, ok := seen[module.ID]; ok && previous != s.manifest.ServiceID {
				return nil, errors.New("ambiguous module id")
			}
			seen[module.ID] = s.manifest.ServiceID
			methods, err := r.methods(ctx, p, s, module)
			if err != nil {
				return nil, err
			}
			for _, method := range methods {
				if method.Effect != "read" && allowed(p, method.Permissions) {
					tools = append(tools, Tool{ServiceID: s.manifest.ServiceID, Module: module.ID, Method: method.ID, Description: method.Description, InputSchema: method.InputSchema, Effect: method.Effect, ConfirmationMode: method.Confirmation.Mode})
				}
			}
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Module == tools[j].Module {
			return tools[i].Method < tools[j].Method
		}
		return tools[i].Module < tools[j].Module
	})
	return tools, nil
}

// Bound data before schema checks so a script cannot force unbounded validation work.
func validateDocument(value any) error {
	nodes := 0
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		nodes++
		if nodes > 16384 || depth > 32 {
			return errors.New("module JSON complexity limit exceeded")
		}
		switch value := value.(type) {
		case map[string]any:
			if len(value) > 256 {
				return errors.New("module JSON object too large")
			}
			for _, v := range value {
				if err := walk(v, depth+1); err != nil {
					return err
				}
			}
		case []any:
			if len(value) > 4096 {
				return errors.New("module JSON array too large")
			}
			for _, v := range value {
				if err := walk(v, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, 0)
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
