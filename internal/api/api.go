package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/store"
)

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (domain.Principal, error)
}
type Options struct {
	ClearIdentityCache                              func(context.Context) (int64, error)
	RecoveryMode                                    bool
	OperatorLogin                                   *OperatorLogin
	DiagnosticsHealth                               func() domain.DiagnosticsHealth
	DebugRequest                                    func(context.Context, *http.Request, domain.Principal)
	DebugPayload                                    func(context.Context, string, any)
	RunCreated                                      func(context.Context, string)
	SSEBufferEvents                                 int
	AuthorizeExecution                              func(context.Context, domain.Run) (domain.Principal, error)
	DisconnectGrace                                 time.Duration
	LifecycleContext                                context.Context
	MaxWaitMS                                       int
	AdminOrganizationID                             string
	Artifacts                                       *artifacts.Service
	Ready                                           func(context.Context) error
	MaxBodyBytes                                    int64
	StreamDuration, StreamPoll, ReauthorizeInterval time.Duration
	SealCredential                                  func(context.Context, *http.Request, domain.Principal) (json.RawMessage, error)
}
type server struct {
	store   *store.Store
	auth    Authenticator
	options Options
	docs    huma.API
	streams streamTracker
}
type endpoint func(http.ResponseWriter, *http.Request, domain.Principal) error

const (
	jsonMediaType              = "application/json"
	defaultChatID              = "default"
	pauseCommand               = "pause"
	statusField                = "status"
	maxHistoryPageSize         = 100
	defaultHistoryPageSize     = 50
	maxSSEBufferEvents         = 200
	defaultSSEBufferEvents     = 128
	maxWaitMS                  = 30000
	maxRunArtifacts            = 16
	maxIdempotencyKeyLength    = 128
	defaultDisconnectGrace     = 5 * time.Second
	defaultStreamDuration      = 5 * time.Minute
	defaultStreamPoll          = 250 * time.Millisecond
	defaultReauthorizeInterval = 15 * time.Second
	requestReadTimeout         = 10 * time.Second
	defaultMaxBodyBytes        = 1 << 20
)

// New exposes durable operations. Store commits before HTTP success.
func New(repository *store.Store, authenticator Authenticator, options Options) http.Handler {
	options = defaultOptions(options)
	s := &server{
		store:   repository,
		auth:    authenticator,
		options: options,
		streams: streamTracker{runs: map[string]*streamState{}},
	}
	mux := http.NewServeMux()
	s.docs = documentation(mux)
	s.registerHealth(mux)
	s.registerRoutes(mux)
	s.registerDiagnostics(mux)
	s.registerMemory(mux)
	return mux
}

func defaultOptions(options Options) Options {
	if options.LifecycleContext == nil {
		options.LifecycleContext = context.Background()
	}
	if options.DisconnectGrace <= 0 {
		options.DisconnectGrace = defaultDisconnectGrace
	}
	if options.SSEBufferEvents <= 0 {
		options.SSEBufferEvents = defaultSSEBufferEvents
	}
	if options.SSEBufferEvents > maxSSEBufferEvents {
		options.SSEBufferEvents = maxSSEBufferEvents
	}
	if options.MaxWaitMS <= 0 || options.MaxWaitMS > maxWaitMS {
		options.MaxWaitMS = maxWaitMS
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = defaultMaxBodyBytes
	}
	if options.StreamDuration == 0 {
		options.StreamDuration = defaultStreamDuration
	}
	if options.StreamPoll == 0 {
		options.StreamPoll = defaultStreamPoll
	}
	if options.ReauthorizeInterval == 0 {
		options.ReauthorizeInterval = defaultReauthorizeInterval
	}
	return options
}

