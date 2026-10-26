package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/complynx/loom/internal/domain"
)

const (
	loomHelpService   = "$loom"
	helpServicesLevel = "services"
	helpModulesLevel  = "modules"
	helpMethodsLevel  = "methods"
	maxHelpIndexItems = 50
)

type helpGroup struct {
	ID          string `json:"id"`
	Service     string `json:"service,omitempty"`
	Summary     string `json:"summary"`
	ModuleCount int    `json:"module_count,omitempty"`
	MethodCount int    `json:"method_count"`
}

func builtinCategorySummary(module string) string {
	switch module {
	case skillsModule:
		return "Discover and load reusable organization procedures; editing requires skills.write."
	case historyModule:
		return "Find earlier conversation facts, decisions and activity; read bounded event pages."
	case memoryModule:
		return "Recall or save the user's lasting facts and preferences across chats."
	case artifactsModule:
		return "Import service files and obtain temporary handles or source references."
	case filesModule:
		return "Inspect or extract text, images, tables and media from files."
	case diagnosticsModule:
		return "Inspect organization-scoped runtime health and run diagnostics."
	case helpModule:
		return "Discover services, categories and methods; read selected full contracts."
	default:
		return "Use the model's structured tools for questions, actions and attachments."
	}
}

func helpMatches(entry helpEntry, input helpRequest, query string) bool {
	if entry.Visibility == "hidden" || (entry.Visibility == "search" && query == "") {
		return false
	}
	if (input.Service != "" && entry.Service != input.Service) || (input.Module != "" && entry.Module != input.Module) {
		return false
	}
	return strings.Contains(
		strings.ToLower(
			entry.ID+" "+entry.Service+" "+entry.ServiceSummary+" "+entry.ModuleSummary+" "+entry.Summary+" "+entry.SearchText,
		),
		query,
	)
}

func groupedHelp(entries []helpEntry, input helpRequest) []helpGroup {
	groups := map[string]helpGroup{}
	modules := map[string]map[string]bool{}
	query := strings.ToLower(input.Query)
	for _, entry := range entries {
		if !helpMatches(entry, input, query) {
			continue
		}
		id, summary, service := entry.Module, entry.ModuleSummary, entry.Service
		if input.Level == helpServicesLevel {
			id, summary, service = entry.Service, entry.ServiceSummary, ""
		}
		group := groups[id]
		group.ID, group.Summary, group.Service = id, summary, service
		group.MethodCount++
		if input.Level == helpServicesLevel {
			if modules[id] == nil {
				modules[id] = map[string]bool{}
			}
			modules[id][entry.Module] = true
			group.ModuleCount = len(modules[id])
		}
		groups[id] = group
	}
	out := make([]helpGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, group)
	}
	slices.SortFunc(out, func(a, b helpGroup) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func helpGroups(entries []helpEntry, input helpRequest, warnings []string) (json.RawMessage, error) {
	return helpIndexPage(groupedHelp(entries, input), input, warnings, func(group helpGroup) string { return group.ID })
}

type helpIndexResult[T any] struct {
	Items      []T      `json:"items"`
	Partial    bool     `json:"partial"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// helpIndexPage bounds complete entries and includes continuation metadata in the byte limit.
func helpIndexPage[T any](
	entries []T,
	input helpRequest,
	warnings []string,
	id func(T) string,
) (json.RawMessage, error) {
	limit, maxBytes := input.Limit, input.MaxBytes
	if limit == 0 {
		limit = 20
	}
	if maxBytes == 0 {
		maxBytes = 8192
	}
	result := helpIndexResult[T]{Items: []T{}, Warnings: warnings}
	for _, entry := range entries {
		if id(entry) <= input.AfterID {
			continue
		}
		previous := len(result.Items)
		result.Items = append(result.Items, entry)
		result.Partial = true // Reserve space for the worst-case continuation before accepting the entry.
		result.NextCursor = id(entry)
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if previous == limit || len(raw) > maxBytes {
			result.Items = result.Items[:previous]
			if previous == 0 {
				return nil, domain.NewError(http.StatusUnprocessableEntity, "help_page_too_small")
			}
			result.NextCursor = id(result.Items[previous-1])
			return json.Marshal(result)
		}
	}
	result.Partial, result.NextCursor = false, ""
	return json.Marshal(result)
}

// environmentOverview has two independently paged levels; method details stay on demand.
func environmentOverview(entries []helpEntry, options *domain.DiscoveryOptions) (json.RawMessage, error) {
	entries = slices.DeleteFunc(
		slices.Clone(entries),
		func(entry helpEntry) bool { return entry.Visibility != "" && entry.Visibility != "default" },
	)
	const serviceBytes, categoryBytes = 3072, 4096
	services, err := helpGroups(
		entries,
		helpRequest{Level: helpServicesLevel, Limit: maxHelpIndexItems, MaxBytes: serviceBytes},
		nil,
	)
	if err != nil {
		return nil, err
	}
	categories, err := helpGroups(
		entries,
		helpRequest{Level: helpModulesLevel, Limit: maxHelpIndexItems, MaxBytes: categoryBytes},
		nil,
	)
	if err != nil {
		return nil, err
	}
	expanded, err := expandedServices(entries, options)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Services   json.RawMessage            `json:"services"`
		Categories json.RawMessage            `json:"categories"`
		Expanded   map[string]json.RawMessage `json:"expanded,omitempty"`
	}{services, categories, expanded})
}

type expandedMethod struct {
	Module  string `json:"module"`
	Method  string `json:"method"`
	Summary string `json:"summary"`
	Effect  string `json:"effect"`
}

func expandedServices(entries []helpEntry, options *domain.DiscoveryOptions) (map[string]json.RawMessage, error) {
	if err := domain.ValidateDiscovery(options); err != nil {
		return nil, err
	}
	if options == nil || len(options.ExpandServices) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	const expansionBytes = 16384
	out := map[string]json.RawMessage{}
	for _, service := range options.ExpandServices {
		methods := []expandedMethod{}
		for _, entry := range entries {
			if entry.Service == service {
				methods = append(methods, expandedMethod{entry.Module, entry.Method, entry.Summary, entry.Effect})
			}
		}
		page, err := helpIndexPage(
			methods,
			helpRequest{Limit: maxHelpIndexItems, MaxBytes: expansionBytes / len(options.ExpandServices)},
			nil,
			func(method expandedMethod) string { return method.Module + "." + method.Method },
		)
		if failure, ok := errors.AsType[*domain.Error](err); ok && failure.Code == "help_page_too_small" {
			// Expansion is a presentation hint. A large escaped entry must not block the run.
			page, err = json.Marshal(helpIndexResult[expandedMethod]{
				Items:   []expandedMethod{},
				Partial: true,
				Warnings: []string{
					"initial_expansion_page_too_small; use help.list for this service with a larger max_bytes",
				},
			})
		}
		if err != nil {
			return nil, err
		}
		out[service] = page
	}
	return out, nil
}
