package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func recoveryEvidence(fence int64) domain.RecoveryPrepareRequest {
	return domain.RecoveryPrepareRequest{
		ExpectedFence: fence,
		Evidence: domain.ReconcileEvidence{
			Kind:      "operator_attestation",
			Reference: "incident-restore-42",
			Reason:    "All execution writers stopped; provider receipt inventory required",
		},
	}
}

func TestRecoveryPreparationPreservesUncertaintyAndSettlesOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := testStore(t)
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	run, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "old-worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, run.ID, "old-worker", run.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, err)
	_, err = s.Control(ctx, p, run.ID, "cancel-request", "cancel")
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	// Any scheduling from this operation fails the behavior test.
	s = store.New(
		s.Pool(),
		func(context.Context, pgx.Tx, string) error { t.Error("recovery must not enqueue"); return nil },
	)
	in := recoveryEvidence(run.Fence)
	for range 2 {
		prepared, e := s.PrepareRecovery(ctx, p, run.ID, "prepare", in)
		require.NoError(t, e)
		require.Equal(t, domain.Reconciling, prepared.State)
		require.Equal(t, run.Fence+1, prepared.Fence)
		require.Empty(t, prepared.LeaseOwner)
		require.Nil(t, prepared.LeaseUntil)
		require.True(t, prepared.CancelRequested)
		require.EqualValues(t, 500, prepared.ReservedMicrocredits)
		require.Zero(t, prepared.SpentMicrocredits)
		require.Len(t, prepared.UnresolvedSteps, 1)
		require.Equal(t, step.ID, prepared.UnresolvedSteps[0].ID)
	}
	var count int
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='run.recovery.prepare' AND target=$1", run.ID).
			Scan(&count),
	)
	require.Equal(t, 1, count)
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT count(*) FROM credit_ledger WHERE run_id=$1 AND kind='settle'", run.ID).
			Scan(&count),
	)
	require.Zero(t, count)
	changed := in
	changed.Evidence.Reason = "changed attestation"
	_, err = s.PrepareRecovery(ctx, p, run.ID, "prepare", changed)
	code(t, err, "idempotency_conflict")
	_, err = s.PrepareRecovery(ctx, p, run.ID, "new-key", in)
	code(t, err, "recovery_fence_changed")
	receipt := domain.ReconcileStepRequest{
		Usage: domain.Usage{InputTokens: 2, OutputTokens: 3, Microcredits: 5},
		Evidence: domain.ReconcileEvidence{
			Kind:      "provider_receipt",
			Reference: "verified-receipt",
			Reason:    "confirmed provider charge",
		},
	}
	for range 2 {
		settled, e := s.ReconcileStep(ctx, p, run.ID, step.ID, "receipt", receipt)
		require.NoError(t, e)
		require.EqualValues(t, 5, settled.SpentMicrocredits)
		require.Zero(t, settled.ReservedMicrocredits)
	}
}

func TestRecoveryPreparationGuards(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, code string }{
		{"live lease", "lease_busy"},
		{"foreign org", "not_found"},
		{"missing right", "forbidden"},
		{"stale fence", "recovery_fence_changed"},
		{"no attestation", "invalid_recovery_evidence"},
		{"certain run", "run_not_uncertain"},
		{"queued run", "recovery_fence_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s := testStore(t)
			p := principal("alice")
			p.Permissions["operations.reconcile"] = true
			run, err := s.CreateRun(ctx, p, "run", request())
			require.NoError(t, err)
			if tc.name != "queued run" {
				run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
				require.NoError(t, err)
			}
			if tc.name != "certain run" && tc.name != "queued run" {
				_, err = s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 10)
				require.NoError(t, err)
			}
			if tc.name != "live lease" {
				_, err = s.Pool().
					Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
				require.NoError(t, err)
			}
			in := recoveryEvidence(run.Fence)
			switch tc.name {
			case "foreign org":
				p.OrgID = "other"
			case "missing right":
				delete(p.Permissions, "operations.reconcile")
			case "stale fence":
				in.ExpectedFence++
			case "no attestation":
				in.Evidence.Kind = "provider_receipt"
			case "queued run":
				in.ExpectedFence = 1
			}
			_, err = s.PrepareRecovery(ctx, p, run.ID, "prepare", in)
			code(t, err, tc.code)
			var n int
			require.NoError(
				t,
				s.Pool().
					QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='run.recovery.prepare'").
					Scan(&n),
			)
			require.Zero(t, n)
		})
	}
}

func TestRecoveryPreparationKeepsUnknownExternalWrite(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := testStore(t)
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	run, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	operation := uuid.NewString()
	_, err = s.Pool().
		Exec(ctx, "INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES($1,$2,'write-42','unknown','{}')", operation, run.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	prepared, err := s.PrepareRecovery(ctx, p, run.ID, "prepare", recoveryEvidence(run.Fence))
	require.NoError(t, err)
	require.Equal(t, domain.Reconciling, prepared.State)
	var state string
	require.NoError(t, s.Pool().QueryRow(ctx, "SELECT state FROM write_operations WHERE id=$1", operation).Scan(&state))
	require.Equal(t, "unknown", state)
}