func (s *server) registerHealth(mux *http.ServeMux) {
	mux.HandleFunc(
		"GET /healthz",
		func(w http.ResponseWriter, _ *http.Request) {
			write(w, http.StatusOK, map[string]string{statusField: "ok"})
		},
	)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.options.RecoveryMode {
			fail(w, http.StatusServiceUnavailable, "recovery_mode", "Restore review is required", false)
			return
		}
		if s.options.Ready == nil {
			fail(w, http.StatusServiceUnavailable, "dependency_unavailable", "Readiness is unavailable", true)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.options.Ready(ctx); err != nil {
			fail(w, http.StatusServiceUnavailable, "dependency_unavailable", "Service is not ready", true)
			return
		}
		write(w, http.StatusOK, map[string]string{statusField: "ready"})
	})
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	s.route(mux, "POST /v1/auth/sessions", "chat.read", s.createSession)
	s.route(mux, "POST /v1/auth/sessions/{id}/reauthorize", "chat.read", s.reauthorizeSession)
	s.route(mux, "DELETE /v1/auth/sessions/{id}", "chat.read", s.revokeSession)
	s.route(mux, "POST /v1/chats", "chat.read", s.createChat)
	s.route(mux, "GET /v1/chats/default", "chat.read", s.defaultChat)
	s.route(mux, "DELETE /v1/chats/{id}", "chat.delete", s.deleteChat)
	s.route(mux, "GET /v1/chats/{id}/history", "chat.read", s.history)
	s.route(mux, "POST /v1/chats/{id}/events", "chat.events.append", s.appendEvent)
	s.route(mux, "POST /v1/chats/{id}/history/search", "chat.read", s.search)
	s.route(mux, "POST /v1/chats/{id}/history/read", "chat.read", s.readHistory)
	s.route(mux, "POST /v1/runs/{id}/artifacts/handles", "artifacts.read", s.createArtifactHandle)
	s.route(mux, "GET /v1/artifact-handles/{id}/content", "artifacts.read", s.handleContent)
	if s.store.SemanticSearchEnabled() {
		s.route(mux, "POST /v1/chats/{id}/history/embeddings", "chat.embeddings.write", s.indexEmbedding)
	}
	s.route(mux, "POST /v1/runs", "runs.create", s.createRun)
	s.route(mux, "GET /v1/runs/{id}", "chat.read", s.getRun)
	s.route(mux, "GET /v1/runs/{id}/events", "chat.read", s.events)
	s.route(mux, "POST /v1/runs/{id}/input", "runs.control", s.input)
	for _, command := range []string{pauseCommand, "resume", "cancel"} {
		s.route(mux, "POST /v1/runs/{id}/"+command, "runs.control", s.control(command))
	}
	s.route(mux, "GET /v1/models", "chat.read", s.models)
	s.route(mux, "GET /v1/users/me/preferences", "chat.read", s.preferences)
	s.route(mux, "PATCH /v1/users/me/preferences", "preferences.self.write", s.updatePreferences)
	s.route(mux, "GET /v1/users/me/credits", "chat.read", s.balance)
	s.route(mux, "GET /v1/admin/settings", "settings.read", s.settings)
	s.route(mux, "PATCH /v1/admin/settings", "settings.write", s.updateSettings)
	s.route(mux, "POST /v1/admin/identity/cache/clear", "settings.write", s.clearIdentityCache)
	s.route(mux, "GET /v1/admin/users/{id}/policy", "users.policy.write", s.policy)
	s.route(mux, "PATCH /v1/admin/users/{id}/policy", "users.policy.write", s.updatePolicy)
	s.route(mux, "GET /v1/admin/users/{id}/preferences", "preferences.other.write", s.userPreferences)
	s.route(mux, "PATCH /v1/admin/users/{id}/preferences", "preferences.other.write", s.updatePreferences)
	s.route(mux, "POST /v1/admin/billing/{id}/adjustments", "billing.adjust", s.adjustCredits)
	s.route(mux, "POST /v1/admin/runs/{id}/recovery/prepare", "operations.reconcile", s.prepareRecovery)
	s.route(mux, "POST /v1/admin/runs/{id}/steps/{step}/reconcile", "operations.reconcile", s.reconcileStep)
	s.route(mux, "POST /v1/artifacts", "artifacts.write", s.uploadArtifact)
	s.route(mux, "GET /v1/artifacts/{id}", "artifacts.read", s.artifactMetadata)
	s.route(mux, "GET /v1/artifacts/{id}/content", "artifacts.read", s.artifactContent)
}
func (s *server) route(mux *http.ServeMux, pattern, permission string, handler endpoint) {
	documentOperation(s.docs, pattern, permission)
	handler = s.recoveryEndpoint(pattern, handler)
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.auth == nil {
			fail(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization is unavailable", true)
			return
		}
		if freshAuthorization(pattern) {
			r = r.WithContext(identity.RequireFresh(r.Context()))
		}
		p, err := s.auth.Authenticate(r.Context(), r)
		if err != nil {
			s.error(w, err)
			return
		}
		if !validPrincipal(p) {
			fail(w, http.StatusUnauthorized, "unauthorized", "Valid authorization is required", false)
			return
		}
		if administrativeOrganizationRequired(r.URL.Path) &&
			(s.options.AdminOrganizationID == "" || p.OrgID != s.options.AdminOrganizationID) {
			fail(w, http.StatusForbidden, "forbidden", "Administrative organization is required", false)
			return
		}
		if s.options.DebugRequest != nil {
			s.options.DebugRequest(r.Context(), r, p)
		}
		if permission != "" && !p.Has(permission) {
			fail(w, http.StatusForbidden, "forbidden", "Permission is required", false)
			return
		}
		if handlerErr := handler(w, r, p); handlerErr != nil {
			s.error(w, handlerErr)
		}
	})
}

