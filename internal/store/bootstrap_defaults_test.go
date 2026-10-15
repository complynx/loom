package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootstrapRejectsInvalidDefaults(t *testing.T) {
	t.Parallel()
	repository := testStore(t)
	settings, err := repository.GetSettings(t.Context())
	require.NoError(t, err)
	settings.DefaultEffort = "unsupported"
	code(t, repository.Bootstrap(t.Context(), settings), "invalid_default_model")
	settings.DefaultModelID = ""
	code(t, repository.Bootstrap(t.Context(), settings), "invalid_default_model")
	persisted, err := repository.GetSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "test", persisted.DefaultModelID)
	require.Equal(t, "low", persisted.DefaultEffort)
}
