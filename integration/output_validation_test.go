package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
)

type rejectionRestartStore struct {
	*store.Store

	interrupted bool
}

func (s *rejectionRestartStore) SaveOutputValidation(
	ctx context.Context,
	rid, owner string,
	fence int64,
	input, result json.RawMessage,
) error {
	if err := s.Store.SaveOutputValidation(ctx, rid, owner, fence, input, result); err != nil {
		return err
	}
	s.interrupted = true
	if err := s.Transition(ctx, rid, owner, fence, domain.Paused, "shutdown", ""); err != nil {
		return err
	}
	return context.Canceled
}

func validationEngine(
	t *testing.T,
	_ *fixture,
	model *modelFixture,
	repository runs.Store,
	call func(context.Context, domain.Run, domain.Principal, string, provider.Call) (json.RawMessage, *domain.Approval, error),
) *runs.Engine {
	t.Helper()
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	var modelClient provider.Client = client
	if dir := os.Getenv("LOOM_OUTPUT_CAPTURE_DIR"); dir != "" {
		modelClient = outputCaptureClient{Client: client, dir: dir}
	}
	return runs.New(
		repository,
		modelClient,
		runs.Options{
			Owner:       "qa-worker",
			ModuleCall:  call,
			DiscardCall: call,
			Authorize: func(ctx context.Context, r domain.Run) (domain.Principal, string, error) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://boundary", nil)
				req.Header.Set("Authorization", "Bearer "+r.ActorID)
				p, e := testAuth{}.Authenticate(ctx, req)
				return p, "user-token", e
			},
		},
	)
}

func outputCall(name, id string, args any) any {
	raw, _ := json.Marshal(args)
	return map[string]any{
		"id":     "provider-" + id,
		"status": "completed",
		"output": []any{map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(raw)}},
		"usage":  map[string]int{"input_tokens": 10, "output_tokens": 5},
	}
}

func TestOutputValidationFinalRestartAndDefault(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "restricted", false: "default"}[enabled], func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			input := runRequest(
				"",
				"",
				"Выполни тестовое действие fixture.write с пустыми параметрами, затем ответь по-русски",
			)
			input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: enabled}
			r := f.createRun(t, input, "output-run")
			changed := input
			changed.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: !enabled}
			code, _ := f.request(t, "alice", http.MethodPost, "/v1/runs", "output-run", changed)
			require.Equal(t, http.StatusConflict, code)
			var effects atomic.Int32
			backend := func(context.Context, domain.Run, domain.Principal, string, provider.Call) (json.RawMessage, *domain.Approval, error) {
				effects.Add(1)
				return json.RawMessage(`{"verified":"done"}`), nil, nil
			}
			model, engine := fakeModel(t, f, outputFinalResponder(enabled))
			if enabled {
				fault := &rejectionRestartStore{Store: f.repository}
				_ = validationEngine(t, f, model, fault, backend).Execute(t.Context(), r.ID)
				require.True(t, fault.interrupted)
				require.Equal(t, domain.Paused, f.run(t, r.ID).State)
				eventCode, raw := f.request(t, "alice", http.MethodGet, "/v1/runs/"+r.ID+"/events", "", nil)
				require.Equal(t, http.StatusOK, eventCode)
				require.NotContains(t, string(raw), "tg-emoji")
				code, raw = f.request(
					t,
					"alice",
					http.MethodPost,
					"/v1/runs/"+r.ID+"/resume",
					"resume",
					map[string]any{},
				)
				require.Less(t, code, 300, string(raw))
				engine = validationEngine(t, f, model, store.New(f.pool, nil), backend)
			}
			require.NoError(t, engine.Execute(t.Context(), r.ID))
			done := f.run(t, r.ID)
			require.Equal(t, domain.Completed, done.State)
			if enabled {
				require.Equal(t, "Готово 😀", done.Result)
			} else {
				require.Contains(t, done.Result, "tg-emoji")
			}
			model.mu.Lock()
			defer model.mu.Unlock()
			if enabled {
				require.Len(t, model.requests, 3)
				require.EqualValues(t, 1, effects.Load())
				require.Contains(t, string(model.requests[2]), "output_validation_failed")
				require.Contains(t, string(model.requests[2]), "Output policy:")
				captureWireProof(t, model.requests)
			} else {
				require.Len(t, model.requests, 1)
			}
			var count int
			require.NoError(
				t,
				f.pool.QueryRow(t.Context(), "SELECT count(*) FROM credit_ledger l JOIN steps s ON s.id=l.step_id WHERE s.kind='model' AND l.kind='settle'").
					Scan(&count),
			)
			require.Equal(t, len(model.requests), count)
		})
	}
}

