package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/lifecycle"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
	"github.com/complynx/loom/internal/transcription"
)

const zitadelCallerType = "zitadel"

type executeRun struct {
	RunID string `json:"run_id"`
}

func (executeRun) Kind() string { return "clx_execute_run" }

type runWorker struct {
	river.WorkerDefaults[executeRun]

	engine     *runs.Engine
	repository *store.Store
	observer   *telemetry.Telemetry
}

func (w *runWorker) Work(ctx context.Context, job *river.Job[executeRun]) (err error) {
	ctx = telemetry.WithRun(ctx, job.Args.RunID)
	ctx, finishTrace := w.observer.Start(ctx, "run_execution", job.Args.RunID)
	defer func() { finishTrace(err) }()
	w.observer.Event(ctx, "run execution started", "run_id", job.Args.RunID)
	started := time.Now()
	defer func() { w.observer.Operation(ctx, "run_execution", operationOutcome(err), job.Args.RunID, started) }()
	defer func() {
		checkpoint, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
		defer cancel()
		if final, e := w.repository.ExecutionRun(checkpoint, job.Args.RunID); e == nil {
			w.observer.Event(
				context.WithoutCancel(ctx),
				"run checkpoint",
				"run_id",
				final.ID,
				"state",
				string(final.State),
			)
		}
	}()
	r, err := w.repository.ExecutionRun(ctx, job.Args.RunID)
	if err != nil {
		return err
	}
	if r.State != domain.Queued && r.State != domain.Running {
		return nil
	}
	return w.executeWithRetry(ctx, job)
}

// Timeout is enforced by Engine from the admitted run snapshot and active usage.
func (w *runWorker) Timeout(*river.Job[executeRun]) time.Duration { return -1 }

type providerRouter map[string]provider.Client

func (p providerRouter) SupportsCompaction(req provider.Request) bool {
	client := p[req.Provider]
	if capability, ok := client.(interface{ SupportsCompaction(provider.Request) bool }); ok {
		return capability.SupportsCompaction(req)
	}
	_, ok := client.(provider.CompactClient)
	return ok
}

func (p providerRouter) Compact(ctx context.Context, req provider.Request) (provider.Response, error) {
	client, ok := p[req.Provider].(provider.CompactClient)
	if !ok {
		return provider.Response{}, &provider.Failure{Code: "compaction_unavailable"}
	}
	return client.Compact(ctx, req)
}

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
	logger, observer, stopTelemetry, err := runtimeTelemetry(ctx, cfg)
	if err != nil {
		return err
	}
	runtimeCtx, cancelRuntime := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuntime()
	resources := &runtimeComponents{}
	startupOK := false
	defer func() {
		if !startupOK {
			resources.closeStartup()
			flushTelemetry(ctx, stopTelemetry)
		}
	}()
	if err = resources.initStorage(ctx, cfg); err != nil {
		return err
	}
	if err = resources.initAgent(ctx, cfg, logger, observer); err != nil {
		return err
	}
	if err = resources.startBackground(runtimeCtx, cfg, logger); err != nil {
		return err
	}
	queue := resources.queue
	var draining atomic.Bool
	ready := runtimeFileReadiness(
		cfg,
		observer,
		runtimeReadiness(&draining, resources.registry, resources.pool, resources.executor),
	)
	operatorLogin, err := api.NewOperatorLogin(cfg.Identity.Issuer, cfg.Identity.Audience, cfg.Operator)
	if err != nil {
		return err
	}
	handler := runtimeHandler(
		runtimeCtx,
		cfg,
		resources.repository,
		resources.auth,
		observer,
		resources.files,
		ready,
		resources.cipher,
		resources.authorize,
		operatorLogin,
	)
	server, listener, metricsServer, err := runtimeServers(
		ctx,
		runtimeCtx,
		cfg,
		handler,
		observer,
		logger,
		resources.repository,
		resources.registry,
		resources.executor,
		queue,
	)
	if err != nil {
		return err
	}
	startupOK = true
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	logger.InfoContext(ctx,
		"runtime started",
		"recovery_mode", cfg.Execution.RecoveryMode,
		"metrics_enabled",
		metricsServer != nil,
		"workers",
		resources.maxWorkers,
		"log_payloads",
		cfg.Observability.LogPayloads,
	)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveResult:
	}
	draining.Store(true)
	cancelRuntime()
	shutdownRuntime(
		ctx,
		cfg,
		resources.engine,
		queue,
		server,
		metricsServer,
		resources.files,
		resources.pool,
		logger,
		stopTelemetry,
	)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}
