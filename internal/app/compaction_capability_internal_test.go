package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/provider"
)

func TestProviderRouterKeepsWrappedCompactionCapability(t *testing.T) {
	t.Parallel()
	mock, err := provider.NewMock(t.TempDir())
	require.NoError(t, err)
	responses, err := provider.NewResponses("https://provider.invalid/v1", "fixture", nil)
	require.NoError(t, err)
	request := provider.Request{Provider: "openai"}
	require.False(t, (providerRouter{"openai": measuredProvider{client: mock}}).SupportsCompaction(request))
	require.True(t, (providerRouter{"openai": measuredProvider{client: responses}}).SupportsCompaction(request))
}
