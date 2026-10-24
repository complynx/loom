package runs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func TestReactionToolNegotiationAndDeniedExecution(t *testing.T) {
	t.Parallel()
	for _, policy := range []*domain.ReactionPolicy{nil, {}, {AllEmoji: true}, {Emoji: []string{"👍"}}, {Custom: []domain.CustomReaction{{ID: "thanks", Description: "Thank you"}}}} {
		tools := standardTools(domain.Interaction{Reactions: policy})
		found := false
		for _, tool := range tools {
			if tool.Name == "message_react" {
				found = true
				encoded, err := json.Marshal(policy)
				require.NoError(t, err)
				require.Contains(t, tool.Description, string(encoded))
			}
		}
		require.Equal(t, policy.Enabled(), found)
	}
	engine := &Engine{}
	result, waiting, err := engine.executeReaction(t.Context(), domain.Run{}, domain.Principal{}, provider.Call{
		Name: "message_react", Arguments: json.RawMessage(`{"message_seq":1,"reaction":"👍","active":true}`),
	})
	require.NoError(t, err)
	require.False(t, waiting)
	require.JSONEq(t, `{"error":"reaction_not_supported"}`, string(result))
}

func TestReactionAvailabilityMatchesExecutionCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		policy    *domain.ReactionPolicy
		profile   domain.ExecutionProfile
		available bool
	}{
		{name: "absent"},
		{name: "disabled", policy: &domain.ReactionPolicy{}},
		{name: "emoji", policy: &domain.ReactionPolicy{Emoji: []string{"👍"}}, available: true},
		{name: "all", policy: &domain.ReactionPolicy{AllEmoji: true}, available: true},
		{name: "custom", policy: &domain.ReactionPolicy{Custom: []domain.CustomReaction{{ID: "thanks", Description: "Thank you"}}}, available: true},
		{name: "restricted", policy: &domain.ReactionPolicy{AllEmoji: true}, profile: domain.ProfileNativeFiles},
	} {
		for _, mode := range []string{"", domain.ReplyModeAgent} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				run := domain.Run{
					ExecutionProfile: tc.profile,
					Interaction:      domain.Interaction{ReplyMode: mode, Reactions: tc.policy},
				}
				engine := New(nil, nil, Options{})
				tools, err := engine.turnTools(t.Context(), run, domain.Principal{}, "")
				require.NoError(t, err)
				found := false
				for _, tool := range tools {
					if tool.Name == "message_react" {
						found = true
					}
				}
				require.Equal(t, tc.available, found)
				instructions := conversationInstructions(run)
				if tc.available {
					require.Contains(
						t,
						instructions,
						"Quick message reactions are available; you might react before starting the task.",
					)
				} else {
					require.NotContains(t, instructions, "Quick message reactions are available")
				}
			})
		}
	}
}
