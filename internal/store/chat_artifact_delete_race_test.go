package store_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/store"
)

func TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits(t *testing.T) {
	t.Parallel()
	repository := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := principal("shared-artifact-owner")
	p.Permissions["chat.delete"] = true
	p.Permissions["artifacts.read"] = true
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	first, err := repository.CreateChat(ctx, p)
	require.NoError(t, err)
	second, err := repository.CreateChat(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 10, QuotaBytes: 10})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	artifactOwner := artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}
	item, err := files.Upload(ctx, artifactOwner, "text/plain", strings.NewReader("hello"))
	require.NoError(t, err)
	require.NoError(t, repository.AttachChatArtifact(ctx, p, first.ID, item.ID))
	// The queue hook holds the public CreateRun transaction after attachment, while
	// the artifact row is share-locked and the second reference is uncommitted.
	blocked := make(chan int, 1)
	release := make(chan struct{})
	attaching := store.New(repository.Pool(), func(ctx context.Context, tx pgx.Tx, _ string) error {
		var pid int
		if pidErr := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); pidErr != nil {
			return pidErr
		}
		blocked <- pid
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	attached := make(chan error, 1)
	input := request()
	input.ChatID = second.ID
	input.Input.ArtifactIDs = []string{item.ID}
	go func() {
		_, createErr := attaching.CreateRun(ctx, p, "attach-shared-blob", input)
		attached <- createErr
	}()
	var blocker int
	select {
	case blocker = <-blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	deleted := make(chan error, 1)
	go func() { deleted <- repository.DeleteChat(ctx, p, first.ID) }()
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := repository.Pool().QueryRow(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%artifacts%'
  )`, blocker).Scan(&waiting)
		return queryErr == nil && waiting
	}, time.Second, 5*time.Millisecond)
	close(release)
	require.NoError(t, <-attached)
	require.NoError(t, <-deleted)
	metadata, err := files.Metadata(ctx, artifactOwner, item.ID)
	require.NoError(t, err)
	_, body, err := files.Download(ctx, artifactOwner, metadata.ID)
	require.NoError(t, err)
	content, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "hello", string(content))
	tombstones, err := repository.ListDeletedArtifacts(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, tombstones)
	// The surviving chat still owns the reference; only deleting it collects the blob.
	require.NoError(t, repository.DeleteChat(ctx, p, second.ID))
	tombstones, err = repository.ListDeletedArtifacts(ctx, 10)
	require.NoError(t, err)
	require.Len(t, tombstones, 1)
}
