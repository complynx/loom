package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
)

const activeCompactionPayload = "large-tool-result-"

type activeCompactionClient struct {
	t               *testing.T
	native          bool
	requests        []provider.Request
	compactRequests []provider.Request
	initialCalls    []provider.Call
	beforeCompact   func(provider.Request)
}

func (c *activeCompactionClient) SupportsCompaction(provider.Request) bool { return c.native }

func (c *activeCompactionClient) CountInputTokens(_ context.Context, request provider.Request) (int64, error) {
	raw, err := json.Marshal(request.Input)
	if err != nil {
		return 0, err
	}
	if strings.Contains(string(raw), activeCompactionPayload) {
		return 7000, nil
	}
	return 100, nil
}

func (c *activeCompactionClient) Respond(_ context.Context, request provider.Request) (provider.Response, error) {
	if strings.HasPrefix(request.Instructions, "Compact conversation") {
		c.captureCompaction(request)
		return provider.Response{
			ID:    "text-compact",
			Text:  "Tool found order A-12. Keep user constraint: no booking before approval.",
			Usage: provider.Usage{InputTokens: 7000, OutputTokens: 30},
		}, nil
	}
	c.requests = append(c.requests, request)
	switch len(c.requests) {
	case 1:
		return activeCallResponse(c.t, c.initialCalls), nil
	case 2:
		return activeCallResponse(
			c.t,
			[]provider.Call{
				{
					ID:   "choose-time",
					Name: "question",
					Arguments: json.RawMessage(
						`{"text":"Choose a time","choices":[{"id":"evening","label":"Evening"}],"allow_text":false}`,
					),
				},
			},
		), nil
	default:
		return provider.Response{
			ID:   "after-resume",
			Text: "Order A-12 ready for evening; approval still required.",
			Output: []provider.Item{
				provider.Message("assistant", "Order A-12 ready for evening; approval still required."),
			},
			Usage: provider.Usage{InputTokens: 100, OutputTokens: 10},
		}, nil
	}
}

func (c *activeCompactionClient) Compact(_ context.Context, request provider.Request) (provider.Response, error) {
	c.captureCompaction(request)
	return provider.Response{
		ID: "native-compact",
		Output: []provider.Item{
			provider.Message("user", "Find order A-12; no booking before approval."),
			json.RawMessage(`{"type":"compaction","id":"cmp-order","encrypted_content":"opaque-order-state"}`),
		},
		Usage: provider.Usage{InputTokens: 7000, OutputTokens: 30},
	}, nil
}

func (c *activeCompactionClient) captureCompaction(request provider.Request) {
	c.compactRequests = append(c.compactRequests, request)
	if c.beforeCompact != nil {
		c.beforeCompact(request)
	}
}

func activeCallResponse(t *testing.T, calls []provider.Call) provider.Response {
	t.Helper()
	response := provider.Response{
		ID:    "tool-turn",
		Calls: calls,
		Usage: provider.Usage{InputTokens: 100, OutputTokens: 10},
	}
	for _, call := range calls {
		raw, err := json.Marshal(
			map[string]string{
				"type":      "function_call",
				"call_id":   call.ID,
				"name":      call.Name,
				"arguments": string(call.Arguments),
			},
		)
		require.NoError(t, err)
		response.Output = append(response.Output, raw)
	}
	return response
}

func configureActiveCompaction(t *testing.T, f *fixture, native bool) {
	t.Helper()
	settings, err := f.repository.GetSettings(context.Background())
	require.NoError(t, err)
	settings.MaxRunMicrocredits = 100000
	settings.MaxSteps = 20
	model := &settings.Models[0]
	model.InputTokenLimit = 20000
	model.ContextTokens = 20000
	model.CompactionThresholdTokens = 6000
	model.CompactionTargetTokens = 2000
	if native {
		model.Provider = "openai"
	}
	_, err = f.repository.UpdateSettings(
		context.Background(),
		domain.Principal{Actor: "qa-admin", Permissions: map[string]bool{"settings.write": true}},
		settings.Version,
		settings,
	)
	require.NoError(t, err)
}

func activeCompactionEngine(
	f *fixture,
	client provider.Client,
	owner string,
	script func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error),
) *runs.Engine {
	return runs.New(
		f.repository,
		client,
		runs.Options{
			Owner:  owner,
			Script: script,
			Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
				return domain.Principal{
					Subject:     "alice",
					Actor:       "alice",
					OrgID:       "test-org",
					Permissions: map[string]bool{"chat.read": true, "runs.create": true},
				}, "token", nil
			},
		},
	)
}

