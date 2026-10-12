package lifecycle_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

func TestTemporaryFilesExpireDuringActiveRunAndReleaseQuotaAfterCollection(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := t.Context()
	p := principal("temporary")
	p.Permissions["artifacts.read"] = true
	run, err := s.CreateRun(ctx, p, "run", request(""))
	require.NoError(t, err)
	root := t.TempDir()
	files, err := artifacts.New(root, s, artifacts.Limits{MaxUploadBytes: 8, QuotaBytes: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	owner := artifacts.Principal{OwnerID: run.OwnerID, OrgID: p.OrgID}
	source, err := files.Upload(ctx, owner, "text/plain", bytes.NewBufferString("12345678"))
	require.NoError(t, err)
	require.WithinDuration(t, source.CreatedAt.Add(24*time.Hour), source.ExpiresAt, time.Second)
	require.NoError(t, s.AttachChatArtifact(ctx, p, run.ChatID, source.ID))
	handle, err := s.CreateArtifactHandle(ctx, p, run.ID, source.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE artifacts SET expires_at=now()-interval '1 second' WHERE id=$1", source.ID)
	require.NoError(t, err)
	_, err = files.Metadata(ctx, owner, source.ID)
	require.ErrorIs(t, err, artifacts.ErrNotFound)
	_, err = s.LoadArtifactHandle(ctx, p, handle.ID)
	require.Error(t, err)
	_, err = files.Upload(ctx, owner, "text/plain", bytes.NewBufferString("a"))
	require.Error(t, err)
	swept, err := s.SweepLifecycle(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), swept.Artifacts)
	count, err := files.CollectDeleted(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	_, err = os.Stat(filepath.Join(root, source.ID))
	require.ErrorIs(t, err, os.ErrNotExist)
	live, err := s.GetRun(ctx, p, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, live.State)
	_, err = files.Upload(ctx, owner, "text/plain", bytes.NewBufferString("12345678"))
	require.NoError(t, err)
	count, err = files.CollectDeleted(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, count)
	maxTTLFiles, err := artifacts.New(
		t.TempDir(),
		s,
		artifacts.Limits{MaxUploadBytes: 8, QuotaBytes: 32, TTL: 7 * 24 * time.Hour},
	)
	require.NoError(t, err)
	defer maxTTLFiles.Close()
	maximum, err := maxTTLFiles.Upload(ctx, owner, "text/plain", bytes.NewBufferString("max"))
	require.NoError(t, err)
	stored, err := s.GetArtifact(ctx, maximum.ID)
	require.NoError(t, err)
	require.Equal(t, stored.CreatedAt.Add(7*24*time.Hour), stored.ExpiresAt)
}

func TestLegacyUpgradeExpiresFilesAndRemovesCheckpointCopies(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := t.Context()
	p := principal("legacy")
	run, err := s.CreateRun(ctx, p, "legacy", request(""))
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "compact", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	old := json.RawMessage(
		`{"output":[{"role":"user","content":[{"type":"input_text","text":"Keep text"},{"type":"input_image","image_url":"data:image/png;base64,PRIVATE_BYTES"}]},{"type":"compaction","encrypted_content":"opaque-context"}]}`,
	)
	require.NoError(t, s.CompleteStep(ctx, run.ID, "worker", run.Fence, step.ID, old, domain.Usage{}))
	tool, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "tool", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	legacyTool, err := json.Marshal(
		map[string]string{
			"output": `{"nested":{"encoding":"base64","metadata":{"artifact_id":"source"},"data":"OLD_FILE_BYTES"},"text":"Keep tool text"}`,
		},
	)
	require.NoError(t, err)
	require.NoError(t, s.CompleteStep(ctx, run.ID, "worker", run.Fence, tool.ID, legacyTool, domain.Usage{}))
	item := domain.Artifact{
		ID:        "12345678123412341234123456781234",
		OwnerID:   run.OwnerID,
		OrgID:     p.OrgID,
		MIME:      "text/plain",
		SHA256:    "digest",
		Size:      1,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	require.NoError(t, s.CommitArtifact(ctx, item, 10))
	_, err = s.Pool().
		Exec(ctx, "ALTER TABLE artifacts DROP COLUMN expires_at; ALTER TABLE artifacts DROP COLUMN source_external_id")
	require.NoError(t, err)
	raw, err := os.ReadFile("../../migrations/00002_temporary_artifacts.sql")
	require.NoError(t, err)
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	_, err = s.Pool().Exec(ctx, up)
	require.NoError(t, err)
	migrated, err := s.GetArtifact(ctx, item.ID)
	require.NoError(t, err)
	require.WithinDuration(t, item.CreatedAt.Add(24*time.Hour), migrated.ExpiresAt, time.Second)
	rows, err := s.ExecutionSteps(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.NotContains(t, string(row.Result), "PRIVATE_BYTES")
		require.NotContains(t, string(row.Result), "OLD_FILE_BYTES")
		require.Contains(t, string(row.Result), "Keep")
		require.Zero(t, row.Usage.Microcredits)
	}
	require.Contains(t, string(rows[0].Result), "opaque-context")
	fresh, err := s.GetRun(ctx, p, run.ID)
	require.NoError(t, err)
	require.Zero(t, fresh.ReservedMicrocredits)
	require.Zero(t, fresh.SpentMicrocredits)
}
