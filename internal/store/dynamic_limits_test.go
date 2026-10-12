package store_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestDynamicLimitsRejectCeilingViolationsAndPreserveAdmittedRuns(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.SetPolicyCeilings(8, 4)
	s.SetExecutionCeilings(
		5000,
		4,
		32768,
		domain.Settings{
			TotalActiveRunLimit:     4,
			ScriptTimeoutMS:         5000,
			ParallelRPCLimit:        4,
			ContextInputTokenBudget: 32768,
		},
	)
	ctx := t.Context()
	p := principal("limits")
	original, err := s.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, original.TotalActiveRunLimit)
	first, err := s.CreateRun(ctx, p, "first", request())
	require.NoError(t, err)
	for _, field := range []string{"total", "script", "parallel", "context", "negative"} {
		candidate := original
		switch field {
		case "total":
			candidate.TotalActiveRunLimit = 5
		case "script":
			candidate.ScriptTimeoutMS = 5001
		case "parallel":
			candidate.ParallelRPCLimit = 5
		case "context":
			candidate.ContextInputTokenBudget = 32769
		case "negative":
			candidate.ScriptTimeoutMS = -1
		}
		_, err = s.UpdateSettings(ctx, p, original.Version, candidate)
		code(t, err, "hard_execution_ceiling")
	}
	reduced := original
	reduced.TotalActiveRunLimit = 1
	reduced.ScriptTimeoutMS = 100
	reduced.ParallelRPCLimit = 1
	reduced.ContextInputTokenBudget = 8192
	reduced, err = s.UpdateSettings(ctx, p, original.Version, reduced)
	require.NoError(t, err)
	other := principal("limits-other")
	_, err = s.CreateRun(ctx, other, "denied", request())
	code(t, err, "total_run_limit")
	persisted, err := s.GetRun(ctx, p, first.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, persisted.State)
	require.Equal(t, original.ScriptTimeoutMS, persisted.SettingsSnapshot.ScriptTimeoutMS)
	reduced.TotalActiveRunLimit = 2
	raised, err := s.UpdateSettings(ctx, p, reduced.Version, reduced)
	require.NoError(t, err)
	next, err := s.CreateRun(ctx, other, "allowed", request())
	require.NoError(t, err)
	require.Equal(t, raised.ScriptTimeoutMS, next.SettingsSnapshot.ScriptTimeoutMS)
	require.Equal(t, raised.ContextInputTokenBudget, next.SettingsSnapshot.ContextInputTokenBudget)
	_, err = s.UpdateSettings(ctx, p, original.Version, original)
	code(t, err, "version_conflict")
}

func TestDynamicGlobalLimitSerializesConcurrentUsers(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.SetPolicyCeilings(8, 8)
	s.SetExecutionCeilings(
		5000,
		4,
		32768,
		domain.Settings{
			TotalActiveRunLimit:     8,
			ScriptTimeoutMS:         5000,
			ParallelRPCLimit:        4,
			ContextInputTokenBudget: 32768,
		},
	)
	ctx := t.Context()
	admin := principal("admin")
	settings, err := s.GetSettings(ctx)
	require.NoError(t, err)
	settings.TotalActiveRunLimit = 2
	_, err = s.UpdateSettings(ctx, admin, settings.Version, settings)
	require.NoError(t, err)
	const users = 8
	callers := make([]domain.Principal, users)
	chats := make([]string, users)
	for i := range users {
		callers[i] = principal(fmt.Sprintf("user-%d", i))
		chat, chatErr := s.CreateChat(ctx, callers[i])
		require.NoError(t, chatErr)
		chats[i] = chat.ID
	}
	results := make(chan error, users)
	var workers sync.WaitGroup
	for i := range users {
		workers.Go(func() {
			in := request()
			in.ChatID = chats[i]
			_, createErr := s.CreateRun(ctx, callers[i], fmt.Sprintf("parallel-%d", i), in)
			results <- createErr
		})
	}
	workers.Wait()
	close(results)
	accepted := 0
	for createErr := range results {
		if createErr == nil {
			accepted++
		} else {
			code(t, createErr, "total_run_limit")
		}
	}
	require.Equal(t, 2, accepted)
}
