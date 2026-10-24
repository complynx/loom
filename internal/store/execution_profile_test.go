package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestExecutionProfilePersistsAcrossRetryAndWorkerRestart(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("descriptor")
	in := domain.CreateRun{ExecutionProfile: domain.ProfileNativeFiles, Input: domain.Input{Text: "read original file"}}
	run, err := s.CreateRun(t.Context(), p, "profile", in)
	require.NoError(t, err)
	restarted := store.New(s.Pool(), nil)
	loaded, err := restarted.GetRun(t.Context(), p, run.ID)
	require.NoError(t, err)
	require.True(t, loaded.NativeFilesOnly())
	claimed, err := restarted.ClaimRun(t.Context(), run.ID, "descriptor-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed.NativeFilesOnly())
	retry, err := restarted.CreateRun(t.Context(), p, "profile", in)
	require.NoError(t, err)
	require.Equal(t, run.ID, retry.ID)
	require.True(t, retry.NativeFilesOnly())
	in.ExecutionProfile = ""
	_, err = restarted.CreateRun(t.Context(), p, "profile", in)
	code(t, err, "idempotency_conflict")
}