func TestOutputValidationQuestionAndApproval(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"question", "approval"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			input := runRequest("", "", "Перед изменением тестовой суммы на 5 запроси подтверждение по-русски")
			input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
			r := f.createRun(t, input, "present")
			var calls atomic.Int32
			model, _ := fakeModel(t, f, func(i int) any {
				if mode == "question" {
					label := "Да 😀"
					if i == 1 {
						label = `[😀](tg://emoji?id=12)`
					}
					return outputCall(
						"question",
						"q"+string(rune('0'+i)),
						map[string]any{
							"text":       "Изменить тестовую сумму на 5?",
							"choices":    []any{map[string]string{"id": "yes", "label": label}},
							"allow_text": true,
						},
					)
				}
				if i == 1 {
					return outputCall(
						"module_call",
						"prepare",
						map[string]any{
							"module":            "fixture",
							"method":            "write",
							"arguments":         map[string]any{},
							"confirmation_text": "Подтверждаю",
						},
					)
				}
				return outputCall(
					"approval_present",
					"display",
					map[string]string{"approval_id": "original-approval", "text": "Подтвердите исходное действие 😀"},
				)
			})
			expiry := time.Now().Add(time.Hour)
			backend := func(_ context.Context, _ domain.Run, _ domain.Principal, _ string, _ provider.Call) (json.RawMessage, *domain.Approval, error) {
				calls.Add(1)
				return nil, &domain.Approval{
					ID:                 "original-approval",
					OperationID:        "original-operation",
					BackendOperationID: "original-backend",
					ArgumentHash:       "original-hash",
					Arguments:          json.RawMessage(`{"amount":5}`),
					Text:               `Изменить тестовую сумму на 5 <tg-emoji emoji-id="12">😀</tg-emoji>`,
					ConfirmationText:   "Подтверждаю",
					ExpiresAt:          expiry,
				}, nil
			}
			require.NoError(t, validationEngine(t, f, model, f.repository, backend).Execute(t.Context(), r.ID))
			done := f.run(t, r.ID)
			if mode == "question" {
				require.Equal(t, domain.WaitingInput, done.State)
				require.Equal(t, "Да 😀", done.Question.Choices[0].Label)
				require.Zero(t, calls.Load())
			} else {
				require.Equal(t, domain.WaitingApproval, done.State)
				require.Equal(t, "original-operation", done.Approval.OperationID)
				require.Equal(t, "original-backend", done.Approval.BackendOperationID)
				require.Equal(t, "original-hash", done.Approval.ArgumentHash)
				require.WithinDuration(t, expiry, done.Approval.ExpiresAt, time.Microsecond)
				require.EqualValues(t, 1, calls.Load())
				require.Empty(t, done.Approval.Decision)
			}
			code, raw := f.request(t, "alice", http.MethodGet, "/v1/runs/"+r.ID+"/events", "", nil)
			require.Equal(t, http.StatusOK, code)
			require.NotContains(t, string(raw), "tg-emoji")
			require.NotContains(t, string(raw), "tg://emoji")
			model.mu.Lock()
			require.Len(t, model.requests, 2)
			require.Contains(t, string(model.requests[1]), "output_validation_failed")
			model.mu.Unlock()
		})
	}
}

func TestOutputValidationCapabilities(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, raw := f.request(t, "alice", http.MethodGet, "/v1/capabilities", "", nil)
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, `{"output_validation":{"version":1,"disallow_custom_emoji":true}}`, string(raw))
}

