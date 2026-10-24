package domain_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestOutputPolicyCustomEmoji(t *testing.T) {
	t.Parallel()
	policy := domain.OutputPolicy{DisallowCustomEmoji: true}
	for _, text := range []string{`<tg-emoji emoji-id="12">😀</tg-emoji>`, `<TG-EMOJI emoji-id='12'/>`, `[😀](tg://emoji?id=12)`, `![😀](tg://emoji?id=12)`, `<a href="tg://emoji?id=12">😀</a>`, `<a href='tg&#58;//emoji?id=12'>😀</a>`} {
		require.True(t, policy.InvalidText(text), text)
		require.False(t, (domain.OutputPolicy{}).InvalidText(text))
	}
	for _, text := range []string{"Здравствуйте 😀", "Custom emoji are not supported", "tg://emoji is a link scheme", `&lt;tg-emoji emoji-id="12"&gt;`, `[documentation](https://example.com/emoji)`, `<b>😀</b>`} {
		require.False(t, policy.InvalidText(text), text)
	}
}