func sessionPrincipal(s domain.AuthSession) domain.Principal {
	return domain.Principal{
		Issuer:    s.Issuer,
		Subject:   s.Subject,
		Actor:     s.Actor,
		OrgID:     s.OrgID,
		ExpiresAt: s.ExpiresAt,
	}
}

func callerBinding(
	_ context.Context,
	registry *modules.Registry,
	c config.Identity,
	p domain.Principal,
) (identity.Binding, error) {
	count := 0
	binding := identity.Binding{}
	for _, caller := range registry.CallerBindings() {
		if caller.Auth.Type != zitadelCallerType || caller.Auth.Issuer != p.Issuer ||
			caller.Auth.Subject != p.Subject ||
			caller.Auth.Audience != c.Audience {
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
	if count == 0 {
		return identity.Binding{}, &domain.Error{
			Status:    http.StatusServiceUnavailable,
			Code:      "caller_binding_unavailable",
			Message:   "Current caller registration is unavailable",
			Retryable: true,
		}
	}
	if count != 1 {
		return identity.Binding{}, identity.ErrUnauthorized
	}
	return binding, nil
}

func executionAuthorizer(
	repository *store.Store,
	auth *identity.Authenticator,
	cipher *identity.SessionCipher,
	checkService func(context.Context, domain.Principal) (identity.Binding, error),
	observer *telemetry.Telemetry,
) RunAuthorizer {
	authorize := func(c context.Context, r domain.Run) (domain.Principal, string, error) {
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
	}
	return func(c context.Context, r domain.Run) (domain.Principal, string, error) {
		started := time.Now()
		var err error
		c, finishTrace := observer.Start(c, "authorization", r.ID)
		defer func() { finishTrace(err) }()
		defer func() { observer.Operation(c, "authorization", operationOutcome(err), r.ID, started) }()
		principal, token, err := authorize(c, r)
		return principal, token, err
	}
}

func runtimeRegistry(
	ctx context.Context,
	cfg config.Config,
	repository *store.Store,
	files **artifacts.Service,
	observedServices *sync.Map,
	logger *slog.Logger,
) (*modules.Registry, error) {
	sources := deploymentSource{}
	if cfg.Discovery.Label != "" && cfg.Discovery.Label != modules.ServiceLabel {
		return nil, errors.New("unsupported discovery label")
	}
	for _, network := range cfg.Discovery.AllowedNetworks {
		source := modules.DockerSource{Network: network, BaseURL: cfg.Discovery.DockerAPI}
		if socket, ok := strings.CutPrefix(cfg.Discovery.DockerAPI, "unix://"); ok {
			source.UnixSocket = socket
			source.BaseURL = ""
		}
		sources = append(sources, source)
	}
	bindings := rememberCallerBindings(cfg.Identity, repository)
	importArtifact := importModuleArtifact(repository, files)
	if cfg.Execution.RecoveryMode {
		bindings = func(_ context.Context, callers []modules.CallerBinding) error {
			for _, caller := range callers {
				if caller.Auth.Type == zitadelCallerType && caller.Auth.Issuer == cfg.Identity.Issuer &&
					caller.Auth.Audience == cfg.Identity.Audience {
					observedServices.Store(
						serviceIdentity{caller.Auth.Issuer, caller.Auth.Subject, caller.Auth.Audience},
						true,
					)
				}
			}
			return nil
		}
		importArtifact = nil
	}
	registry, err := modules.NewRegistry(
		sources,
		modules.Options{
			OnCallerBindings:    bindings,
			WrapTransport:       func(base http.RoundTripper) http.RoundTripper { return telemetry.TraceTransport{Base: base} },
			ImportArtifact:      importArtifact,
			RequestTimeout:      time.Duration(cfg.Discovery.RequestTimeoutMS) * time.Millisecond,
			DynamicCacheCeiling: time.Duration(cfg.Discovery.DynamicCacheCeilingSeconds) * time.Second,
			FreshTTL:            time.Duration(cfg.Discovery.FreshnessTTLSeconds) * time.Second,
			PollInterval:        time.Duration(cfg.Discovery.PollSeconds) * time.Second,
			ManifestLimit:       cfg.Discovery.MaxManifestBytes,
		},
	)
	if err != nil {
		return nil, err
	}
	refreshCtx, refreshCancel := context.WithTimeout(ctx, discoveryRefreshTimeout)
	err = registry.Refresh(refreshCtx)
	refreshCancel()
	if err != nil {
		logger.WarnContext(ctx, "module discovery unavailable")
	}

	return registry, nil
}

func runtimeAuthenticator(
	cfg config.Config,
	repository *store.Store,
	registry *modules.Registry,
	cipher *identity.SessionCipher,
	checkService func(context.Context, domain.Principal) (identity.Binding, error),
	observedServices *sync.Map,
) (*identity.Authenticator, error) {
	auth, err := identity.New(
		cfg.Identity,
		&http.Client{Transport: telemetry.TraceTransport{}},
		func(c context.Context, _ *http.Request, p domain.Principal) (identity.Binding, error) {
			return checkService(c, p)
		},
	)
	if err != nil {
		return nil, err
	}
	err = auth.UseServiceClassifier(func(c context.Context, p domain.Principal) (bool, error) {
		if registry.TrustReady() != nil {
			return false, identity.ErrUnavailable
		}

		if _, known := observedServices.Load(serviceIdentity{p.Issuer, p.Subject, cfg.Identity.Audience}); known {
			return true, nil
		}
		return repository.IsKnownServiceIdentity(c, p.Issuer, p.Subject, cfg.Identity.Audience)
	})
	if err != nil {
		return nil, err
	}
	err = auth.UseSessions(
		cipher,
		func(c context.Context, id string, p domain.Principal) (domain.Principal, json.RawMessage, error) {
			s, e := repository.LoadAuthSessionForActor(c, p, id)
			if e != nil {
				return domain.Principal{}, nil, e
			}
			return sessionPrincipal(s), s.Envelope, nil
		},
		checkService,
	)
	if err != nil {
		return nil, err
	}

	return auth, nil
}

func runtimeEngine(
	cfg config.Config,
	repository *store.Store,
	registry *modules.Registry,
	files *artifacts.Service,
	pool *pgxpool.Pool,
	authorize RunAuthorizer,
	observer *telemetry.Telemetry,
) (*runs.Engine, sandbox.Client, error) {
	router := providerRouter{}
	for name, p := range cfg.Providers {
		client, e := configuredProvider(p)
		if e != nil {
			return nil, sandbox.Client{}, e
		}
		router[name] = measuredProvider{client: client, observer: observer}
	}

	executor := sandbox.Client{
		Socket:  cfg.Sandbox.Socket,
		Timeout: time.Duration(cfg.Sandbox.ScriptTimeoutCeilingMS) * time.Millisecond,
	}
	coordinator := &operations.Coordinator{
		Pool:        pool,
		ApprovalTTL: time.Duration(cfg.BootstrapSettings.ApprovalWaitSeconds) * time.Second,
	}
	bridge := NewModuleBridge(registry, coordinator, repository, authorize)
	bridge.observer = observer
	repository.SetApprovalHandler(bridge.ApproveTx)
	var speech transcription.Provider
	if cfg.Transcription.Enabled {
		var speechErr error
		speech, speechErr = transcription.NewOpenAI(
			transcription.Options{
				Endpoint:   cfg.Transcription.Endpoint,
				APIKey:     cfg.Transcription.APIKey,
				HTTPClient: &http.Client{Transport: telemetry.TraceTransport{}},
			},
		)
		if speechErr != nil {
			return nil, sandbox.Client{}, speechErr
		}
	}
	hooks := runtimeTools{
		transcriber: speech,
		cfg:         cfg,
		registry:    registry,
		repository:  repository,
		files:       files,
		executor:    executor,
		authorize:   authorize,
		bridge:      bridge,
		observer:    observer,
	}
	engine := runs.New(
		repository,
		router,
		runs.Options{
			PaidTool:                hooks,
			PaidToolReplay:          hooks.replayPaidTool,
			SponsoredAttemptLimit:   cfg.Execution.SponsoredAttemptLimit,
			RetryAttemptLimit:       cfg.Execution.ProviderRetryAttemptLimit,
			RetryWindow:             time.Duration(cfg.Execution.ProviderRetryWindowSeconds) * time.Second,
			RetryShortLimit:         cfg.Execution.ProviderRetryShortLimit,
			RetryShortBudget:        time.Duration(cfg.Execution.ProviderRetryShortBudgetMS) * time.Millisecond,
			Lease:                   time.Duration(cfg.Execution.LeaseSeconds) * time.Second,
			InferenceTimeout:        inferenceTimeout,
			MaxInputBytes:           modelMaxInputBytes,
			ContextInputTokenBudget: cfg.BootstrapSettings.ContextInputTokenBudget,
			Heartbeat:               time.Duration(cfg.Execution.HeartbeatSeconds) * time.Second,
			Authorize:               authorize,
			Tools:                   hooks.tools,
			Script:                  hooks.script,
			Artifacts:               hooks.modelArtifacts,
			Attachment:              hooks.attachArtifact,
			ModuleCall: func(c context.Context, r domain.Run, p domain.Principal, token string, call provider.Call) (json.RawMessage, *domain.Approval, error) {
				started := time.Now()
				var err error
				c, finishTrace := observer.Start(c, "module_call", r.ID)
				defer func() { finishTrace(err) }()
				defer func() { observer.Operation(c, "module_call", operationOutcome(err), r.ID, started) }()
				result, approval, err := bridge.Call(c, r, p, token, call)
				return result, approval, err
			},
			DiscardCall: bridge.Discard,
		},
	)

	return engine, executor, nil
}

func runtimeQueue(
	cfg config.Config,
	pool *pgxpool.Pool,
	engine *runs.Engine,
	repository *store.Store,
	observer *telemetry.Telemetry,
	logger *slog.Logger,
) (*river.Client[pgx.Tx], int, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &runWorker{engine: engine, repository: repository, observer: observer})
	maxWorkers := cfg.Execution.ActiveRunsTotalCeiling
	if maxWorkers <= 0 {
		maxWorkers = 32
	}
	queue, err := river.NewClient(
		riverpgxv5.New(pool),
		&river.Config{
			Workers:           workers,
			Queues:            map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}},
			JobTimeout:        time.Duration(cfg.BootstrapSettings.RunActiveTimeSeconds) * time.Second,
			Logger:            slog.New(queueLogHandler{next: logger.Handler()}),
			FetchPollInterval: time.Second,
			MaxAttempts:       queueMaxAttempts,
		},
	)
	if err != nil {
		return nil, 0, errors.New("queue configuration invalid")
	}

	return queue, maxWorkers, nil
}

