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