func captureWireProof(t *testing.T, requests []json.RawMessage) {
	t.Helper()
	if dir := os.Getenv("LOOM_OUTPUT_CAPTURE_DIR"); dir != "" {
		for i, raw := range requests {
			require.NoError(
				t,
				os.WriteFile(
					filepath.Join(
						dir,
						[]string{"initial-wire.json", "after-write-wire.json", "correction-wire.json"}[i],
					),
					raw,
					0600,
				),
			)
		}
	}
}
func TestOutputValidationLimitKeepsRejectedTextPrivate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE settings SET doc=jsonb_set(doc,'{max_steps}','1')`)
	require.NoError(t, err)
	input := runRequest("", "", "Reply")
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "limit")
	model, engine := fakeModel(t, f, func(int) any { return finalResponse(`<tg-emoji emoji-id="12">😀</tg-emoji>`) })
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	done := f.run(t, r.ID)
	require.Equal(t, domain.Paused, done.State)
	require.Equal(t, "step_limit", done.Reason)
	require.Empty(t, done.Result)
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	model.mu.Unlock()
	code, raw := f.request(t, "alice", http.MethodGet, "/v1/runs/"+r.ID+"/events", "", nil)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, string(raw), "tg-emoji")
}

func TestOutputValidationPersistenceGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	input := runRequest("", "", "Reply")
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "guards")
	run, err := f.repository.ClaimRun(t.Context(), r.ID, "guard-worker", time.Minute)
	require.NoError(t, err)
	invalid := `<tg-emoji emoji-id="12">😀</tg-emoji>`
	for _, reject := range []func() error{
		func() error {
			return f.repository.Transition(t.Context(), run.ID, run.LeaseOwner, run.Fence, domain.Completed, "", invalid)
		},
		func() error {
			return f.repository.AskQuestion(t.Context(), run.ID, run.LeaseOwner, run.Fence, domain.Question{Text: "Question?", Choices: []domain.Choice{{ID: "yes", Label: invalid}}})
		},
		func() error {
			return f.repository.AskApproval(t.Context(), run.ID, run.LeaseOwner, run.Fence, domain.Approval{Text: invalid})
		},
	} {
		var failure *domain.Error
		require.ErrorAs(t, reject(), &failure)
		require.Equal(t, "output_validation_failed", failure.Code)
	}
	require.NoError(
		t,
		f.repository.Transition(t.Context(), run.ID, run.LeaseOwner, run.Fence, domain.Completed, "", "Ready 😀"),
	)
}

func outputFinalResponder(enabled bool) func(int) any {
	return func(i int) any {
		if enabled && i == 1 {
			return outputCall(
				"module_call",
				"successful-write",
				map[string]any{
					"module":            "fixture",
					"method":            "write",
					"arguments":         map[string]any{},
					"confirmation_text": "",
				},
			)
		}
		if !enabled || i == 2 {
			return finalResponse(`<tg-emoji emoji-id="12">😀</tg-emoji>`)
		}
		return finalResponse("Готово 😀")
	}
}

type outputCaptureClient struct {
	provider.Client

	dir string
}

func (c outputCaptureClient) Respond(ctx context.Context, request provider.Request) (provider.Response, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return provider.Response{}, err
	}
	if err = os.WriteFile(filepath.Join(c.dir, "actual-"+request.ClientRequestID+".json"), raw, 0600); err != nil {
		return provider.Response{}, err
	}
	return c.Client.Respond(ctx, request)
}
func TestOutputValidationCorrectedApprovalDoesNotReturn(t *testing.T) {
	t.Parallel()
	f := conversationFixture(t)
	f.repository.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error { return nil })
	input := runRequest("", "", "Ask before each of two test changes")
	input.Interaction.Approvals = true
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "two-approvals")
	model, _ := fakeModel(t, f, func(i int) any {
		switch i {
		case 1:
			return outputCall("module_call", "first", map[string]any{})
		case 2:
			return outputCall(
				"approval_present",
				"correct",
				map[string]string{"approval_id": "approval-first", "text": "Confirm first test change"},
			)
		case 3:
			return outputCall("module_call", "second", map[string]any{})
		default:
			return finalResponse("Both requests were rejected")
		}
	})
	var preparations atomic.Int32
	backend := func(_ context.Context, run domain.Run, _ domain.Principal, _ string, call provider.Call) (json.RawMessage, *domain.Approval, error) {
		if run.Approval != nil && run.Approval.Call.ID == call.ID && run.Approval.Decision != "" {
			return json.RawMessage(`{"status":"rejected"}`), nil, nil
		}
		preparations.Add(1)
		text := "Confirm second test change"
		if call.ID == "first" {
			text = `Confirm first test change <tg-emoji emoji-id="12">😀</tg-emoji>`
		}
		return nil, &domain.Approval{
			ID:          "approval-" + call.ID,
			OperationID: "operation-" + call.ID,
			Text:        text,
			ExpiresAt:   time.Now().Add(time.Hour),
		}, nil
	}
	for _, id := range []string{"first", "second"} {
		require.NoError(t, validationEngine(t, f, model, store.New(f.pool, nil), backend).Execute(t.Context(), r.ID))
		done := f.run(t, r.ID)
		require.Equal(t, domain.WaitingApproval, done.State)
		require.Equal(t, "approval-"+id, done.Approval.ID)
		code, raw := f.request(
			t,
			"alice",
			http.MethodPost,
			"/v1/runs/"+r.ID+"/input",
			"reject-"+id,
			map[string]string{"kind": "approval", "approval_id": done.Approval.ID, "decision": "reject"},
		)
		require.Equal(t, http.StatusOK, code, string(raw))
	}
	require.NoError(t, validationEngine(t, f, model, f.repository, backend).Execute(t.Context(), r.ID))
	require.Equal(t, domain.Completed, f.run(t, r.ID).State, f.run(t, r.ID).Reason)
	require.EqualValues(t, 2, preparations.Load())
}

func TestOutputValidationImmutableConfirmationPauses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	input := runRequest("", "", "Ask for consent")
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "immutable")
	model, _ := fakeModel(t, f, func(int) any { return outputCall("module_call", "action", map[string]any{}) })
	backend := func(context.Context, domain.Run, domain.Principal, string, provider.Call) (json.RawMessage, *domain.Approval, error) {
		return nil, &domain.Approval{
			ID:               "approval",
			OperationID:      "operation",
			Text:             "Confirm?",
			ConfirmationText: `<tg-emoji emoji-id="12">😀</tg-emoji>`,
			ExpiresAt:        time.Now().Add(time.Hour),
		}, nil
	}
	require.NoError(t, validationEngine(t, f, model, f.repository, backend).Execute(t.Context(), r.ID))
	done := f.run(t, r.ID)
	require.Equal(t, domain.Paused, done.State)
	require.Equal(t, "approval_confirmation_unsupported", done.Reason)
	require.Nil(t, done.Approval)
}