func runtimeReadiness(
	draining *atomic.Bool,
	registry *modules.Registry,
	pool *pgxpool.Pool,
	executor sandbox.Client,
) func(context.Context) error {
	return func(c context.Context) error {
		if draining.Load() {
			return errors.New("draining")
		}
		if registry.TrustReady() != nil {
			return errors.New("discovery trust unavailable")
		}
		if e := pool.Ping(c); e != nil {
			return e
		}
		value, e := executor.Execute(
			c,
			"return true;",
			nil,
			sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
				return nil, errors.New("readiness has no modules")
			}),
		)
		if e != nil || string(value) != "true" {
			return errors.New("sandbox unavailable")
		}
		return nil
	}
}

func runtimeHandler(
	runtimeCtx context.Context,
	cfg config.Config,
	repository *store.Store,
	auth *identity.Authenticator,
	observer *telemetry.Telemetry,
	files *artifacts.Service,
	ready func(context.Context) error,
	cipher *identity.SessionCipher,
	authorize RunAuthorizer,
	operatorLogin *api.OperatorLogin,
) http.Handler {
	return api.New(
		repository,
		measuredAuth{auth: auth, observer: observer},
		api.Options{
			RecoveryMode:      cfg.Execution.RecoveryMode,
			OperatorLogin:     operatorLogin,
			DiagnosticsHealth: observer.DiagnosticsHealth,
			DebugRequest:      observer.DebugRequest,
			DebugPayload:      observer.DebugPayload,
			RunCreated: func(c context.Context, id string) {
				observer.Event(c, "run admitted", "request_id", telemetry.RequestID(c), "run_id", id)
			},
			Artifacts:           files,
			Ready:               ready,
			MaxBodyBytes:        cfg.HTTP.MaxRequestBytes,
			MaxWaitMS:           cfg.HTTP.MaxWaitMS,
			SSEBufferEvents:     cfg.HTTP.SSEBufferEvents,
			AdminOrganizationID: cfg.Identity.AdminOrganizationID,
			SealCredential:      cipher.SealRequest,
			LifecycleContext:    runtimeCtx,
			DisconnectGrace:     time.Duration(cfg.HTTP.DisconnectGraceMS) * time.Millisecond,
			AuthorizeExecution: func(c context.Context, r domain.Run) (domain.Principal, error) {
				p, _, e := authorize(c, r)
				return p, e
			},
		},
	)
}

