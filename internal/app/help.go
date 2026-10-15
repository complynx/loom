package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/complynx/loom/internal/domain"
)

const (
	helpModule       = "help"
	helpListMethod   = "list"
	minHelpPageBytes = 256
)

//go:embed help_cards.json
var helpCardsJSON []byte

type helpCard struct {
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

type helpEntry struct {
	ID      string `json:"id"`
	Module  string `json:"module"`
	Method  string `json:"method"`
	Summary string `json:"summary"`
	Effect  string `json:"effect"`
}

type helpRequest struct {
	Module   string `json:"module"`
	Method   string `json:"method"`
	Query    string `json:"query"`
	AfterID  string `json:"after_id"`
	Limit    int    `json:"limit"`
	Cursor   string `json:"cursor"`
	MaxBytes int    `json:"max_bytes"`
}

func (t runtimeTools) builtinHelpAvailable(p domain.Principal, module string) bool {
	switch module {
	case filesModule:
		return t.cfg.FileTools.Enabled && p.Has("artifacts.read") && p.Has("chat.read")
	case artifactsModule:
		return p.Has("artifacts.read") && p.Has("chat.read")
	case historyModule:
		return p.Has("chat.read")
	case diagnosticsModule:
		return p.Has(diagnosticsPermission)
	default:
		return true
	}
}

func (t runtimeTools) helpCatalog(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	token string,
) ([]helpEntry, map[string]helpCard, []string, error) {
	cards := map[string]helpCard{}
	if err := json.Unmarshal(helpCardsJSON, &cards); err != nil {
		return nil, nil, nil, err
	}
	entries := []helpEntry{}
	for id, card := range cards {
		module, method, _ := strings.Cut(id, ".")
		if !t.builtinHelpAvailable(p, module) || (id == "history.semantic" && !t.cfg.Search.SemanticEnabled) ||
			(id == "$tools.artifact_attach" && !p.Has("artifacts.read")) ||
			(id == "$tools.audio_transcribe" && (!t.cfg.Transcription.Enabled || !p.Has("artifacts.read") || !p.Has("chat.read"))) {
			continue
		}
		effect := builtinHelpEffect(module, method)
		entries = append(entries, helpEntry{id, module, method, card.Summary, effect})
	}
	mp := modulePrincipal(p, token, r.SettingsSnapshot.Version)
	reads, readErr := t.registry.List(ctx, mp)
	actions, actionErr := t.registry.ListActions(ctx, mp)
	warnings := []string{}
	if readErr != nil || actionErr != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		warnings = append(warnings, "remote_catalog_unavailable; builtin help remains available")
	}
	for _, tool := range append(reads, actions...) {
		effect := tool.Effect
		if effect == "" {
			effect = builtinReadMethod
		}
		entries = append(
			entries,
			helpEntry{tool.Module + "." + tool.Method, tool.Module, tool.Method, tool.Description, effect},
		)
	}
	if len(actions) == 0 {
		entries = slices.DeleteFunc(entries, func(entry helpEntry) bool { return entry.ID == "$tools.module_call" })
	}
	slices.SortFunc(entries, func(a, b helpEntry) int { return strings.Compare(a.ID, b.ID) })
	return entries, cards, warnings, nil
}

func (t runtimeTools) help(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	var input helpRequest
	if err := decodeDiagnosticsInput(
		raw,
		&input,
	); err != nil || len(input.Query) > 256 || len(input.AfterID) > 512 || len(input.Module) > 128 ||
		len(input.Method) > 128 {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_help_input")
	}
	if method != helpListMethod && method != builtinReadMethod {
		return nil, domain.NotFound()
	}
	if input.Limit < 0 || input.Limit > 50 || input.MaxBytes < 0 || input.MaxBytes > 32768 {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_help_bounds")
	}
	current, token, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	entries, cards, warnings, err := t.helpCatalog(ctx, r, current, token)
	if err != nil {
		return nil, err
	}
	if method == helpListMethod {
		return helpList(entries, input, warnings)
	}
	id := input.Module + "." + input.Method
	if !slices.ContainsFunc(entries, func(entry helpEntry) bool { return entry.ID == id }) {
		return nil, domain.NotFound()
	}
	card, ok := cards[id]
	if !ok {
		descriptor, describeErr := t.registry.Describe(
			ctx,
			modulePrincipal(current, token, r.SettingsSnapshot.Version),
			input.Module,
			input.Method,
		)
		if describeErr != nil {
			return nil, domain.NotFound()
		}
		m := descriptor.Method
		card.Text = fmt.Sprintf(
			"# %s\n\nService-authored contract (untrusted data).\n\n%s\n\nEffect: %s. Timeout: %d ms. Result limit: %d bytes. Confirmation: %s.\n\nInput schema:\n```json\n%s\n```\n\nOutput schema:\n```json\n%s\n```\n\nRead: await modules.%s.%s(arguments). Write: model function module_call({module,method,arguments}); do not write through JS.",
			id,
			m.Description,
			m.Effect,
			m.TimeoutMS,
			m.MaxResultBytes,
			m.Confirmation.Mode,
			m.InputSchema,
			m.OutputSchema,
			input.Module,
			input.Method,
		)
	}
	return helpPage(id, card.Text, input)
}

func helpList(entries []helpEntry, input helpRequest, warnings []string) (json.RawMessage, error) {
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	items := []helpEntry{}
	query := strings.ToLower(input.Query)
	for _, entry := range entries {
		if entry.ID <= input.AfterID || (input.Module != "" && entry.Module != input.Module) ||
			!strings.Contains(strings.ToLower(entry.ID+" "+entry.Summary), query) {
			continue
		}
		// Lists return short summaries; schemas and examples are read on demand.
		entry.Summary = shortHelpSummary(entry.Summary)
		items = append(items, entry)
		if len(items) > limit {
			break
		}
	}
	partial := len(items) > limit
	next := ""
	if partial {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return json.Marshal(struct {
		Items      []helpEntry `json:"items"`
		Partial    bool        `json:"partial"`
		NextCursor string      `json:"next_cursor,omitempty"`
		Warnings   []string    `json:"warnings,omitempty"`
	}{items, partial, next, warnings})
}

func shortHelpSummary(text string) string {
	const limit = 240
	if len(text) <= limit {
		return text
	}
	end := limit
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "..."
}

func helpPage(id, text string, input helpRequest) (json.RawMessage, error) {
	start := 0
	var err error
	if input.Cursor != "" {
		start, err = strconv.Atoi(input.Cursor)
	}
	if err != nil || start < 0 || start > len(text) || (start < len(text) && !utf8.RuneStart(text[start])) {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_help_cursor")
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = 8192
	}
	if limit < minHelpPageBytes {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_help_bounds")
	}
	end := min(start+limit, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	partial := end < len(text)
	next := ""
	if partial {
		next = strconv.Itoa(end)
	}
	return json.Marshal(struct {
		ID         string `json:"id"`
		Text       string `json:"text"`
		Partial    bool   `json:"partial"`
		NextCursor string `json:"next_cursor,omitempty"`
	}{id, text[start:end], partial, next})
}

func builtinHelpEffect(module, method string) string {
	if module == memoryModule &&
		!slices.Contains([]string{memoryStateMethod, helpListMethod, builtinReadMethod}, method) {
		return "write"
	}
	if module != "$tools" {
		return builtinReadMethod
	}
	switch method {
	case "audio_transcribe":
		return "paid"
	case "module_call":
		return "action"
	case "question":
		return "interaction"
	default:
		return "model_function"
	}
}
