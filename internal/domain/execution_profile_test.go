package domain_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestExecutionProfileValidation(t *testing.T) {
	t.Parallel()
	require.NoError(t, domain.ExecutionProfile("").Validate())
	require.NoError(t, domain.ProfileNativeFiles.Validate())
	require.Error(t, domain.ExecutionProfile("all").Validate())
}
