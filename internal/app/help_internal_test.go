package app

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
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
	require.Contains(t, result.Source.Text, "Timeout:")
	require.Contains(t, result.Source.Text, "Result limit:")
	require.Contains(t, result.Source.Text, "Confirmation binding:")
	require.Contains(t, result.Source.Text, "Supports operation adoption:")
	paid, paidErr := f.tools.builtin(
		t.Context(),
		f.run,
		helpModule,
		helpListMethod,
		json.RawMessage(`{"module":"$tools","query":"audio"}`),
	)
	require.NoError(t, paidErr)
	require.Contains(t, string(paid), `"effect":"paid"`)
	card, cardErr := f.tools.builtin(t.Context(), f.run, helpModule, builtinReadMethod,
		json.RawMessage(`{"module":"$tools","method":"audio_transcribe"}`))
	require.NoError(t, cardErr)
	var audioCard struct {
		Text    string `json:"text"`
		Partial bool   `json:"partial"`
	}
	require.NoError(t, json.Unmarshal(card, &audioCard))
	require.False(t, audioCard.Partial)
	for _, term := range []string{"exactly one", "artifact_id", "reuse_only", "transcript_not_found", "transcript_id", "source_artifact_id", "files.transcript_read", "next_offset", "next_cursor", "UUID", "reprocess_key"} {
		require.Contains(t, audioCard.Text, term)
	}
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
	delete(f.p.Permissions, "qa.read")
	_, err = f.tools.builtin(t.Context(), f.run, helpModule, builtinReadMethod,
		json.RawMessage(`{"module":"fixture","method":"fetch_file"}`))
	require.Error(t, err, "selected remote help must reauthorize after revocation")
	_, err = f.tools.builtin(t.Context(), f.run, helpModule, builtinReadMethod,
		json.RawMessage(`{"module":"foreign","method":"read"}`))
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

func TestExpandedServiceRechecksCurrentMethodPermissions(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.run.Discovery = &domain.DiscoveryOptions{ExpandServices: []string{"acceptance", "foreign"}}
	catalog, err := f.tools.tools(t.Context(), f.run, f.p, "")
	require.NoError(t, err)
	require.Contains(t, catalog[0].Description, "fetch_file")
	delete(f.p.Permissions, "qa.read")
	catalog, err = f.tools.tools(t.Context(), f.run, f.p, "")
	require.NoError(t, err)
	require.NotContains(t, catalog[0].Description, "fetch_file")
	require.NotContains(t, catalog[0].Description, "fixture_dynamic")
}