func runtimeServers(
	ctx, runtimeCtx context.Context,
	cfg config.Config,
	handler http.Handler,
	observer *telemetry.Telemetry,
	logger *slog.Logger,
	repository *store.Store,
	registry *modules.Registry,
	executor sandbox.Client,
	queue *river.Client[pgx.Tx],
) (*http.Server, net.Listener, *http.Server, error) {
	server := &http.Server{
		Addr:              cfg.HTTP.Listen,
		Handler:           observer.HTTP(handler),
		ReadHeaderTimeout: apiReadHeaderTimeout,
		IdleTimeout:       apiIdleTimeout,
		MaxHeaderBytes:    apiMaxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return runtimeCtx },
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTP.Listen)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if queue != nil {
			_ = queue.Stop(stopCtx)
		}
		return nil, nil, nil, errors.New("HTTP listener unavailable")
	}
	var metricsServer *http.Server
	if cfg.Observability.MetricsListen != "" {
		metricsListener, e := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Observability.MetricsListen)
		if e != nil {
			_ = listener.Close()
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer stopCancel()
			if queue != nil {
				_ = queue.Stop(stopCtx)
			}
			return nil, nil, nil, errors.New("metrics listener unavailable")
		}
		metricsServer = &http.Server{
			Handler:           observer,
			ReadHeaderTimeout: metricsRequestTimeout,
			WriteTimeout:      metricsRequestTimeout,
			IdleTimeout:       metricsIdleTimeout,
			MaxHeaderBytes:    metricsMaxHeaderBytes,
		}
		go func() {
			if metricsErr := metricsServer.Serve(
				metricsListener,
			); metricsErr != nil &&
				!errors.Is(metricsErr, http.ErrServerClosed) {
				logger.ErrorContext(runtimeCtx, "metrics listener stopped")
			}
		}()
		go collectTelemetry(
			runtimeCtx,
			repository,
			observer,
			registry.TrustReady,
			func(c context.Context) bool { return !cfg.Execution.RecoveryMode && executor.Health(c) == nil },
			cfg.Artifacts.Root,
			cfg.FileTools,
		)
	}

	return server, listener, metricsServer, nil
}

