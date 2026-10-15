package app

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/sandbox"
)

func TestHelpDiscoverReadAndPageWithCurrentRights(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.tools.executor = sandbox.Inline{}
	f.tools.cfg.FileTools.Enabled = true
	f.tools.cfg.Transcription.Enabled = true
	f.tools.cfg.Sandbox = config.Sandbox{ScriptTimeoutCeilingMS: 5000}
	raw, err := f.tools.executeScript(
		t.Context(),
		f.run,
		`const list=await modules.help.list({module:"files",query:"PDF",limit:2}); const card=await modules.help.read({module:"files",method:"pdf_text",max_bytes:256}); const tail=await modules.help.read({module:"files",method:"pdf_text",cursor:card.next_cursor,max_bytes:8192}); const source=await modules.help.read({module:"fixture",method:"fetch_file"}); return {list,card,tail,source};`,
	)
	require.NoError(t, err)
	var result struct {
		List struct {
			Items      []helpEntry `json:"items"`
			Partial    bool        `json:"partial"`
			NextCursor string      `json:"next_cursor"`
		} `json:"list"`
		Card struct {
			Text       string `json:"text"`
			Partial    bool   `json:"partial"`
			NextCursor string `json:"next_cursor"`
		} `json:"card"`
		Tail struct {
			Text    string `json:"text"`
			Partial bool   `json:"partial"`
		} `json:"tail"`
		Source struct {
			Text string `json:"text"`
		} `json:"source"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.List.Items, 2)
	require.True(t, result.List.Partial)
	require.NotEmpty(t, result.List.NextCursor)
	require.True(t, result.Card.Partial)
	require.LessOrEqual(t, len(result.Card.Text), 256)
	require.False(t, result.Tail.Partial)
	require.Contains(t, result.Card.Text+result.Tail.Text, "max_chars")
	require.Contains(t, result.Card.Text+result.Tail.Text, "Example:")
	require.Contains(t, result.Source.Text, "Input schema:")
	require.Contains(t, result.Source.Text, "Output schema:")
	require.NotContains(t, result.Source.Text, "read-token")
	paid, paidErr := f.tools.builtin(
		t.Context(),
		f.run,
		helpModule,
		helpListMethod,
		json.RawMessage(`{"module":"$tools","query":"audio"}`),
	)
	require.NoError(t, paidErr)
	require.Contains(t, string(paid), `"effect":"paid"`)
	delete(f.p.Permissions, "artifacts.read")
	_, err = f.tools.builtin(
		t.Context(),
		f.run,
		helpModule,
		builtinReadMethod,
		json.RawMessage(`{"module":"files","method":"pdf_text"}`),
	)
	require.Error(t, err)
	raw, err = f.tools.builtin(t.Context(), f.run, helpModule, helpListMethod, json.RawMessage(`{"module":"files"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"partial":false}`, string(raw))
	f.tools.cfg.Search.SemanticEnabled = false
	_, err = f.tools.builtin(
		t.Context(),
		f.run,
		helpModule,
		builtinReadMethod,
		json.RawMessage(`{"module":"history","method":"semantic"}`),
	)
	require.Error(t, err)
}

func TestHelpRejectsInvalidCursorAndBounds(t *testing.T) {
	t.Parallel()
	_, err := helpPage("help.read", "abc", helpRequest{Cursor: "-1"})
	require.Error(t, err)
	_, err = helpPage("help.read", "abc", helpRequest{MaxBytes: 1})
	require.Error(t, err)
	_, err = helpPage("help.read", "é", helpRequest{Cursor: "1"})
	require.Error(t, err)
}
