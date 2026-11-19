package runs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func TestInvalidToolInputExplainsHowToCorrectIt(t *testing.T) {
	t.Parallel()
	choices := strings.TrimSuffix(strings.Repeat(`{"id":"x","label":"Choice"},`, maxQuestionChoices+1), ",")
	for _, tc := range []struct {
		name string
		args string
		code string
		hint string
	}{
		{toolScript, `{"code":"` + strings.Repeat("x", maxScriptBytes+1) + `"}`, "invalid_script", "65536"},
		{toolQuestion, `{"text":"Choose","choices":[` + choices + `],"allow_text":false}`, "invalid_question", "20 choices"},
		{
			toolQuestion,
			`{"text":"Choose","choices":[{"id":"x","label":"A"},{"id":"x","label":"B"}],"allow_text":false}`,
			"invalid_choices",
			"unique nonempty id",
		},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			e := New(nil, nil, Options{})
			call := provider.Call{Name: tc.name, Arguments: json.RawMessage(tc.args)}
			raw, waiting, err := e.executeCall(
				t.Context(), domain.Run{}, domain.Principal{}, "", call, domain.Settings{},
			)
			require.NoError(t, err)
			require.False(t, waiting)
			var failure domain.ToolError
			require.NoError(t, json.Unmarshal(raw, &failure))
			require.Equal(t, tc.code, failure.Code)
			require.Contains(t, failure.Recovery, tc.hint)
		})
	}
}