func shutdownRuntime(
	ctx context.Context,
	cfg config.Config,
	engine *runs.Engine,
	queue *river.Client[pgx.Tx],
	server, metricsServer *http.Server,
	files *artifacts.Service,
	pool *pgxpool.Pool,
	logger *slog.Logger,
	stopTelemetry func(context.Context) error,
) {
	shutdownStarted := time.Now()
	stops := []func(context.Context) error{server.Shutdown}
	if engine != nil {
		stops = append(stops, engine.Stop)
	}
	if queue != nil {
		stops = append(stops, queue.Stop)
	}
	if metricsServer != nil {
		stops = append(stops, metricsServer.Shutdown)
	}
	components := make([]func(context.Context), 0, len(stops))
	for _, stop := range stops {
		components = append(components, func(shutdownCtx context.Context) {
			if stop(shutdownCtx) != nil {
				logger.WarnContext(shutdownCtx, "shutdown component deadline exceeded")
			}
		})
	}
	complete := lifecycle.Shutdown(ctx,
		time.Duration(cfg.Execution.ShutdownDeadlineMS)*time.Millisecond,
		components,
		func() {
			if files != nil {
				_ = files.Close()
			}
			pool.Close()
			flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer flushCancel()
			if err := stopTelemetry(flushCtx); err != nil {
				logger.WarnContext(flushCtx, "telemetry shutdown incomplete")
			}
		},
	)
	if !complete {
		// Forced HTTP closure must not extend the process exit budget.
		go func() {
			_ = server.Close()
			if metricsServer != nil {
				_ = metricsServer.Close()
			}
		}()
		logger.WarnContext(ctx, "shutdown checkpoint deadline exceeded")
	}
	logger.InfoContext(ctx,
		"runtime stopped",
		"duration_ms",
		time.Since(shutdownStarted).Milliseconds(),
		"deadline_exceeded",
		!complete,
	)
}

