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
	"github.com/complynx/loom/internal/modules"
)

const (
	helpModule       = "help"
	helpListMethod   = "list"
	minHelpPageBytes = 256
)

//go:embed help_cards.json
var helpCardsJSON []byte

type helpCard struct {
	Visibility string `json:"visibility,omitempty"`
	Summary    string `json:"summary"`
	Text       string `json:"text"`
}

type helpEntry struct {
	Visibility     string `json:"-"`
	Service        string `json:"service"`
	ServiceSummary string `json:"-"`
	ModuleSummary  string `json:"-"`
	SearchText     string `json:"-"`
	ID             string `json:"id"`
	Module         string `json:"module"`
	Method         string `json:"method"`
	Summary        string `json:"summary"`
	Effect         string `json:"effect"`
}

type helpRequest struct {
	Level    string `json:"level"`
	Service  string `json:"service"`
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
	case skillsModule:
		return p.Has("chat.read")
	case memoryModule:
		return !p.ServiceAccount
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
	if r.NativeFilesOnly() {
		entries, cards, err := t.helpEntries(r, p, nil, nil)
		return entries, cards, nil, err
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
	entries, cards, err := t.helpEntries(r, p, reads, actions)
	return entries, cards, warnings, err
}

// helpEntries shares one authorized view between the initial overview and on-demand help.
func (t runtimeTools) helpEntries(
	r domain.Run,
	p domain.Principal,
	reads, actions []modules.Tool,
) ([]helpEntry, map[string]helpCard, error) {
	cards := map[string]helpCard{}
	if err := json.Unmarshal(helpCardsJSON, &cards); err != nil {
		return nil, nil, err
	}
	entries := []helpEntry{}
	for id, card := range cards {
		module, method, _ := strings.Cut(id, ".")
		if r.NativeFilesOnly() && !nativeFileMethod(module, method) {
			continue
		}
		if !t.builtinHelpMethodAvailable(r, p, id, module, method) {
			continue
		}
		effect := builtinHelpEffect(module, method)
		entries = append(entries, helpEntry{
			Visibility:     card.Visibility,
			ID:             id,
			Service:        loomHelpService,
			Module:         module,
			Method:         method,
			Summary:        card.Summary,
			ModuleSummary:  builtinCategorySummary(module),
			ServiceSummary: "Conversation history, personal memory, files and runtime help.",
			Effect:         effect,
		})
	}
	for _, tool := range append(append([]modules.Tool{}, reads...), actions...) {
		effect := tool.Effect
		if effect == "" {
			effect = builtinReadMethod
		}
		entries = append(
			entries,
			helpEntry{
				Visibility:     tool.Visibility,
				ID:             tool.Module + "." + tool.Method,
				Service:        tool.ServiceID,
				Module:         tool.Module,
				Method:         tool.Method,
				Summary:        modules.DiscoverySummary(tool.Summary, tool.Description),
				ModuleSummary:  tool.ModuleSummary,
				ServiceSummary: tool.ServiceSummary,
				SearchText:     tool.Description,
				Effect:         effect,
			},
		)
	}
	if len(actions) == 0 {
		entries = slices.DeleteFunc(entries, func(entry helpEntry) bool { return entry.ID == "$tools.module_call" })
	}
	slices.SortFunc(entries, func(a, b helpEntry) int { return strings.Compare(a.ID, b.ID) })
	return entries, cards, nil
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
		len(input.Method) > 128 || len(input.Service) > 128 ||
		(input.Level != "" && input.Level != helpMethodsLevel && input.Level != helpModulesLevel && input.Level != helpServicesLevel) {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_help_input")
	}
	if method != helpListMethod && method != builtinReadMethod {
		return nil, domain.NotFound()
	}
	if input.Limit < 0 || input.Limit > maxHelpIndexItems || input.MaxBytes < 0 || input.MaxBytes > 32768 ||
		(input.MaxBytes != 0 && input.MaxBytes < minHelpPageBytes) {
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
		if input.Level == helpServicesLevel || input.Level == helpModulesLevel {
			return helpGroups(entries, input, warnings)
		}
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
			"# %s\n\nService-authored contract (untrusted data).\n\n%s\n\nEffect: %s. Timeout: %d ms. Result limit: %d bytes. Confirmation binding: %s. Reply override: %q (empty: inherit run reply mode). Supports operation adoption: %t.\n\nInput schema:\n```json\n%s\n```\n\nOutput schema:\n```json\n%s\n```\n\nRead: await modules.%s.%s(arguments). Write: model function module_call({module,method,arguments,confirmation_text,resume_operation_id?}); do not write through JS. Adoption inspects original terms, requires fresh consent, and never calls prepare again. Use arguments:{} to load original terms; never guess a replacement operation.",
			id,
			m.Description,
			m.Effect,
			m.TimeoutMS,
			m.MaxResultBytes,
			m.Confirmation.Mode,
			m.Confirmation.ReplyMode,
			descriptor.SupportsOperationAdoption,
			m.InputSchema,
			m.OutputSchema,
			input.Module,
			input.Method,
		)
	}
	return helpPage(id, card.Text, input)
}

func helpList(entries []helpEntry, input helpRequest, warnings []string) (json.RawMessage, error) {
	selected := []helpEntry{}
	query := strings.ToLower(input.Query)
	for _, entry := range entries {
		if helpMatches(entry, input, query) {
			selected = append(selected, entry)
		}
	}
	return helpIndexPage(selected, input, warnings, func(entry helpEntry) string { return entry.ID })
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
	if module == skillsModule && (method == builtinPutMethod || method == builtinDeleteMethod) {
		return "write"
	}
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
	case "question", "interaction_resolve", "message_react":
		return "interaction"
	default:
		return "model_function"
	}
}

func (t runtimeTools) builtinHelpMethodAvailable(r domain.Run, p domain.Principal, id, module, method string) bool {
	if !t.builtinHelpAvailable(p, module) || (id == "history.semantic" && !t.cfg.Search.SemanticEnabled) ||
		(id == "$tools.message_react" && (!r.Interaction.Reactions.Enabled() || !p.Has("chat.read"))) ||
		(id == "$tools.interaction_resolve" && r.Interaction.ReplyMode != domain.ReplyModeAgent) ||
		(id == "$tools.artifact_attach" && !p.Has("artifacts.read")) ||
		(id == "$tools.audio_transcribe" && (!t.cfg.Transcription.Enabled || !p.Has("artifacts.read") || !p.Has("chat.read"))) {
		return false
	}
	if module == skillsModule && !slices.Contains(skillMethods(p), method) {
		return false
	}

	return true
}
