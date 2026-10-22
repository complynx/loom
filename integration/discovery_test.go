package integration_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestDiscoveryExpansionIsDurableAndPartOfImmutableRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	input := runRequest("", "", "Find an appointment")
	input.Discovery = &domain.DiscoveryOptions{ExpandServices: []string{"appointments", "$loom"}}
	first := f.createRun(t, input, "discovery")
	restored, err := f.repository.ExecutionRun(t.Context(), first.ID)
	require.NoError(t, err)
	require.Equal(t, input.Discovery, restored.Discovery)
	replayed := f.createRun(t, input, "discovery")
	require.Equal(t, first.ID, replayed.ID)
	input.Discovery = &domain.DiscoveryOptions{ExpandServices: []string{"billing"}}
	code, _ := f.request(t, "alice", "POST", "/v1/runs", "discovery", input)
	require.Equal(t, 409, code)
	for _, invalid := range [][]string{{"appointments", "appointments"}, {"*"}, {""}, {"foreign/service"}} {
		input.Discovery = &domain.DiscoveryOptions{ExpandServices: invalid}
		code, _ = f.request(t, "alice", "POST", "/v1/runs", "invalid-discovery", input)
		require.Equal(t, 422, code)
	}
}
