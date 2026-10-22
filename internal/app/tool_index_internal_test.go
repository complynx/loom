package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
)

func TestHierarchyOverviewBoundsAndFindsCapabilitiesBeyondItsPage(t *testing.T) {
	t.Parallel()
	entries := []helpEntry{}
	const services, categories, methods = 12, 18, 10
	for service := range services {
		for category := range categories {
			for method := range methods {
				module := fmt.Sprintf("service%02d_category%02d", service, category)
				entries = append(entries, helpEntry{
					ID:      fmt.Sprintf("%s.method%02d", module, method),
					Service: fmt.Sprintf("service%02d", service),
					Module:  module,
					Method:  fmt.Sprintf("method%02d", method),
					Summary: "Find the requested business result.",
					ModuleSummary: strings.Repeat(
						"😀",
						160,
					),
					ServiceSummary: "Business service for synthetic discovery.",
					SearchText:     "Full contract searchable even when the overview is paged.",
					Effect:         "read",
				})
			}
		}
	}
	raw, err := environmentOverview(entries, nil)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 8192)
	var overview struct {
		Services   helpIndexResult[helpGroup] `json:"services"`
		Categories helpIndexResult[helpGroup] `json:"categories"`
	}
	require.NoError(t, json.Unmarshal(raw, &overview))
	require.True(t, overview.Categories.Partial)
	require.Len(t, overview.Services.Items, services)
	require.Equal(t, categories, overview.Services.Items[0].ModuleCount)
	require.Equal(t, categories*methods, overview.Services.Items[0].MethodCount)
	require.NotContains(t, string(raw), "Full contract")
	after, count := "", 0
	for {
		page, pageErr := helpGroups(entries, helpRequest{Level: "modules", AfterID: after, MaxBytes: 4096}, nil)
		require.NoError(t, pageErr)
		require.LessOrEqual(t, len(page), 4096)
		var result helpIndexResult[helpGroup]
		require.NoError(t, json.Unmarshal(page, &result))
		count += len(result.Items)
		if !result.Partial {
			break
		}
		require.Greater(t, result.NextCursor, after)
		after = result.NextCursor
	}
	require.Equal(t, services*categories, count)
	last := entries[len(entries)-1]
	page, err := helpList(entries, helpRequest{Query: last.ID}, nil)
	require.NoError(t, err)
	var result helpIndexResult[helpEntry]
	require.NoError(t, json.Unmarshal(page, &result))
	require.Len(t, result.Items, 1)
	require.Equal(t, last.ID, result.Items[0].ID)
	selected := []string{}
	for service := range 10 {
		selected = append(selected, fmt.Sprintf("service%02d", service))
	}
	expanded, err := environmentOverview(entries, &domain.DiscoveryOptions{ExpandServices: selected})
	require.NoError(t, err)
	require.LessOrEqual(t, len(expanded), 26*1024)
	var expansion struct {
		Expanded map[string]helpIndexResult[expandedMethod] `json:"expanded"`
	}
	require.NoError(t, json.Unmarshal(expanded, &expansion))
	require.Len(t, expansion.Expanded, len(selected))
	firstPage := expansion.Expanded[selected[0]]
	require.True(t, firstPage.Partial)
	require.NotEmpty(t, firstPage.Items)
	count, after = len(firstPage.Items), firstPage.NextCursor
	for {
		continuationPage, pageErr := helpList(entries, helpRequest{Service: selected[0], AfterID: after}, nil)
		require.NoError(t, pageErr)
		var continuation helpIndexResult[helpEntry]
		require.NoError(t, json.Unmarshal(continuationPage, &continuation))
		count += len(continuation.Items)
		if !continuation.Partial {
			break
		}
		require.Greater(t, continuation.NextCursor, after)
		after = continuation.NextCursor
	}
	require.Equal(t, categories*methods, count)
}

