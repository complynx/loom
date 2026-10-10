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

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
	"github.com/complynx/clx-scripter/internal/store"
	"github.com/danielgtaylor/huma/v2"
)

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (domain.Principal, error)
}
type Options struct {
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

// New exposes durable operations. Store commits before HTTP success.
func New(repository *store.Store, authenticator Authenticator, options Options) http.Handler {
	if options.LifecycleContext == nil {
		options.LifecycleContext = context.Background()
	}
	if options.DisconnectGrace <= 0 {
		options.DisconnectGrace = 5 * time.Second
	}
	if options.SSEBufferEvents <= 0 {
		options.SSEBufferEvents = 128
	}
	if options.SSEBufferEvents > 200 {
		options.SSEBufferEvents = 200
	}
	if options.MaxWaitMS <= 0 || options.MaxWaitMS > 30000 {
		options.MaxWaitMS = 30000
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = 1 << 20
	}
	if options.StreamDuration == 0 {
		options.StreamDuration = 5 * time.Minute
	}
	if options.StreamPoll == 0 {
		options.StreamPoll = 250 * time.Millisecond
	}
	if options.ReauthorizeInterval == 0 {
		options.ReauthorizeInterval = 15 * time.Second
	}
	s := &server{store: repository, auth: authenticator, options: options, streams: streamTracker{runs: map[string]*streamState{}}}
	mux := http.NewServeMux()
	s.docs = documentation(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if options.Ready == nil {
			fail(w, 503, "dependency_unavailable", "Readiness is unavailable", true)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := options.Ready(ctx); err != nil {
			fail(w, 503, "dependency_unavailable", "Service is not ready", true)
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	s.route(mux, "POST /v1/auth/sessions", "chat.read", s.createSession)
	s.route(mux, "POST /v1/auth/sessions/{id}/reauthorize", "chat.read", s.reauthorizeSession)
	s.route(mux, "DELETE /v1/auth/sessions/{id}", "chat.read", s.revokeSession)
	s.route(mux, "POST /v1/chats", "chat.read", s.createChat)
	s.route(mux, "GET /v1/chats/default", "chat.read", s.defaultChat)
	s.route(mux, "DELETE /v1/chats/{id}", "chat.delete", s.deleteChat)
	s.route(mux, "GET /v1/chats/{id}/history", "chat.read", s.history)
	s.route(mux, "POST /v1/chats/{id}/events", "chat.events.append", s.appendEvent)
	s.route(mux, "POST /v1/chats/{id}/history/search", "chat.read", s.search)
	s.route(mux, "POST /v1/runs", "runs.create", s.createRun)
	s.route(mux, "GET /v1/runs/{id}", "chat.read", s.getRun)
	s.route(mux, "GET /v1/runs/{id}/events", "chat.read", s.events)
	s.route(mux, "POST /v1/runs/{id}/input", "runs.control", s.input)
	for _, command := range []string{"pause", "resume", "cancel"} {
		s.route(mux, "POST /v1/runs/{id}/"+command, "runs.control", s.control(command))
	}
	s.route(mux, "GET /v1/models", "chat.read", s.models)
	s.route(mux, "GET /v1/users/me/preferences", "chat.read", s.preferences)
	s.route(mux, "PATCH /v1/users/me/preferences", "preferences.self.write", s.updatePreferences)
	s.route(mux, "GET /v1/users/me/credits", "chat.read", s.balance)
	s.route(mux, "GET /v1/admin/settings", "settings.read", s.settings)
	s.route(mux, "PATCH /v1/admin/settings", "settings.write", s.updateSettings)
	s.route(mux, "GET /v1/admin/users/{id}/policy", "users.policy.write", s.policy)
	s.route(mux, "PATCH /v1/admin/users/{id}/policy", "users.policy.write", s.updatePolicy)
	s.route(mux, "GET /v1/admin/users/{id}/preferences", "preferences.other.write", s.userPreferences)
	s.route(mux, "PATCH /v1/admin/users/{id}/preferences", "preferences.other.write", s.updatePreferences)
	s.route(mux, "POST /v1/admin/billing/{id}/adjustments", "billing.adjust", s.adjustCredits)
	s.route(mux, "POST /v1/admin/runs/{id}/steps/{step}/reconcile", "operations.reconcile", s.reconcileStep)
	s.route(mux, "POST /v1/artifacts", "artifacts.write", s.uploadArtifact)
	s.route(mux, "GET /v1/artifacts/{id}", "artifacts.read", s.artifactMetadata)
	s.route(mux, "GET /v1/artifacts/{id}/content", "artifacts.read", s.artifactContent)
	return mux
}
func (s *server) route(mux *http.ServeMux, pattern, permission string, handler endpoint) {
	documentOperation(s.docs, pattern, permission)
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.auth == nil {
			fail(w, 503, "dependency_unavailable", "Authorization is unavailable", true)
			return
		}
		p, err := s.auth.Authenticate(r.Context(), r)
		if err != nil {
			s.error(w, err)
			return
		}
		if p.Subject == "" || p.OrgID == "" || (!p.ExpiresAt.IsZero() && !time.Now().Before(p.ExpiresAt)) {
			fail(w, 401, "unauthorized", "Valid authorization is required", false)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/admin/settings") && (s.options.AdminOrganizationID == "" || p.OrgID != s.options.AdminOrganizationID) {
			fail(w, 403, "forbidden", "Administrative organization is required", false)
			return
		}
		if !p.Has(permission) {
			fail(w, 403, "forbidden", "Permission is required", false)
			return
		}
		if err := handler(w, r, p); err != nil {
			s.error(w, err)
		}
	})
}
func (s *server) decode(w http.ResponseWriter, r *http.Request, value any) error {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return apiError(408, "request_timeout", "Request body could not be read")
	}
	stop := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stop()
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		return apiError(415, "unsupported_media_type", "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.options.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return apiError(413, "body_too_large", "Request body is too large")
		}
		return apiError(422, "invalid_request", "Request body could not be read")
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return apiError(422, "invalid_request", "A JSON object is required")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return apiError(422, "invalid_request", "Request body does not match the contract")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return apiError(422, "invalid_request", "Exactly one JSON value is required")
	}
	return nil
}
func key(r *http.Request) (string, error) {
	v := r.Header.Get("Idempotency-Key")
	if len(v) == 0 || len(v) > 128 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
		return "", apiError(422, "invalid_idempotency_key", "Idempotency-Key is required and must contain at most 128 characters")
	}
	return v, nil
}
func cursor(r *http.Request) (int64, int, error) {
	after, err := strconv.ParseInt(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		return 0, 0, apiError(422, "invalid_cursor", "Cursor must be a nonnegative integer")
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil || limit < 1 || limit > 100 {
		return 0, 0, apiError(422, "invalid_limit", "Limit must be between 1 and 100")
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
		fail(w, 401, "unauthorized", "Valid authorization is required", false)
		return
	}
	if errors.Is(err, identity.ErrUnavailable) {
		fail(w, 503, "dependency_unavailable", "Authorization is unavailable", true)
		return
	}
	var e *domain.Error
	if errors.As(err, &e) {
		message := e.Message
		if message == "" {
			message = strings.ReplaceAll(e.Code, "_", " ")
		}
		fail(w, e.Status, e.Code, message, e.Retryable)
		return
	}
	fail(w, 500, "internal_error", "Request could not be completed", false)
}
func fail(w http.ResponseWriter, status int, code, message string, retryable bool) {
	var id [12]byte
	_, _ = rand.Read(id[:])
	if status < 400 || status > 599 {
		status = 500
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="harness"`)
	}
	write(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": retryable, "request_id": hex.EncodeToString(id[:])}})
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *server) createChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body struct{}
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.CreateChat(r.Context(), p)
	if err == nil {
		write(w, 201, v)
	}
	return err
}
func (s *server) defaultChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.DefaultChat(r.Context(), p)
	if err == nil {
		write(w, 200, v)
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
		write(w, 200, map[string]any{"events": v})
	}
	return err
}
func (s *server) appendEvent(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.AppendEvent
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	id := r.PathValue("id")
	if id == "default" {
		chat, err := s.store.DefaultChat(r.Context(), p)
		if err != nil {
			return err
		}
		id = chat.ID
	}
	v, err := s.store.AppendEvent(r.Context(), p, id, body)
	if err == nil {
		write(w, 201, v)
	}
	return err
}
func (s *server) search(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.SearchRequest
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.Search(r.Context(), p, r.PathValue("id"), body)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) createRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if !p.Has("chat.read") {
		return apiError(403, "forbidden", "Chat read permission is required")
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	var body domain.CreateRun
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	if body.Delivery.WaitMS < 0 || body.Delivery.WaitMS > s.options.MaxWaitMS {
		return apiError(422, "invalid_delivery", fmt.Sprintf("wait_ms must be between 0 and %d", s.options.MaxWaitMS))
	}
	if body.Delivery.Mode != "" && body.Delivery.Mode != "async" && body.Delivery.Mode != "wait" {
		return apiError(422, "invalid_delivery", "Unsupported delivery mode")
	}
	if strings.TrimSpace(body.Input.Text) == "" && len(body.Input.ArtifactIDs) == 0 {
		return apiError(422, "invalid_input", "Text or an artifact is required")
	}
	if len(body.Input.ArtifactIDs) > 16 {
		return apiError(422, "invalid_input", "At most 16 artifacts are allowed")
	}
	if body.Interaction.Questions == "" {
		body.Interaction.Questions = "none"
	}
	if body.Interaction.Questions != "" && body.Interaction.Questions != "none" && body.Interaction.Questions != "text" && body.Interaction.Questions != "choices" && body.Interaction.Questions != "choices_and_text" {
		return apiError(422, "invalid_interaction", "Unsupported question mode")
	}
	if body.DisconnectPolicy == "pause" && !p.Has("runs.control") {
		return apiError(403, "forbidden", "Run control permission is required for disconnect pause")
	}
	if body.DisconnectPolicy != "" && body.DisconnectPolicy != "continue" && body.DisconnectPolicy != "pause" {
		return apiError(422, "invalid_disconnect_policy", "Unsupported disconnect policy")
	}
	if body.Budget.MaxMicrocredits < 0 {
		return apiError(422, "invalid_budget", "Budget must be nonnegative")
	}
	if len(body.Input.ArtifactIDs) > 0 {
		if s.options.Artifacts == nil {
			return apiError(503, "dependency_unavailable", "Artifact storage is unavailable")
		}
		owner, err := s.artifactPrincipal(r.Context(), p)
		if err != nil {
			return err
		}
		for _, id := range body.Input.ArtifactIDs {
			if _, err := s.options.Artifacts.Metadata(r.Context(), owner, id); err != nil {
				return artifactError(err)
			}
		}
	}
	if body.AuthSessionID != "" && r.Header.Get("X-Subject-Token") != "" {
		return apiError(422, "invalid_auth_session", "Use either subject credentials or a saved authorization session")
	}
	if sessionID := r.Header.Get("X-Auth-Session-ID"); sessionID != "" {
		if body.AuthSessionID != "" && body.AuthSessionID != sessionID {
			return apiError(422, "invalid_auth_session", "Header and request authorization sessions differ")
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
	v, err := s.store.CreateRun(r.Context(), p, k, body)
	if err != nil {
		return err
	}
	status := 202
	if body.Delivery.Mode == "wait" && body.Delivery.WaitMS > 0 {
		deadline := time.NewTimer(time.Duration(body.Delivery.WaitMS) * time.Millisecond)
		defer deadline.Stop()
		ticker := time.NewTicker(s.options.StreamPoll)
		defer ticker.Stop()
	waiting:
		for {
			select {
			case <-r.Context().Done():
				return nil
			case <-deadline.C:
				break waiting
			case <-ticker.C:
				v, err = s.store.GetRun(r.Context(), p, v.ID)
				if err != nil {
					return err
				}
				if v.State.Terminal() {
					status = 200
					break waiting
				}
			}
		}
	}
	w.Header().Set("Location", "/v1/runs/"+v.ID)
	write(w, status, v)
	return nil
}
func (s *server) getRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.GetRun(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) input(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body inputRequest
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.AddInput(r.Context(), p, r.PathValue("id"), k, domain.RunInput{Kind: body.Kind, Text: body.Text, QuestionID: body.QuestionID, ChoiceIDs: body.ChoiceIDs, ApprovalID: body.ApprovalID, Decision: body.Decision})
	if err == nil {
		write(w, 200, mutationReceipt(v))
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
			if err := s.decode(w, r, &body); err != nil {
				return err
			}
		}
		v, err := s.store.Control(r.Context(), p, r.PathValue("id"), k, command)
		if err == nil {
			write(w, 200, mutationReceipt(v))
		}
		return err
	}
}

func (s *server) events(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if r.Header.Get("Last-Event-ID") != "" && r.URL.Query().Has("after") {
		return apiError(422, "invalid_cursor", "Use either Last-Event-ID or after")
	}
	afterText := defaultString(r.Header.Get("Last-Event-ID"), defaultString(r.URL.Query().Get("after"), "0"))
	after, err := strconv.ParseInt(afterText, 10, 64)
	if err != nil || after < 0 {
		return apiError(422, "invalid_cursor", "Event cursor must be nonnegative")
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
		return apiError(500, "stream_unavailable", "Streaming is unavailable")
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
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	nextAuth := time.Now().Add(s.options.ReauthorizeInterval)
	for {
		// Check authorization before every batch, including continuous backlog replay.
		if !time.Now().Before(nextAuth) {
			fresh, err := s.auth.Authenticate(ctx, r)
			if err != nil || fresh.Issuer != p.Issuer || fresh.Subject != p.Subject || fresh.Actor != p.Actor || fresh.OrgID != p.OrgID || !fresh.Has("chat.read") {
				return nil
			}
			p = fresh
			nextAuth = time.Now().Add(s.options.ReauthorizeInterval)
		}
		if !p.ExpiresAt.IsZero() && !time.Now().Before(p.ExpiresAt) {
			return nil
		}
		values, err := s.store.RunEvents(ctx, p, id, after, s.options.SSEBufferEvents)
		if err != nil {
			return nil
		}
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		for _, event := range values {
			payload, err := json.Marshal(event)
			if err != nil {
				return nil
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, payload); err != nil {
				return nil
			}
			after = event.Seq
		}
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
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
func (s *server) models(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Models(r.Context(), p)
	if err == nil {
		write(w, 200, map[string]any{"models": v})
	}
	return err
}
func (s *server) preferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Preferences(r.Context(), p)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) updatePreferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.Preferences
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.UpdatePreferences(r.Context(), p, r.PathValue("id"), body.Version, body)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) settings(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.GetSettings(r.Context())
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) updateSettings(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.Settings
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.UpdateSettings(r.Context(), p, body.Version, body)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) policy(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Policy(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) updatePolicy(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.UserPolicy
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.UpdatePolicy(r.Context(), p, r.PathValue("id"), body.Version, body)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) balance(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.Balance(r.Context(), p)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) adjustCredits(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body struct {
		Amount int64  `json:"amount_microcredits"`
		Reason string `json:"reason"`
	}
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.AdjustCredits(r.Context(), p, r.PathValue("id"), k, body.Amount, body.Reason)
	if err == nil {
		write(w, 200, v)
	}
	return err
}
func (s *server) bootstrap(r *http.Request, p domain.Principal) (domain.AuthSession, error) {
	if s.options.SealCredential == nil {
		return domain.AuthSession{}, apiError(503, "dependency_unavailable", "Credential storage is unavailable")
	}
	envelope, err := s.options.SealCredential(r.Context(), r, p)
	if err != nil {
		return domain.AuthSession{}, err
	}
	return s.store.CreateAuthSession(r.Context(), p, envelope, p.ExpiresAt)
}
func (s *server) createSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body struct{}
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.bootstrap(r, p)
	if err == nil {
		write(w, 201, map[string]any{"auth_session_id": v.ID, "expires_at": v.ExpiresAt, "version": v.Version})
	}
	return err
}
func (s *server) reauthorizeSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body struct {
		Version int64 `json:"version"`
	}
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	if s.options.SealCredential == nil {
		return apiError(503, "dependency_unavailable", "Credential storage is unavailable")
	}
	envelope, err := s.options.SealCredential(r.Context(), r, p)
	if err != nil {
		return err
	}
	v, err := s.store.ReauthorizeAuthSession(r.Context(), p, r.PathValue("id"), envelope, p.ExpiresAt, body.Version)
	if err == nil {
		write(w, 200, map[string]any{"auth_session_id": v.ID, "expires_at": v.ExpiresAt, "version": v.Version})
	}
	return err
}
func (s *server) revokeSession(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if err := s.store.RevokeAuthSession(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *server) userPreferences(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.UserPreferences(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, 200, v)
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
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.ReconcileStep(r.Context(), p, r.PathValue("id"), r.PathValue("step"), k, body)
	if err != nil {
		return err
	}
	write(w, 200, map[string]any{"run_id": v.ID, "status": v.State, "reason": v.Reason, "unresolved_steps": v.UnresolvedSteps, "spent_microcredits": v.SpentMicrocredits, "reserved_microcredits": v.ReservedMicrocredits})
	return nil
}

func (s *server) deleteChat(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if r.PathValue("id") == "default" {
		return apiError(422, "invalid_chat_id", "Use the explicit chat ID for deletion")
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
