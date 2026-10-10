package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/sandbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSandboxWorkerProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "clx-app-worker" {
		return
	}
	if sandbox.WorkerMain(context.Background(), os.Stdin, os.Stdout) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// This exercises the production queue and credential path. Only external HTTP
// peers are fakes; PostgreSQL, River and the isolated subprocess protocol are real.
func TestRuntimeRunsQueuedRequestAndShutsDown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("production runtime requires Linux")
	}
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLX_TEST_DATABASE_DSN for real PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "app_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() {
		clean, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_, _ = admin.Exec(clean, "DROP SCHEMA "+schema+" CASCADE")
	}()
	dbURL, err := url.Parse(dsn)
	require.NoError(t, err)
	query := dbURL.Query()
	query.Set("search_path", schema)
	dbURL.RawQuery = query.Encode()
	require.NoError(t, Migrate(ctx, config.Config{Database: config.Database{DSN: dbURL.String()}}))
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer.URL, "jwks_uri": issuer.URL + "/keys", "introspection_endpoint": issuer.URL + "/introspect"})
		case "/introspect":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": issuer.URL, "sub": "user", "aud": "harness", "exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "org", "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{"org": "org"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	var modelCalls atomic.Int32
	var lastModel atomic.Value
	var revokeDisclosure atomic.Value
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer provider-only", r.Header.Get("Authorization"))
		modelCalls.Add(1)
		var wire struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
		lastModel.Store(wire.Model)
		if modelCalls.Load() == 1 || modelCalls.Load() == 4 {
			if modelCalls.Load() == 4 {
				revokeDisclosure.Load().(func())()
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"search","status":"completed","output":[{"type":"function_call","id":"tool-search","call_id":"search-call","name":"script","arguments":"{\"code\":\"return await modules.history.search({mode: \\\"fulltext\\\", query: \\\"Help\\\", limit: 1})\"}"}],"usage":{"input_tokens":20,"output_tokens":5}}`)
			return
		}
		if modelCalls.Load() == 2 {
			require.Contains(t, string(wire.Input), "message.user")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Ready to help"}]}],"usage":{"input_tokens":20,"output_tokens":5}}`)
	}))
	defer model.Close()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0600))
	socket := filepath.Join(dir, "sandbox.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	sandboxCtx, stopSandbox := context.WithCancel(context.Background())
	defer stopSandbox()
	supervisorDone := make(chan error, 1)
	go func() {
		supervisorDone <- (sandbox.Supervisor{WorkerPath: os.Args[0], WorkerArgs: []string{"-test.run=TestSandboxWorkerProcess", "--", "clx-app-worker"}, MaxWorkers: 2}).Serve(sandboxCtx, listener)
	}()
	defer func() { _ = listener.Close() }()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())
	c := config.Config{HTTP: config.HTTP{Listen: address, MaxRequestBytes: 1 << 20, MaxWaitMS: 1000, DisconnectGraceMS: 100}, Database: config.Database{DSN: dbURL.String(), MaxConnections: 20, StatementTimeoutMS: 2000, LockTimeoutMS: 500}, Identity: config.Identity{Issuer: issuer.URL, Audience: "harness", AdminOrganizationID: "org", ClientID: "introspect", ClientSecret: "test-only", EncryptionKeyFile: keyPath, EncryptionKeyID: "key", JWKSCacheTTLSeconds: 300, RolePermissions: map[string][]string{"member": {"chat.read", "runs.create", "runs.control", "settings.read", "settings.write", "preferences.self.write"}}}, Providers: map[string]config.Provider{"fake": {Endpoint: model.URL, APIKey: "provider-only"}}, Artifacts: config.Artifacts{Root: filepath.Join(dir, "artifacts"), MaxUploadBytes: 1 << 20, QuotaPerUserBytes: 10 << 20}, Sandbox: config.Sandbox{Socket: socket, EngineCandidate: "goja", MaxWorkers: 2, MemoryCeilingBytes: 134217728, PIDsCeiling: 32, ParallelRPCCeiling: 8, ScriptTimeoutCeilingMS: 1000}, Execution: config.Execution{LeaseSeconds: 15, HeartbeatSeconds: 3, ShutdownTargetMS: 1000, ShutdownDeadlineMS: 5000, ActiveRunsTotalCeiling: 2, ActiveRunsPerUserCeiling: 2}, BootstrapSettings: config.Settings{ExternalDisclosureDefault: true, ModelCatalog: []config.Model{{ID: "test", Provider: "fake", Model: "test-model", ParallelTools: true, Steering: true, Questions: true, Efforts: []string{"low"}, MaxEffort: "low", ContextTokens: 8192, MaxOutputTokens: 100}}, DefaultModelID: "test", DefaultEffort: "low", Tariffs: []config.Tariff{{ModelID: "test", InputCreditsPerMillion: 1000, OutputCreditsPerMillion: 1000}}, InitialCredits: 1000000, MaxRunMicrocredits: 100000, UserActiveRunLimit: 2, ScriptTimeoutMS: 1000, ParallelRPCLimit: 4, RunActiveTimeSeconds: 60, InputWaitSeconds: 60, ContextInputTokenBudget: 32768, HistoryRetention: config.Retention{Mode: "forever"}, ArtifactRetention: config.Retention{Mode: "forever"}, TraceRetention: config.Retention{Mode: "forever"}}}
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	appDone := make(chan error, 1)
	go func() { appDone <- Serve(appCtx, c) }()
	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + address
	var startupError error
	require.Eventually(t, func() bool {
		select {
		case startupError = <-appDone:
			return true
		default:
		}
		response, e := client.Get(base + "/readyz")
		if e != nil {
			return false
		}
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == 200
	}, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, startupError)
	request, err := http.NewRequest(http.MethodPost, base+"/v1/runs", strings.NewReader(`{"input":{"text":"Help me"},"budget":{"max_microcredits":100000},"interaction":{"questions":"text","approvals":true}}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer user-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "runtime-happy")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, 202, response.StatusCode, string(body))
	var created domain.Run
	require.NoError(t, json.Unmarshal(body, &created))
	require.NotEmpty(t, created.ID, string(body))
	var final domain.Run
	require.Eventually(t, func() bool {
		request, e := http.NewRequest(http.MethodGet, base+"/v1/runs/"+created.ID, nil)
		if e != nil {
			return false
		}
		request.Header.Set("Authorization", "Bearer user-token")
		res, e := client.Do(request)
		if e != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		if json.NewDecoder(res.Body).Decode(&final) != nil {
			return false
		}
		return final.State == domain.Completed
	}, 10*time.Second, 100*time.Millisecond, fmt.Sprintf("last run: %+v", final))
	require.Equal(t, "Ready to help", final.Result)
	require.EqualValues(t, 2, modelCalls.Load())
	require.Equal(t, "test-model", lastModel.Load())
	// The admin catalog changes while the process and provider credentials stay live.
	requestJSON := func(method, path, key string, body any) (int, []byte) {
		var input io.Reader
		if body != nil {
			encoded, e := json.Marshal(body)
			require.NoError(t, e)
			input = strings.NewReader(string(encoded))
		}
		req, e := http.NewRequest(method, base+path, input)
		require.NoError(t, e)
		req.Header.Set("Authorization", "Bearer user-token")
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := client.Do(req)
		require.NoError(t, e)
		defer func() { _ = res.Body.Close() }()
		raw, e := io.ReadAll(res.Body)
		require.NoError(t, e)
		return res.StatusCode, raw
	}
	code, raw := requestJSON("GET", "/v1/admin/settings", "", nil)
	require.Equal(t, 200, code, string(raw))
	var current domain.Settings
	require.NoError(t, json.Unmarshal(raw, &current))
	newModel := current.Models[0]
	newModel.ID, newModel.ProviderModel = "added", "added-provider-model"
	newModel.Provider = "unconfigured"
	current.Models = append(current.Models, newModel)
	code, raw = requestJSON("PATCH", "/v1/admin/settings", "", current)
	require.Equal(t, 422, code, string(raw))
	current.Models[len(current.Models)-1].Provider = "fake"
	code, raw = requestJSON("PATCH", "/v1/admin/settings", "", current)
	require.Equal(t, 200, code, string(raw))
	code, raw = requestJSON("GET", "/v1/users/me/preferences", "", nil)
	require.Equal(t, 200, code, string(raw))
	var prefs domain.Preferences
	require.NoError(t, json.Unmarshal(raw, &prefs))
	prefs.ModelID = "added"
	code, raw = requestJSON("PATCH", "/v1/users/me/preferences", "", prefs)
	require.Equal(t, 200, code, string(raw))
	code, raw = requestJSON("POST", "/v1/runs", "runtime-dynamic-model", map[string]any{"input": map[string]string{"text": "Use the new model"}, "budget": map[string]int{"max_microcredits": 100000}})
	require.Equal(t, 202, code, string(raw))
	var dynamicRun domain.Run
	require.NoError(t, json.Unmarshal(raw, &dynamicRun))
	require.Eventually(t, func() bool {
		status, result := requestJSON("GET", "/v1/runs/"+dynamicRun.ID, "", nil)
		if status != 200 {
			return false
		}
		var run domain.Run
		if json.Unmarshal(result, &run) != nil {
			return false
		}
		return run.State == domain.Completed
	}, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, "added-provider-model", lastModel.Load())
	require.EqualValues(t, 3, modelCalls.Load())
	code, raw = requestJSON("GET", "/v1/admin/settings", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, json.Unmarshal(raw, &current))
	revokeDisclosure.Store(func() {
		current.ExternalDisclosure = false
		status, result := requestJSON("PATCH", "/v1/admin/settings", "", current)
		require.Equal(t, 200, status, string(result))
	})
	code, raw = requestJSON("POST", "/v1/runs", "runtime-disclosure-revoked", map[string]any{"input": map[string]string{"text": "Search private history"}, "budget": map[string]int{"max_microcredits": 100000}})
	require.Equal(t, 202, code, string(raw))
	var blocked domain.Run
	require.NoError(t, json.Unmarshal(raw, &blocked))
	require.Eventually(t, func() bool {
		status, result := requestJSON("GET", "/v1/runs/"+blocked.ID, "", nil)
		if status != 200 {
			return false
		}
		if json.Unmarshal(result, &blocked) != nil {
			return false
		}
		return blocked.State == domain.Paused
	}, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, "disclosure_denied", blocked.Reason)
	require.EqualValues(t, 4, modelCalls.Load())
	// A PostgreSQL dependency failure exhausts a real River job. Recovery is
	// bounded and leaves a paused run for the caller to resume explicitly.
	code, raw = requestJSON("POST", "/v1/runs/"+blocked.ID+"/cancel", "runtime-revoked-cancel", nil)
	require.Equal(t, 200, code, string(raw))
	code, raw = requestJSON("GET", "/v1/admin/settings", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, json.Unmarshal(raw, &current))
	current.ExternalDisclosure = true
	code, raw = requestJSON("PATCH", "/v1/admin/settings", "", current)
	require.Equal(t, 200, code, string(raw))
	faultDB, e := pgxpool.New(ctx, dbURL.String())
	require.NoError(t, e)
	defer faultDB.Close()
	_, e = faultDB.Exec(ctx, `CREATE FUNCTION queue_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test dependency unavailable'; END $$;
CREATE TRIGGER queue_test_failure BEFORE UPDATE ON runs FOR EACH ROW WHEN (NEW.state = 'running' AND OLD.state = 'queued') EXECUTE FUNCTION queue_test_failure();
CREATE FUNCTION queue_test_attempts() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.max_attempts := 1; RETURN NEW; END $$;
CREATE TRIGGER queue_test_attempts BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION queue_test_attempts();`)
	require.NoError(t, e)
	code, raw = requestJSON("POST", "/v1/runs", "runtime-queue-exhaustion", map[string]any{"input": map[string]string{"text": "Recover after dependency outage"}, "budget": map[string]int{"max_microcredits": 100000}})
	require.Equal(t, 202, code, string(raw))
	var exhausted domain.Run
	require.NoError(t, json.Unmarshal(raw, &exhausted))
	require.Eventually(t, func() bool {
		var discarded bool
		queryErr := faultDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='clx_execute_run' AND args->>'run_id'=$1 AND state='discarded')`, exhausted.ID).Scan(&discarded)
		return queryErr == nil && discarded
	}, 10*time.Second, 100*time.Millisecond)
	_, e = faultDB.Exec(ctx, `DROP TRIGGER queue_test_failure ON runs; DROP TRIGGER queue_test_attempts ON river_job; DROP FUNCTION queue_test_failure(); DROP FUNCTION queue_test_attempts();`)
	require.NoError(t, e)
	stopApp()
	select {
	case e = <-appDone:
		require.NoError(t, e)
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime did not stop")
	}
	appCtx, stopApp = context.WithCancel(context.Background())
	defer stopApp()
	appDone = make(chan error, 1)
	go func() { appDone <- Serve(appCtx, c) }()
	require.Eventually(t, func() bool {
		res, requestErr := client.Get(base + "/readyz")
		if requestErr != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode == 200
	}, 10*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		status, result := requestJSON("GET", "/v1/runs/"+exhausted.ID, "", nil)
		if status != 200 {
			return false
		}
		if json.Unmarshal(result, &exhausted) != nil {
			return false
		}
		return exhausted.State == domain.Paused
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, "queue_attempts_exhausted", exhausted.Reason)
	require.EqualValues(t, 4, modelCalls.Load())
	code, raw = requestJSON("POST", "/v1/runs/"+exhausted.ID+"/resume", "runtime-exhausted-resume", nil)
	require.Equal(t, 200, code, string(raw))
	require.Eventually(t, func() bool {
		status, result := requestJSON("GET", "/v1/runs/"+exhausted.ID, "", nil)
		if status != 200 {
			return false
		}
		if json.Unmarshal(result, &exhausted) != nil {
			return false
		}
		return exhausted.State == domain.Completed
	}, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, "Ready to help", exhausted.Result)
	require.EqualValues(t, 5, modelCalls.Load())
	started := time.Now()
	stopApp()
	select {
	case err = <-appDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown exceeded five seconds")
	}
	require.Less(t, time.Since(started), 5*time.Second)
	stopSandbox()
	require.NoError(t, listener.Close())
	select {
	case <-supervisorDone:
	case <-time.After(time.Second):
		t.Fatal("sandbox supervisor did not stop")
	}
}