// recoveryEndpoint runs after authentication and route permission checks.
func (s *server) recoveryEndpoint(pattern string, handler endpoint) endpoint {
	if !s.options.RecoveryMode || recoveryRoute(pattern) {
		return handler
	}
	return func(http.ResponseWriter, *http.Request, domain.Principal) error {
		return apiError(http.StatusServiceUnavailable, "recovery_mode", "Restore review is required")
	}
}

// recoveryRoute allows only read-only diagnostics and explicit repair operations.
func recoveryRoute(pattern string) bool {
	switch pattern {
	case "GET /v1/admin/diagnostics",
		"GET /v1/admin/diagnostics/health",
		"GET /v1/admin/diagnostics/runs",
		"GET /v1/admin/diagnostics/runs/{id}",
		"GET /v1/admin/settings",
		"POST /v1/admin/identity/cache/clear",
		"POST /v1/admin/runs/{id}/steps/{step}/reconcile",
		"POST /v1/admin/runs/{id}/recovery/prepare",
		"POST /v1/runs/{id}/cancel":
		return true
	default:
		return false
	}
}
func (s *server) decode(w http.ResponseWriter, r *http.Request, value any) error {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(
		time.Now().Add(requestReadTimeout),
	); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return apiError(http.StatusRequestTimeout, "request_timeout", "Request body could not be read")
	}
	stop := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stop()
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != jsonMediaType {
		return apiError(
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"Content-Type must be application/json",
		)
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.options.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return apiError(http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large")
		}
		return apiError(http.StatusUnprocessableEntity, "invalid_request", "Request body could not be read")
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return apiError(http.StatusUnprocessableEntity, "invalid_request", "A JSON object is required")
	}
	if s.options.DebugPayload != nil {
		s.options.DebugPayload(r.Context(), "http_request", json.RawMessage(raw))
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if decodeErr := d.Decode(value); decodeErr != nil {
		return apiError(http.StatusUnprocessableEntity, "invalid_request", "Request body does not match the contract")
	}
	if trailingErr := d.Decode(new(any)); trailingErr != io.EOF {
		return apiError(http.StatusUnprocessableEntity, "invalid_request", "Exactly one JSON value is required")
	}
	return nil
}
func key(r *http.Request) (string, error) {
	v := r.Header.Get("Idempotency-Key")
	if len(v) == 0 || len(v) > maxIdempotencyKeyLength || strings.TrimSpace(v) != v ||
		strings.ContainsAny(v, "\x00\r\n") {
		return "", apiError(
			http.StatusUnprocessableEntity,
			"invalid_idempotency_key",
			"Idempotency-Key is required and must contain at most 128 characters",
		)
	}
	return v, nil
}
func cursor(r *http.Request) (int64, int, error) {
	after, err := strconv.ParseInt(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		return 0, 0, apiError(http.StatusUnprocessableEntity, "invalid_cursor", "Cursor must be a nonnegative integer")
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), strconv.Itoa(defaultHistoryPageSize)))
	if err != nil || limit < 1 || limit > maxHistoryPageSize {
		return 0, 0, apiError(http.StatusUnprocessableEntity, "invalid_limit", "Limit must be between 1 and 100")
	}
	return after, limit, nil
}
func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
func apiError(status int, code, message string) *domain.Error {
	return &domain.Error{Status: status, Code: code, Message: message}
}
func (s *server) error(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrUnauthorized) {
		fail(w, http.StatusUnauthorized, "unauthorized", "Valid authorization is required", false)
		return
	}
	if errors.Is(err, identity.ErrUnavailable) {
		fail(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization is unavailable", true)
		return
	}
	if e, ok := errors.AsType[*domain.Error](err); ok {
		message := e.Message
		if message == "" {
			message = strings.ReplaceAll(e.Code, "_", " ")
		}
		fail(w, e.Status, e.Code, message, e.Retryable)
		return
	}
	fail(w, http.StatusInternalServerError, "internal_error", "Request could not be completed", false)
}
func fail(w http.ResponseWriter, status int, code, message string, retryable bool) {
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		var id [12]byte
		_, _ = rand.Read(id[:])
		requestID = hex.EncodeToString(id[:])
		w.Header().Set("X-Request-ID", requestID)
	}
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="harness"`)
	}
	write(
		w,
		status,
		map[string]any{
			"error": map[string]any{
				"code":       code,
				"message":    message,
				"retryable":  retryable,
				"request_id": requestID,
			},
		},
	)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *server) createChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body struct{}
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.CreateChat(r.Context(), p)
	if err == nil {
		write(w, http.StatusCreated, v)
	}
	return err
}
func (s *server) defaultChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.DefaultChat(r.Context(), p)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) history(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	after, limit, err := cursor(r)
	if err != nil {
		return err
	}
	v, err := s.store.History(r.Context(), p, r.PathValue("id"), after, limit)
	if err == nil {
		write(w, http.StatusOK, historyResponse{Events: v})
	}
	return err
}
func (s *server) appendEvent(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.AppendEvent
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	id := r.PathValue("id")
	if id == defaultChatID {
		chat, err := s.store.DefaultChat(r.Context(), p)
		if err != nil {
			return err
		}
		id = chat.ID
	}
	v, err := s.store.AppendEvent(r.Context(), p, id, body)
	if err == nil {
		write(w, http.StatusCreated, v)
	}
	return err
}
func (s *server) search(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.SearchRequest
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.Search(r.Context(), p, r.PathValue("id"), body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) createRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if !p.Has("chat.read") {
		return apiError(http.StatusForbidden, "forbidden", "Chat read permission is required")
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	var body domain.CreateRun
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	if validationErr := s.validateRunRequest(&body, p); validationErr != nil {
		return validationErr
	}
	if artifactErr := s.validateRunArtifacts(r.Context(), p, body.Input.ArtifactIDs); artifactErr != nil {
		return artifactErr
	}
	if sessionErr := s.selectRunSession(r, p, &body); sessionErr != nil {
		return sessionErr
	}
	v, err := s.store.CreateRun(r.Context(), p, k, body)
	if err != nil {
		return err
	}
	if s.options.RunCreated != nil {
		s.options.RunCreated(r.Context(), v.ID)
	}
	status := http.StatusAccepted
	if body.Delivery.Mode == "wait" && body.Delivery.WaitMS > 0 {
		var cancelled bool
		v, status, cancelled, err = s.waitRun(r.Context(), p, v, body.Delivery.WaitMS)
		if err != nil || cancelled {
			return err
		}
	}
	w.Header().Set("Location", "/v1/runs/"+v.ID)
	write(w, status, v)
	return nil
}

func (s *server) validateRunRequest(body *domain.CreateRun, p domain.Principal) error {
	if body.Delivery.WaitMS < 0 || body.Delivery.WaitMS > s.options.MaxWaitMS {
		return apiError(
			http.StatusUnprocessableEntity,
			"invalid_delivery",
			fmt.Sprintf("wait_ms must be between 0 and %d", s.options.MaxWaitMS),
		)
	}
	if body.Delivery.Mode != "" && body.Delivery.Mode != "async" && body.Delivery.Mode != "wait" {
		return apiError(http.StatusUnprocessableEntity, "invalid_delivery", "Unsupported delivery mode")
	}
	if strings.TrimSpace(body.Input.Text) == "" && len(body.Input.ArtifactIDs) == 0 {
		return apiError(http.StatusUnprocessableEntity, "invalid_input", "Text or an artifact is required")
	}
	if len(body.Input.ArtifactIDs) > maxRunArtifacts {
		return apiError(http.StatusUnprocessableEntity, "invalid_input", "At most 16 artifacts are allowed")
	}
	if body.Interaction.Questions == "" {
		body.Interaction.Questions = "none"
	}
	if body.Interaction.Questions != "" && body.Interaction.Questions != "none" &&
		body.Interaction.Questions != "text" &&
		body.Interaction.Questions != "choices" &&
		body.Interaction.Questions != "choices_and_text" {
		return apiError(http.StatusUnprocessableEntity, "invalid_interaction", "Unsupported question mode")
	}
	if body.DisconnectPolicy == pauseCommand && !p.Has("runs.control") {
		return apiError(http.StatusForbidden, "forbidden", "Run control permission is required for disconnect pause")
	}
	if body.DisconnectPolicy != "" && body.DisconnectPolicy != "continue" && body.DisconnectPolicy != pauseCommand {
		return apiError(http.StatusUnprocessableEntity, "invalid_disconnect_policy", "Unsupported disconnect policy")
	}
	if body.Budget.MaxMicrocredits < 0 {
		return apiError(http.StatusUnprocessableEntity, "invalid_budget", "Budget must be nonnegative")
	}
	return nil
}

func (s *server) validateRunArtifacts(ctx context.Context, p domain.Principal, ids []string) error {
	if len(ids) > 0 {
		if s.options.Artifacts == nil {
			return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Artifact storage is unavailable")
		}
		owner, err := s.artifactPrincipal(ctx, p)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, metadataErr := s.options.Artifacts.Metadata(ctx, owner, id); metadataErr != nil {
				return artifactError(metadataErr)
			}
		}
	}
	return nil
}

func (s *server) selectRunSession(r *http.Request, p domain.Principal, body *domain.CreateRun) error {
	if body.AuthSessionID != "" && r.Header.Get("X-Subject-Token") != "" {
		return apiError(
			http.StatusUnprocessableEntity,
			"invalid_auth_session",
			"Use either subject credentials or a saved authorization session",
		)
	}
	if sessionID := r.Header.Get("X-Auth-Session-Id"); sessionID != "" {
		if body.AuthSessionID != "" && body.AuthSessionID != sessionID {
			return apiError(
				http.StatusUnprocessableEntity,
				"invalid_auth_session",
				"Header and request authorization sessions differ",
			)
		}
		if body.AuthSessionID == "" {
			body.AuthSessionID = sessionID
			body.AutoAuthSession = true
		}
	}
	if body.AuthSessionID == "" {
		session, err := s.bootstrap(r, p)
		if err != nil {
			return err
		}
		body.AuthSessionID = session.ID
		body.AutoAuthSession = true
	} else {
		if _, err := s.store.LoadAuthSession(r.Context(), p, body.AuthSessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) waitRun(
	ctx context.Context,
	p domain.Principal,
	run domain.Run,
	waitMS int,
) (domain.Run, int, bool, error) {
	deadline := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(s.options.StreamPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return run, http.StatusAccepted, true, nil
		case <-deadline.C:
			return run, http.StatusAccepted, false, nil
		case <-ticker.C:
			current, err := s.store.GetRun(ctx, p, run.ID)
			if err != nil {
				return run, http.StatusAccepted, false, err
			}
			run = current
			if run.State.Terminal() {
				return run, http.StatusOK, false, nil
			}
		}
	}
}
func (s *server) getRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.GetRun(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) input(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body inputRequest
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.AddInput(
		r.Context(),
		p,
		r.PathValue("id"),
		k,
		domain.RunInput{
			Kind:       body.Kind,
			Text:       body.Text,
			QuestionID: body.QuestionID,
			ChoiceIDs:  body.ChoiceIDs,
			ApprovalID: body.ApprovalID,
			Decision:   body.Decision,
		},
	)
	if err == nil {
		write(w, http.StatusOK, mutationReceipt(v))
	}
	return err
}
func (s *server) control(command string) endpoint {
	return func(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
		k, err := key(r)
		if err != nil {
			return err
		}
		if r.ContentLength != 0 {
			var body struct{}
			if decodeErr := s.decode(w, r, &body); decodeErr != nil {
				return decodeErr
			}
		}
		control := s.store.Control
		if s.options.RecoveryMode {
			control = s.store.ControlExistingUser
		}
		v, err := control(r.Context(), p, r.PathValue("id"), k, command)
		if err == nil {
			write(w, http.StatusOK, mutationReceipt(v))
		}
		return err
	}
}

func (s *server) events(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	after, err := eventCursor(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), p, id)
	if err != nil {
		return err
	}
	unsubscribe := s.subscribe(r.Context(), s.options.LifecycleContext, r, p, run)
	defer unsubscribe()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return apiError(http.StatusInternalServerError, "stream_unavailable", "Streaming is unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ctx, cancel := context.WithTimeout(r.Context(), s.options.StreamDuration)
	defer cancel()
	poll := time.NewTicker(s.options.StreamPoll)
	defer poll.Stop()
	auth := time.NewTicker(s.options.ReauthorizeInterval)
	defer auth.Stop()
	heartbeat := time.NewTicker(requestReadTimeout)
	defer heartbeat.Stop()
	nextAuth := time.Now().Add(s.options.ReauthorizeInterval)
	for {
		// Check authorization before every batch, including continuous backlog replay.
		var valid bool
		p, nextAuth, valid = s.streamAuthorization(ctx, r, p, nextAuth)
		if !valid {
			return nil
		}
		values, eventsErr := s.store.RunEvents(ctx, p, id, after, s.options.SSEBufferEvents)
		if eventsErr != nil {
			return nil
		}
		next, batchErr := writeEventBatch(w, values, after)
		if batchErr != nil {
			return nil
		}
		after = next
		flusher.Flush()
		if len(values) == s.options.SSEBufferEvents {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
		case <-auth.C:
			nextAuth = time.Now()
		case <-heartbeat.C:
			if _, heartbeatErr := io.WriteString(w, ": heartbeat\n\n"); heartbeatErr != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

func (s *server) streamAuthorization(
	ctx context.Context,
	r *http.Request,
	p domain.Principal,
	nextAuth time.Time,
) (domain.Principal, time.Time, bool) {
	if !time.Now().Before(nextAuth) {
		fresh, err := s.auth.Authenticate(ctx, r)
		if err != nil || !sameStreamPrincipal(p, fresh) {
			return p, nextAuth, false
		}
		p = fresh
		nextAuth = time.Now().Add(s.options.ReauthorizeInterval)
	}
	return p, nextAuth, p.ExpiresAt.IsZero() || time.Now().Before(p.ExpiresAt)
}

func eventCursor(r *http.Request) (int64, error) {
	if r.Header.Get("Last-Event-ID") != "" && r.URL.Query().Has("after") {
		return 0, apiError(http.StatusUnprocessableEntity, "invalid_cursor", "Use either Last-Event-ID or after")
	}
	afterText := defaultString(r.Header.Get("Last-Event-ID"), defaultString(r.URL.Query().Get("after"), "0"))
	after, err := strconv.ParseInt(afterText, 10, 64)
	if err != nil || after < 0 {
		return 0, apiError(http.StatusUnprocessableEntity, "invalid_cursor", "Event cursor must be nonnegative")
	}
	return after, nil
}

func sameStreamPrincipal(previous, fresh domain.Principal) bool {
	return fresh.Issuer == previous.Issuer && fresh.Subject == previous.Subject && fresh.Actor == previous.Actor &&
		fresh.OrgID == previous.OrgID && fresh.Has("chat.read")
}

func writeEventBatch(w http.ResponseWriter, events []domain.Event, after int64) (int64, error) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(
		time.Now().Add(defaultDisconnectGrace),
	); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return after, err
	}
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return after, err
		}
		if _, writeErr := fmt.Fprintf(
			w,
			"id: %d\nevent: %s\ndata: %s\n\n",
			event.Seq,
			event.Type,
			payload,
		); writeErr != nil {
			return after, writeErr
		}
		after = event.Seq
	}
	return after, nil
}
func (s *server) models(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Models(r.Context(), p)
	if err == nil {
		write(w, http.StatusOK, modelsResponse{Models: v})
	}
	return err
}
func (s *server) preferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Preferences(r.Context(), p)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) updatePreferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.Preferences
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.UpdatePreferences(r.Context(), p, r.PathValue("id"), body.Version, body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) settings(w http.ResponseWriter, r *http.Request, _ domain.Principal) error {
	v, err := s.store.GetSettings(r.Context())
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) updateSettings(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.Settings
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.UpdateSettings(r.Context(), p, body.Version, body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) policy(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Policy(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) updatePolicy(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.UserPolicy
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.UpdatePolicy(r.Context(), p, r.PathValue("id"), body.Version, body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) balance(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Balance(r.Context(), p)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) adjustCredits(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body billingAdjustmentRequest
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.AdjustCredits(r.Context(), p, r.PathValue("id"), k, body.Amount, body.Reason)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) bootstrap(r *http.Request, p domain.Principal) (domain.AuthSession, error) {
	if s.options.SealCredential == nil {
		return domain.AuthSession{}, apiError(
			http.StatusServiceUnavailable,
			"dependency_unavailable",
			"Credential storage is unavailable",
		)
	}
	envelope, err := s.options.SealCredential(r.Context(), r, p)
	if err != nil {
		return domain.AuthSession{}, err
	}
	return s.store.CreateAuthSession(r.Context(), p, envelope, p.ExpiresAt)
}
func (s *server) createSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body struct{}
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.bootstrap(r, p)
	if err == nil {
		write(
			w,
			http.StatusCreated,
			authSessionReceipt{ID: v.ID, ExpiresAt: v.ExpiresAt, Version: v.Version},
		)
	}
	return err
}
func (s *server) reauthorizeSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body reauthorizeSessionRequest
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	if s.options.SealCredential == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Credential storage is unavailable")
	}
	envelope, err := s.options.SealCredential(r.Context(), r, p)
	if err != nil {
		return err
	}
	v, err := s.store.ReauthorizeAuthSession(r.Context(), p, r.PathValue("id"), envelope, p.ExpiresAt, body.Version)
	if err == nil {
		write(
			w,
			http.StatusOK,
			authSessionReceipt{ID: v.ID, ExpiresAt: v.ExpiresAt, Version: v.Version},
		)
	}
	return err
}
func (s *server) revokeSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if err := s.store.RevokeAuthSession(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) userPreferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.UserPreferences(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}

type inputRequest struct {
	Kind       string   `json:"kind"`
	Text       string   `json:"text,omitempty"`
	QuestionID string   `json:"question_id,omitempty"`
	ChoiceIDs  []string `json:"choice_ids,omitempty"`
	ApprovalID string   `json:"approval_id,omitempty"`
	Decision   string   `json:"decision,omitempty"`
}

func (s *server) reconcileStep(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body domain.ReconcileStepRequest
	if decodeErr := s.decode(w, r, &body); decodeErr != nil {
		return decodeErr
	}
	v, err := s.store.ReconcileStep(r.Context(), p, r.PathValue("id"), r.PathValue("step"), k, body)
	if err != nil {
		return err
	}
	write(
		w,
		http.StatusOK,
		reconciliationReceipt{
			RunID: v.ID, Status: v.State, Reason: v.Reason,
			UnresolvedSteps:   v.UnresolvedSteps,
			SpentMicrocredits: v.SpentMicrocredits, ReservedMicrocredits: v.ReservedMicrocredits,
		},
	)
	return nil
}

func (s *server) deleteChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if r.PathValue("id") == defaultChatID {
		return apiError(http.StatusUnprocessableEntity, "invalid_chat_id", "Use the explicit chat ID for deletion")
	}
	if err := s.store.DeleteChat(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type runMutationReceipt struct {
	ID      string          `json:"run_id"`
	State   domain.RunState `json:"status"`
	Reason  string          `json:"reason,omitempty"`
	Version int64           `json:"version"`
}

func mutationReceipt(run domain.Run) runMutationReceipt {
	return runMutationReceipt{ID: run.ID, State: run.State, Reason: run.Reason, Version: run.Version}
}