func runtimePool(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	pc.MaxConns = cfg.MaxConnections
	pc.ConnConfig.ConnectTimeout = databaseConnectTimeout
	pc.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(cfg.StatementTimeoutMS)
	pc.ConnConfig.RuntimeParams["lock_timeout"] = strconv.Itoa(cfg.LockTimeoutMS)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("database unavailable")
	}

	return pool, nil
}

func runtimeObserver(cfg config.Config) (*slog.Logger, *telemetry.Telemetry, error) {
	if runtime.GOOS != "linux" {
		return nil, nil, errors.New("serve requires Linux with an isolated sandbox supervisor")
	}
	if cfg.Sandbox.ParallelRPCCeiling > parallelRPCLimit {
		return nil, nil, errors.New("sandbox parallel RPC ceiling cannot exceed 8")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	var level slog.Level
	if cfg.Observability.LogLevel != "" {
		if e := level.UnmarshalText([]byte(cfg.Observability.LogLevel)); e != nil {
			return nil, nil, errors.New("invalid log level")
		}
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).
		With("service", "loom")
	logger = telemetry.CorrelateLogger(logger)
	observer := telemetry.New(logger)
	observer.EnablePayloads(cfg.Observability.LogPayloads)
	observer.SetRecoveryMode(cfg.Execution.RecoveryMode)

	return logger, observer, nil
}

// serviceIdentity preserves observed caller classification through recovery discovery removals.
type serviceIdentity struct{ issuer, subject, audience string }

type runtimeComponents struct {
	observedServices sync.Map
	pool             *pgxpool.Pool
	repository       *store.Store
	cipher           *identity.SessionCipher
	registry         *modules.Registry
	files            *artifacts.Service
	auth             *identity.Authenticator
	authorize        RunAuthorizer
	engine           *runs.Engine
	executor         sandbox.Client
	queue            *river.Client[pgx.Tx]
	maxWorkers       int
}

func (r *runtimeComponents) initStorage(ctx context.Context, cfg config.Config) error {
	var err error
	r.pool, err = runtimePool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	if err = r.pool.Ping(ctx); err != nil {
		return errors.New("database unavailable")
	}
	r.repository = store.New(r.pool, func(c context.Context, tx pgx.Tx, id string) error {
		if r.queue == nil {
			return errors.New("queue unavailable")
		}
		_, e := r.queue.InsertTx(c, tx, executeRun{RunID: id}, &river.InsertOpts{MaxAttempts: queueMaxAttempts})
		return e
	})
	r.repository.SetSemanticSearchEnabled(cfg.Search.SemanticEnabled)
	r.repository.SetPolicyCeilings(cfg.Execution.ActiveRunsPerUserCeiling, cfg.Execution.ActiveRunsTotalCeiling)
	r.repository.SetExecutionCeilings(
		cfg.Sandbox.ScriptTimeoutCeilingMS,
		cfg.Sandbox.ParallelRPCCeiling,
		cfg.BootstrapSettings.ContextInputTokenBudget,
		domain.Settings{
			TotalActiveRunLimit:     cfg.Execution.ActiveRunsTotalCeiling,
			ScriptTimeoutMS:         cfg.BootstrapSettings.ScriptTimeoutMS,
			ParallelRPCLimit:        cfg.BootstrapSettings.ParallelRPCLimit,
			ContextInputTokenBudget: cfg.BootstrapSettings.ContextInputTokenBudget,
		},
	)
	aliases := make([]string, 0, len(cfg.Providers))
	for alias := range cfg.Providers {
		aliases = append(aliases, alias)
	}
	r.repository.SetAllowedProviders(aliases)
	if cfg.Execution.RecoveryMode {
		if _, err = r.repository.GetSettings(ctx); err != nil {
			return errors.New("restored settings unavailable")
		}
	} else {
		settings, settingsErr := bootstrapSettings(cfg)
		if settingsErr != nil {
			return settingsErr
		}
		if err = r.repository.Bootstrap(ctx, settings); err != nil {
			return errors.New("settings unavailable; run migrations first")
		}
	}
	r.cipher, err = loadSessionCipher(cfg.Identity)
	if err != nil {
		return err
	}
	if err = r.repository.CheckSessionKeys(ctx, r.cipher.HasKey); err != nil {
		return err
	}

	return nil
}

func (r *runtimeComponents) initAgent(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	observer *telemetry.Telemetry,
) error {
	var err error
	r.registry, err = runtimeRegistry(ctx, cfg, r.repository, &r.files, &r.observedServices, logger)
	if err != nil {
		return err
	}
	checkService := func(c context.Context, p domain.Principal) (identity.Binding, error) {
		return callerBinding(c, r.registry, cfg.Identity, p)
	}
	r.auth, err = runtimeAuthenticator(cfg, r.repository, r.registry, r.cipher, checkService, &r.observedServices)
	if err != nil {
		return err
	}
	if cfg.Execution.RecoveryMode {
		return nil
	}
	r.authorize = executionAuthorizer(r.repository, r.auth, r.cipher, checkService, observer)
	r.files, err = artifacts.New(
		cfg.Artifacts.Root,
		r.repository,
		artifacts.Limits{MaxUploadBytes: cfg.Artifacts.MaxUploadBytes, QuotaBytes: cfg.Artifacts.QuotaPerUserBytes,
			TTL: time.Duration(cfg.Artifacts.TTLSeconds) * time.Second},
	)
	if err != nil {
		return errors.New("artifact volume unavailable")
	}
	r.engine, r.executor, err = runtimeEngine(cfg, r.repository, r.registry, r.files, r.pool, r.authorize, observer)
	if err != nil {
		return err
	}
	r.queue, r.maxWorkers, err = runtimeQueue(cfg, r.pool, r.engine, r.repository, observer, logger)
	if err != nil {
		return err
	}

	return nil
}
func (r *runtimeComponents) closeStartup() {
	if r.files != nil {
		_ = r.files.Close()
	}
	if r.pool == nil {
		return
	}
	closed := make(chan struct{})
	go func() { r.pool.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
	}
}

func rememberCallerBindings(
	cfg config.Identity,
	repository *store.Store,
) func(context.Context, []modules.CallerBinding) error {
	return func(c context.Context, callers []modules.CallerBinding) error {
		for _, caller := range callers {
			if caller.Auth.Type == zitadelCallerType && caller.Auth.Issuer == cfg.Issuer &&
				caller.Auth.Audience == cfg.Audience {
				if e := repository.RememberServiceIdentity(
					c,
					caller.Auth.Issuer,
					caller.Auth.Subject,
					caller.Auth.Audience,
				); e != nil {
					return e
				}
			}
		}
		return nil
	}
}

func importModuleArtifact(
	repository *store.Store,
	files **artifacts.Service,
) func(context.Context, modules.Principal, modules.ArtifactDescriptor, modules.ArtifactSource) (json.RawMessage, error) {
	return func(c context.Context, p modules.Principal, d modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
		if *files == nil {
			return nil, errors.New("artifact storage unavailable")
		}
		r, e := repository.ExecutionRun(c, source.RunID)
		if e != nil {
			return nil, e
		}
		if p.UserID != r.OwnerID || p.Actor != r.ActorID || p.Organization != r.OrgID {
			return nil, domain.Forbidden()
		}
		referenceID, disclosure, policyErr := refreshImportPolicy(c, source, d)
		if policyErr != nil {
			return nil, policyErr
		}
		item, e := (*files).ImportToChat(
			c,
			domain.Principal{Issuer: p.Issuer, Subject: p.Subject, Actor: p.Actor, OrgID: p.Organization},
			r.OwnerID,
			r.ChatID,
			artifacts.Source{
				ServiceID: source.ServiceID,
				Module:    source.Module, Method: source.Method, ReferenceID: referenceID,
				ExternalDisclosureAllowed: disclosure,
				BaseURL:                   source.BaseURL,
				Client:                    source.Client,
				Headers:                   source.Headers,
				AllowedMIMETypes:          source.AllowedMIMETypes,
				Authorize:                 source.Authorize,
			},
			artifacts.Descriptor{
				ExternalID: d.ExternalID, FetchMethod: d.FetchMethod,
				Path:   d.DownloadPath,
				MIME:   d.MIMEType,
				Size:   d.SizeBytes,
				SHA256: d.SHA256,
			},
		)
		if e != nil {
			return nil, e
		}
		return json.Marshal(item)
	}
}

func runtimeTelemetry(
	ctx context.Context,
	cfg config.Config,
) (*slog.Logger, *telemetry.Telemetry, func(context.Context) error, error) {
	logger, observer, err := runtimeObserver(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	stop, err := observer.EnableOTel(ctx, cfg.Observability.OTel)
	return logger, observer, stop, err
}
func flushTelemetry(ctx context.Context, stop func(context.Context) error) {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = stop(flushCtx)
}

func (r *runtimeComponents) startBackground(runtimeCtx context.Context, cfg config.Config, logger *slog.Logger) error {
	var err error
	if r.queue != nil {
		if err = r.queue.Start(runtimeCtx); err != nil {
			return errors.New("queue unavailable")
		}
	}
	go r.registry.Run(runtimeCtx)
	if !cfg.Execution.RecoveryMode {
		sweeper := lifecycle.New(r.repository, time.Minute)
		sweeper.Logger = logger
		go sweeper.Run(runtimeCtx)
		go recoverExhaustedQueue(runtimeCtx, r.repository, logger)
		go reconcileArtifacts(runtimeCtx, r.files, logger)
	}

	return nil
}
