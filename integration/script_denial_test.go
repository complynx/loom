package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
)

func TestScriptAdmissionDenialResumesWithoutReplay(t *testing.T) {
	t.Parallel()
	for _, state := range []domain.RunState{domain.Paused, domain.WaitingAuth} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			model, _ := fakeModel(t, f, func(index int) any {
				if index == 1 {
					return map[string]any{
						"id":     "script-call",
						"status": "completed",
						"output": []any{
							map[string]any{
								"type":      "function_call",
								"call_id":   "denied-script",
								"name":      "script",
								"arguments": `{"code":"return 1"}`,
							},
						},
						"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
					}
				}
				return finalResponse("Admission denied; no action performed")
			})
			client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
			require.NoError(t, err)
			opts := recoveryOptions("denied-script-worker")
			calls := 0
			opts.Script = func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
				calls++
				return nil, &runs.ScriptNotStartedError{
					Cause: &runs.Suspension{State: state, Reason: "disclosure_denied"},
				}
			}
			engine := runs.New(f.repository, client, opts)
			run := f.createRun(t, runRequest("", "", "Perform script"), "denied-script")
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			stopped := f.run(t, run.ID)
			require.Equal(t, state, stopped.State)
			require.Equal(t, "disclosure_denied", stopped.Reason)
			require.EqualValues(t, 15, stopped.SpentMicrocredits)
			require.Zero(t, stopped.ReservedMicrocredits)
			steps, err := f.repository.ExecutionSteps(t.Context(), run.ID)
			require.NoError(t, err)
			require.Len(t, steps, 2)
			require.Equal(t, "tool", steps[1].Kind)
			require.Equal(t, "completed", steps[1].State)
			require.Contains(t, string(steps[1].Result), "script_not_started")
			require.Zero(t, steps[1].Usage.Microcredits)
			if state == domain.WaitingAuth {
				return // Session reauthorization is separate; verify the script is durably settled here.
			}
			code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-script", nil)
			require.Equal(t, 200, code, string(raw))
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			require.Equal(t, domain.Completed, f.run(t, run.ID).State)
			require.Equal(t, 1, calls, "rejected script must not replay on resume")
			require.EqualValues(t, 30, f.run(t, run.ID).SpentMicrocredits)
			model.mu.Lock()
			require.Len(t, model.requests, 2)
			require.Contains(t, string(model.requests[1]), "script_not_started")
			model.mu.Unlock()
		})
	}
}

func TestUnprovenScriptSuspensionRemainsUncertain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	model, _ := fakeModel(t, f, func(int) any {
		return map[string]any{
			"id":     "script-call",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"call_id":   "unknown-script",
					"name":      "script",
					"arguments": `{"code":"return 1"}`,
				},
			},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
	})
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	opts := recoveryOptions("unknown-script-worker")
	opts.Script = func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
		return nil, &runs.Suspension{State: domain.Paused, Reason: "permission_revoked"}
	}
	run := f.createRun(t, runRequest("", "", "Perform script"), "unknown-script")
	require.NoError(t, runs.New(f.repository, client, opts).Execute(t.Context(), run.ID))
	var pending int
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), "SELECT count(*) FROM steps WHERE run_id=$1 AND kind='tool' AND state='pending'", run.ID).
			Scan(&pending),
	)
	require.Equal(t, 1, pending)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-unknown-script", nil)
	require.Equal(t, 409, code, string(raw))
}
