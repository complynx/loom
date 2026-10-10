package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
	"github.com/complynx/clx-scripter/internal/lifecycle"
	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/operations"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/complynx/clx-scripter/internal/sandbox"
	"github.com/complynx/clx-scripter/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type executeRun struct {
	RunID string `json:"run_id"`
}

func (executeRun) Kind() string { return "clx_execute_run" }

type runWorker struct {
	river.WorkerDefaults[executeRun]
	engine     *runs.Engine
	repository *store.Store
}

func (w *runWorker) Work(ctx context.Context, job *river.Job[executeRun]) error {
	r, err := w.repository.ExecutionRun(ctx, job.Args.RunID)
	if err != nil {
		return err
	}
	if r.State != domain.Queued && r.State != domain.Running {
		return nil
	}
	return w.engine.Execute(ctx, job.Args.RunID)
}

// Timeout is enforced by Engine from the admitted run snapshot and active usage.
func (w *runWorker) Timeout(*river.Job[executeRun]) time.Duration { return -1 }

type providerRouter map[string]provider.Client

func (p providerRouter) Respond(ctx context.Context, req provider.Request) (provider.Response, error) {
	client, ok := p[req.Provider]
	if !ok {
		return provider.Response{}, &provider.Failure{Code: "model_unavailable"}
	}
	return client.Respond(ctx, req)
}

func (p providerRouter) CountInputTokens(ctx context.Context, req provider.Request) (int64, error) {
	client, ok := p[req.Provider]
	if !ok {
		return 0, errors.New("model unavailable")
	}
	counter, ok := client.(provider.TokenCounter)
	if !ok {
		return 0, errors.New("token counting unavailable")
	}
	return counter.CountInputTokens(ctx, req)
}

type deploymentSource []modules.DockerSource

func (s deploymentSource) Discover(ctx context.Context) ([]modules.Endpoint, error) {
	out := []modules.Endpoint{}
	seen := map[string]bool{}
	for _, source := range s {
		entries, err := source.Discover(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !seen[entry.ContainerID] {
				out = append(out, entry)
				seen[entry.ContainerID] = true
			}
		}
	}
	return out, nil
}