func TestHierarchyUsesAuthoredPurposeAndKeepsFullDescriptionSearchable(t *testing.T) {
	t.Parallel()
	tools := runtimeTools{}
	read := modules.Tool{
		ServiceID:      "unfamiliar",
		Module:         "unfamiliar_category",
		Method:         "availability",
		Summary:        "Find free appointments.",
		ModuleSummary:  "Choose or manage an appointment.",
		ServiceSummary: "Appointments and bookings.",
		Description:    "Only full selected help contains secret_schema_marker and uncommon_search_term.",
		InputSchema:    json.RawMessage(`{"secret_schema_marker":true}`),
	}
	entries, _, err := tools.helpEntries(domain.Run{}, domain.Principal{}, []modules.Tool{read}, nil)
	require.NoError(t, err)
	overview, err := environmentOverview(entries, nil)
	require.NoError(t, err)
	require.Contains(t, string(overview), read.ModuleSummary)
	require.NotContains(t, string(overview), read.Method)
	require.NotContains(t, string(overview), "secret_schema_marker")
	page, err := helpList(entries, helpRequest{Query: "uncommon_search_term"}, nil)
	require.NoError(t, err)
	var result helpIndexResult[helpEntry]
	require.NoError(t, json.Unmarshal(page, &result))
	require.Len(t, result.Items, 1)
	require.Equal(t, read.Summary, result.Items[0].Summary)
	require.NotContains(t, string(page), "secret_schema_marker")
	_, err = helpList(entries, helpRequest{MaxBytes: 1}, nil)
	require.Error(t, err)
}

func TestRequestedServiceExpansionKeepsOtherServicesCompact(t *testing.T) {
	t.Parallel()
	entries := []helpEntry{
		{
			ID:      "booking.reserve",
			Service: "appointments",
			Module:  "booking",
			Method:  "reserve",
			Summary: "Book a selected slot.",
			Effect:  "write",
		},
		{
			ID:      "invoice.lookup",
			Service: "billing",
			Module:  "invoice",
			Method:  "lookup",
			Summary: "Read an invoice.",
			Effect:  "read",
		},
	}
	raw, err := environmentOverview(
		entries,
		&domain.DiscoveryOptions{ExpandServices: []string{"appointments", "foreign"}},
	)
	require.NoError(t, err)
	var result struct {
		Expanded map[string]helpIndexResult[expandedMethod] `json:"expanded"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Expanded["appointments"].Items, 1)
	require.Equal(t, "reserve", result.Expanded["appointments"].Items[0].Method)
	require.Empty(t, result.Expanded["foreign"].Items)
	_, billingExpanded := result.Expanded["billing"]
	require.False(t, billingExpanded)
	_, err = environmentOverview(
		entries,
		&domain.DiscoveryOptions{ExpandServices: []string{"appointments", "appointments"}},
	)
	require.Error(t, err)
}

func TestEscapedLegacyExpansionFallsBackWithoutBlockingTheRun(t *testing.T) {
	t.Parallel()
	module, method := strings.Repeat("m", 128), strings.Repeat("r", 128)
	entry := helpEntry{ID: module + "." + method, Module: module, Method: method, Service: "service00",
		Summary: strings.Repeat("<", 240), Effect: "read"}
	services := []string{}
	for service := range 10 {
		services = append(services, fmt.Sprintf("service%02d", service))
	}
	raw, err := environmentOverview([]helpEntry{entry}, &domain.DiscoveryOptions{ExpandServices: services})
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 26*1024)
	var result struct {
		Expanded map[string]helpIndexResult[expandedMethod] `json:"expanded"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	fallback := result.Expanded[entry.Service]
	require.Empty(t, fallback.Items)
	require.True(t, fallback.Partial)
	require.Empty(t, fallback.NextCursor, "continuation must start at the first method")
	require.NotEmpty(t, fallback.Warnings)
	page, err := helpList([]helpEntry{entry}, helpRequest{Service: entry.Service}, nil)
	require.NoError(t, err)
	var methods helpIndexResult[helpEntry]
	require.NoError(t, json.Unmarshal(page, &methods))
	require.Len(t, methods.Items, 1)
	require.Equal(t, entry.Summary, methods.Items[0].Summary)
}
