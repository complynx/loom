package domain_test

import (
	"testing"

	"github.com/complynx/loom/internal/domain"

	"github.com/stretchr/testify/require"
)

func TestReactionPolicies(t *testing.T) {
	t.Parallel()
	var absent *domain.ReactionPolicy
	require.False(t, absent.Enabled())
	require.False(t, absent.Allows("👍"))
	limited := &domain.ReactionPolicy{
		Emoji:  []string{"👍", "❤️"},
		Custom: []domain.CustomReaction{{ID: "thanks", Description: "A small thank you"}},
	}
	require.NoError(t, limited.Validate())
	for _, value := range []string{"👍", "❤️", "custom:thanks"} {
		require.True(t, limited.Allows(value), value)
	}
	for _, value := range []string{"👎", "custom:missing", "thanks"} {
		require.False(t, limited.Allows(value), value)
	}
	all := &domain.ReactionPolicy{AllEmoji: true}
	for _, value := range []string{"👍🏽", "👨‍👩‍👧‍👦", "🇳🇱", "1️⃣", "ℹ️", "❤️"} {
		require.True(t, all.Allows(value), value)
	}
	for _, value := range []string{"", "yes", "!", " 👍", "custom:thanks", "👍\n"} {
		require.False(t, all.Allows(value), value)
	}
	for _, policy := range []*domain.ReactionPolicy{
		{Emoji: []string{"👍", "👍"}}, {Emoji: []string{"custom:x"}},
		{Custom: []domain.CustomReaction{{ID: "x"}}}, {Custom: []domain.CustomReaction{{ID: "bad id", Description: "bad"}}},
	} {
		require.Error(t, policy.Validate())
	}
}