// Serve uses a separate worker supervisor. It never evaluates JavaScript inside
// the API process, and all mutation dispatch passes through a fenced coordinator.
func Serve(ctx context.Context, cfg config.Config) error {
	if runtime.GOOS != "linux" {
		return errors.New("serve requires Linux with an isolated sandbox supervisor")
	}
	if cfg.Sandbox.ParallelRPCCeiling > 8 {
		return errors.New("sandbox parallel RPC ceiling cannot exceed 8")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	var level slog.Level
	if cfg.Observability.LogLevel != "" {
		if e := level.UnmarshalText([]byte(cfg.Observability.LogLevel)); e != nil {
			return errors.New("invalid log level")
		}
	}
	if cfg.Observability.LogPayloads {
		return errors.New("payload logging is not supported")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	runtimeCtx, cancelRuntime := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuntime()
	pc, err := pgxpool.ParseConfig(cfg.Database.DSN)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	pc.MaxConns = cfg.Database.MaxConnections
	pc.ConnConfig.ConnectTimeout = 3 * time.Second
	pc.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", cfg.Database.StatementTimeoutMS)
	pc.ConnConfig.RuntimeParams["lock_timeout"] = fmt.Sprintf("%d", cfg.Database.LockTimeoutMS)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return errors.New("database unavailable")
	}
	cleanupPool := func() {
		closed := make(chan struct{})
		go func() { pool.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
	}
	startupOK := false
	defer func() {
		if !startupOK {
			cleanupPool()
		}
	}()
	if err = pool.Ping(ctx); err != nil {
		return errors.New("database unavailable")
	}
	var queue *river.Client[pgx.Tx]
	repository := store.New(pool, func(c context.Context, tx pgx.Tx, id string) error {
		if queue == nil {
			return errors.New("queue unavailable")
		}
		_, e := queue.InsertTx(c, tx, executeRun{RunID: id}, &river.InsertOpts{MaxAttempts: 5})
		return e
	})
	repository.SetPolicyCeilings(cfg.Execution.ActiveRunsPerUserCeiling, cfg.Execution.ActiveRunsTotalCeiling)
	aliases := make([]string, 0, len(cfg.Providers))
	for alias := range cfg.Providers {
		aliases = append(aliases, alias)
	}
	repository.SetAllowedProviders(aliases)
	settings, err := bootstrapSettings(cfg)
	if err != nil {
		return err
	}
	if err = repository.Bootstrap(ctx, settings); err != nil {
		return errors.New("settings unavailable; run migrations first")
	}
	key, err := config.LoadEncryptionKey(cfg.Identity.EncryptionKeyFile)
	if err != nil {
		return errors.New("session encryption key unavailable")
	}
	cipher, err := identity.NewSessionCipherForClient(cfg.Identity.EncryptionKeyID, key, cfg.Identity.Audience, cfg.Identity.ClientID)
	clear(key)
	if err != nil {
		return err
	}
	sources := deploymentSource{}
	if cfg.Discovery.Label != "" && cfg.Discovery.Label != modules.ServiceLabel {
		return errors.New("unsupported discovery label")
	}
	for _, network := range cfg.Discovery.AllowedNetworks {
		source := modules.DockerSource{Network: network, BaseURL: cfg.Discovery.DockerAPI}
		if strings.HasPrefix(cfg.Discovery.DockerAPI, "unix://") {
			source.UnixSocket = strings.TrimPrefix(cfg.Discovery.DockerAPI, "unix://")
			source.BaseURL = ""
		}
		sources = append(sources, source)
	}
	var files *artifacts.Service
	registry, err := modules.NewRegistry(sources, modules.Options{OnCallerBindings: func(c context.Context, callers []modules.CallerBinding) error {
		for _, caller := range callers {
			if caller.Auth.Type == "zitadel" && caller.Auth.Issuer == cfg.Identity.Issuer && caller.Auth.Audience == cfg.Identity.Audience {
				if e := repository.RememberServiceIdentity(c, caller.Auth.Issuer, caller.Auth.Subject, caller.Auth.Audience); e != nil {
					return e
				}
			}
		}
		return nil
	}, ImportArtifact: func(c context.Context, p modules.Principal, d modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
		if files == nil {
			return nil, errors.New("artifact storage unavailable")
		}
		r, e := repository.ExecutionRun(c, source.RunID)
		if e != nil {
			return nil, e
		}
		if p.UserID != r.OwnerID || p.Actor != r.ActorID || p.Organization != r.OrgID {
			return nil, domain.Forbidden()
		}
		item, e := files.Import(c, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, artifacts.Source{ServiceID: source.ServiceID, ExternalDisclosureAllowed: source.ExternalDisclosure, BaseURL: source.BaseURL, Client: source.Client, Headers: source.Headers, AllowedMIMETypes: source.AllowedMIMETypes, Authorize: source.Authorize}, artifacts.Descriptor{Path: d.DownloadPath, MIME: d.MIMEType, Size: d.SizeBytes, SHA256: d.SHA256})
		if e != nil {
			return nil, e
		}
		if e = repository.AttachChatArtifact(c, domain.Principal{Issuer: p.Issuer, Subject: p.Subject, Actor: p.Actor, OrgID: p.Organization}, r.ChatID, item.ID); e != nil {
			return nil, e
		}
		return json.Marshal(item)
	}, RequestTimeout: time.Duration(cfg.Discovery.RequestTimeoutMS) * time.Millisecond, DynamicCacheCeiling: time.Duration(cfg.Discovery.DynamicCacheCeilingSeconds) * time.Second, FreshTTL: time.Duration(cfg.Discovery.FreshnessTTLSeconds) * time.Second, PollInterval: time.Duration(cfg.Discovery.PollSeconds) * time.Second, ManifestLimit: cfg.Discovery.MaxManifestBytes})
	if err != nil {
		return err
	}
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 3*time.Second)
	err = registry.Refresh(refreshCtx)
	refreshCancel()
	if err != nil {
		logger.Warn("module discovery unavailable")
	}
	checkService := func(c context.Context, p domain.Principal) (identity.Binding, error) {
		return callerBinding(c, registry, cfg.Identity, p)
	}
	auth, err := identity.New(cfg.Identity, &http.Client{}, func(c context.Context, _ *http.Request, p domain.Principal) (identity.Binding, error) {
		return checkService(c, p)
	})
	if err != nil {
		return err
	}
	err = auth.UseServiceClassifier(func(c context.Context, p domain.Principal) (bool, error) {
		if registry.TrustReady() != nil {
			return false, identity.ErrUnavailable
		}

		return repository.IsKnownServiceIdentity(c, p.Issuer, p.Subject, cfg.Identity.Audience)
	})
	if err != nil {
		return err
	}
	err = auth.UseSessions(cipher, func(c context.Context, id string, p domain.Principal) (domain.Principal, json.RawMessage, error) {
		s, e := repository.LoadAuthSessionForActor(c, p, id)
		if e != nil {
			return domain.Principal{}, nil, e
		}
		return sessionPrincipal(s), s.Envelope, nil
	}, checkService)
	if err != nil {
		return err
	}
	authorize := RunAuthorizer(func(c context.Context, r domain.Run) (domain.Principal, string, error) {
		session, e := repository.ExecutionAuthSession(c, r.ID)
		if e == nil && session.RevokedAt != nil {
			e = identity.ErrUnauthorized
		}
		if e != nil {
			return domain.Principal{}, "", e
		}
		p, token, e := auth.VerifySession(c, cipher, sessionPrincipal(session), session.Envelope, checkService)
		if e != nil {
			return domain.Principal{}, "", e
		}
		if !p.Has("runs.create") || !p.Has("chat.read") {
			return domain.Principal{}, "", domain.Forbidden()
		}
		if e = repository.CheckRunPolicy(c, r.ID); e != nil {
			return domain.Principal{}, "", e
		}
		current, e := repository.GetSettings(c)
		if e != nil {
			return domain.Principal{}, "", e
		}
		if !current.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure {
			return domain.Principal{}, "", &runs.Suspension{State: domain.Paused, Reason: "disclosure_denied"}
		}
		return p, token, nil
	})
	files, err = artifacts.New(cfg.Artifacts.Root, repository, artifacts.Limits{MaxUploadBytes: cfg.Artifacts.MaxUploadBytes, QuotaBytes: cfg.Artifacts.QuotaPerUserBytes})
	if err != nil {
		return errors.New("artifact volume unavailable")
	}
	defer func() {
		if !startupOK {
			_ = files.Close()
		}
	}()
	router := providerRouter{}
	for name, p := range cfg.Providers {
		client, e := provider.NewResponses(p.Endpoint, p.APIKey, &http.Client{Timeout: 2 * time.Minute})
		if e != nil {
			return e
		}
		router[name] = client
	}

	executor := sandbox.Client{Socket: cfg.Sandbox.Socket, Timeout: time.Duration(cfg.Sandbox.ScriptTimeoutCeilingMS) * time.Millisecond}
	coordinator := &operations.Coordinator{Pool: pool, ApprovalTTL: time.Duration(cfg.BootstrapSettings.ApprovalWaitSeconds) * time.Second}
	bridge := NewModuleBridge(registry, coordinator, repository, authorize)
	repository.SetApprovalHandler(bridge.ApproveTx)
	hooks := runtimeTools{cfg: cfg, registry: registry, repository: repository, files: files, executor: executor, authorize: authorize, bridge: bridge}
	engine := runs.New(repository, router, runs.Options{Lease: time.Duration(cfg.Execution.LeaseSeconds) * time.Second, InferenceTimeout: 2 * time.Minute, MaxInputBytes: 8 << 20, ContextInputTokenBudget: cfg.BootstrapSettings.ContextInputTokenBudget, Heartbeat: time.Duration(cfg.Execution.HeartbeatSeconds) * time.Second, Authorize: authorize, Tools: hooks.tools, Script: hooks.script, Artifacts: hooks.modelArtifacts, ModuleCall: bridge.Call, DiscardCall: bridge.Discard})
	workers := river.NewWorkers()
	river.AddWorker(workers, &runWorker{engine: engine, repository: repository})
	maxWorkers := cfg.Execution.ActiveRunsTotalCeiling
	if maxWorkers <= 0 {
		maxWorkers = 32
	}
	queue, err = river.NewClient(riverpgxv5.New(pool), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}}, JobTimeout: time.Duration(cfg.BootstrapSettings.RunActiveTimeSeconds) * time.Second, Logger: logger, FetchPollInterval: time.Second, MaxAttempts: 5})
	if err != nil {
		return errors.New("queue configuration invalid")
	}
	if err = queue.Start(runtimeCtx); err != nil {
		return errors.New("queue unavailable")
	}
	go registry.Run(runtimeCtx)
	go lifecycle.New(repository, time.Minute).Run(runtimeCtx)
	go recoverExhaustedQueue(runtimeCtx, repository, logger)
	go reconcileArtifacts(runtimeCtx, files, logger)
	var draining atomic.Bool
	ready := func(c context.Context) error {
		if draining.Load() {
			return errors.New("draining")
		}
		if registry.TrustReady() != nil {
			return errors.New("discovery trust unavailable")
		}
		if e := pool.Ping(c); e != nil {
			return e
		}
		value, e := executor.Execute(c, "return true;", nil, sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("readiness has no modules")
		}))
		if e != nil || string(value) != "true" {
			return errors.New("sandbox unavailable")
		}
		return nil
	}
	handler := api.New(repository, auth, api.Options{Artifacts: files, Ready: ready, MaxBodyBytes: cfg.HTTP.MaxRequestBytes, MaxWaitMS: cfg.HTTP.MaxWaitMS, SSEBufferEvents: cfg.HTTP.SSEBufferEvents, AdminOrganizationID: cfg.Identity.AdminOrganizationID, SealCredential: cipher.SealRequest, LifecycleContext: runtimeCtx, DisconnectGrace: time.Duration(cfg.HTTP.DisconnectGraceMS) * time.Millisecond, AuthorizeExecution: func(c context.Context, r domain.Run) (domain.Principal, error) {
		p, _, e := authorize(c, r)
		return p, e
	}})
	server := &http.Server{Addr: cfg.HTTP.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return runtimeCtx }}
	listener, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = queue.Stop(stopCtx)
		return errors.New("HTTP listener unavailable")
	}
	startupOK = true
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	logger.Info("runtime started")
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveResult:
	}
	draining.Store(true)
	cancelRuntime()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(cfg.Execution.ShutdownDeadlineMS)*time.Millisecond)
	defer shutdownCancel()
	var shutdown sync.WaitGroup
	for _, stop := range []func(context.Context) error{engine.Stop, queue.Stop, server.Shutdown} {
		shutdown.Add(1)
		go func(stop func(context.Context) error) {
			defer shutdown.Done()
			if stop(shutdownCtx) != nil {
				logger.Warn("shutdown component deadline exceeded")
			}
		}(stop)
	}
	done := make(chan struct{})
	go func() { shutdown.Wait(); _ = files.Close(); pool.Close(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		_ = server.Close()
		logger.Warn("shutdown checkpoint deadline exceeded")
	}
	logger.Info("runtime stopped")
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}
func sessionPrincipal(s domain.AuthSession) domain.Principal {
	return domain.Principal{Issuer: s.Issuer, Subject: s.Subject, Actor: s.Actor, OrgID: s.OrgID, ExpiresAt: s.ExpiresAt}
}
func callerBinding(_ context.Context, registry *modules.Registry, c config.Identity, p domain.Principal) (identity.Binding, error) {
	count := 0
	binding := identity.Binding{}
	for _, caller := range registry.CallerBindings() {
		if caller.Auth.Type != "zitadel" || caller.Auth.Issuer != p.Issuer || caller.Auth.Subject != p.Subject || caller.Auth.Audience != c.Audience {
			continue
		}
		if registry.CheckCaller(caller.ServiceID) != nil {
			continue
		}
		count++
		binding = identity.Binding{Actor: p.Subject, Permissions: map[string]bool{}}
		for _, permission := range caller.Permissions {
			binding.Permissions[permission] = true
		}
	}
	if count != 1 {
		return identity.Binding{}, identity.ErrUnauthorized
	}
	return binding, nil
}