func TestActiveCompactionSurvivesQuestionResumeWithoutRepeatingPaidWork(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native-%t", native), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			configureActiveCompaction(t, f, native)
			client := &activeCompactionClient{
				t:      t,
				native: native,
				initialCalls: []provider.Call{
					{ID: "read-order", Name: "script", Arguments: json.RawMessage(`{"code":"return readOrder()"}`)},
				},
			}
			scriptCalls := 0
			script := func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
				scriptCalls++
				return json.Marshal(strings.Repeat(activeCompactionPayload, 500))
			}
			input := runRequest("", "", "Find order A-12; no booking before approval.")
			input.Budget.MaxMicrocredits = 100000
			run := f.createRun(t, input, "active-compact")
			require.NoError(
				t,
				activeCompactionEngine(f, client, "compact-worker", script).Execute(context.Background(), run.ID),
			)
			waiting := f.run(t, run.ID)
			require.Equal(t, domain.WaitingInput, waiting.State, waiting.Reason)
			require.Len(t, client.compactRequests, 1)
			require.Equal(t, 1, scriptCalls)
			rawContext, err := json.Marshal(client.requests[1].Input)
			require.NoError(t, err)
			require.NotContains(t, string(rawContext), activeCompactionPayload)
			if native {
				require.Contains(t, string(rawContext), "opaque-order-state")
			} else {
				require.Contains(t, string(rawContext), "no booking before approval")
			}
			code, raw := f.request(
				t,
				"alice",
				"POST",
				"/v1/runs/"+run.ID+"/input",
				"compact-answer",
				domain.RunInput{Kind: "answer", QuestionID: waiting.Question.ID, ChoiceIDs: []string{"evening"}},
			)
			require.Equal(t, 200, code, string(raw))
			require.NoError(
				t,
				activeCompactionEngine(
					f,
					client,
					"restarted-compact-worker",
					script,
				).Execute(context.Background(), run.ID),
			)
			completed := f.run(t, run.ID)
			require.Equal(t, domain.Completed, completed.State)
			require.Equal(t, 1, scriptCalls)
			require.Len(t, client.compactRequests, 1)
			require.Len(t, client.requests, 3)
			rawContext, err = json.Marshal(client.requests[2].Input)
			require.NoError(t, err)
			require.Contains(t, string(rawContext), "evening")
			require.NotContains(t, string(rawContext), activeCompactionPayload)
			require.Zero(t, completed.ReservedMicrocredits)
			steps, err := f.repository.ExecutionSteps(context.Background(), run.ID)
			require.NoError(t, err)
			var charged int64
			compactSteps := 0
			for _, step := range steps {
				charged += step.Usage.Microcredits
				if step.Kind == "compact" {
					compactSteps++
				}
				if step.Kind == "tool" && strings.Contains(string(step.Input), "read-order") {
					require.Contains(t, string(step.Result), activeCompactionPayload)
				}
			}
			require.Equal(t, 1, compactSteps)
			require.Equal(t, charged, completed.SpentMicrocredits)
			code, raw = f.request(t, "alice", "GET", "/v1/chats/"+run.ChatID+"/history", "", nil)
			require.Equal(t, 200, code, string(raw))
			require.Contains(t, string(raw), "no booking before approval")
			require.Contains(t, string(raw), completed.Result)
		})
	}
}

func TestActiveCompactionWaitsForAllToolResults(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	configureActiveCompaction(t, f, true)
	client := &activeCompactionClient{t: t, native: true, initialCalls: []provider.Call{
		{ID: "read-first", Name: "script", Arguments: json.RawMessage(`{"code":"return first()"}`)},
		{ID: "read-second", Name: "script", Arguments: json.RawMessage(`{"code":"return second()"}`)},
	}}
	scriptCalls := 0
	script := func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
		scriptCalls++
		require.Empty(t, client.compactRequests, "unfinished tool group must retain its original context")
		return json.Marshal(strings.Repeat(activeCompactionPayload, 500))
	}
	input := runRequest("", "", "Compare orders")
	input.Budget.MaxMicrocredits = 100000
	run := f.createRun(t, input, "paired-compact")
	client.beforeCompact = func(request provider.Request) {
		require.Equal(t, 2, scriptCalls)
		steps, err := f.repository.ExecutionSteps(context.Background(), run.ID)
		require.NoError(t, err)
		tools := 0
		for _, step := range steps {
			if step.Kind == "tool" {
				tools++
				require.Equal(t, "completed", step.State)
			}
		}
		require.Equal(t, 2, tools)
		var calls, results []string
		for _, raw := range request.Input {
			var item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			}
			require.NoError(t, json.Unmarshal(raw, &item))
			switch item.Type {
			case "function_call":
				calls = append(calls, item.CallID)
			case "function_call_output":
				results = append(results, item.CallID)
			}
		}
		require.ElementsMatch(t, []string{"read-first", "read-second"}, calls)
		require.ElementsMatch(t, calls, results)
	}
	require.NoError(t, activeCompactionEngine(f, client, "paired-worker", script).Execute(context.Background(), run.ID))
	require.Equal(t, domain.WaitingInput, f.run(t, run.ID).State, f.run(t, run.ID).Reason)
	require.Len(t, client.compactRequests, 1)
}
