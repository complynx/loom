package app

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
)

func TestVisibilityControlsDiscoveryWithoutRemovingCapabilities(t *testing.T) {
	t.Parallel()
	tools := runtimeTools{}
	reads := []modules.Tool{}
	for _, visibility := range []string{"default", "list", "search", "hidden"} {
		reads = append(
			reads,
			modules.Tool{
				Module:     visibility + "category",
				Method:     "read",
				ServiceID:  visibility + "service",
				Visibility: visibility,
				Summary:    visibility + "marker",
			},
		)
	}
	entries, _, err := tools.helpEntries(domain.Run{}, domain.Principal{}, reads, nil)
	require.NoError(t, err)
	overview, err := environmentOverview(entries, &domain.DiscoveryOptions{
		ExpandServices: []string{"defaultservice", "listservice", "searchservice", "hiddenservice"},
	})
	require.NoError(t, err)
	require.Contains(t, string(overview), "defaultcategory")
	for _, visibility := range []string{"list", "search", "hidden"} {
		require.NotContains(t, string(overview), visibility+"category")
	}
	page, err := helpList(entries, helpRequest{}, nil)
	require.NoError(t, err)
	require.Contains(t, string(page), "listcategory")
	require.NotContains(t, string(page), "searchcategory")
	require.NotContains(t, string(page), "hiddencategory")
	search, err := helpList(entries, helpRequest{Query: "marker"}, nil)
	require.NoError(t, err)
	require.Contains(t, string(search), "searchcategory")
	require.NotContains(t, string(search), "hiddencategory")
	require.True(t, skillRequirementsAvailable(domain.Skill{Require: []string{"hiddencategory.read"}}, entries))
	// Discovery filters do not mutate the authorized entries used by direct reads.
	encoded, err := json.Marshal(entries)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "hiddencategory.read")
}
