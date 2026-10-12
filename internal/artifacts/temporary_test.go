package artifacts_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

func TestDownloadAlreadyOpenStopsAtFileDeadline(t *testing.T) {
	t.Parallel()
	rows := &metadataStore{rows: map[string]domain.Artifact{}}
	files, err := artifacts.New(
		t.TempDir(),
		rows,
		artifacts.Limits{MaxUploadBytes: 1024, QuotaBytes: 2048, TTL: time.Second},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	item, err := files.Upload(t.Context(), testOwner(), "text/plain", bytes.NewBufferString("temporary"))
	require.NoError(t, err)
	_, r, err := files.Download(t.Context(), testOwner(), item.ID)
	require.NoError(t, err)
	defer r.Close()
	wait, cancel := context.WithDeadline(t.Context(), item.ExpiresAt)
	defer cancel()
	<-wait.Done()
	_, err = r.Read(make([]byte, 1))
	require.ErrorIs(t, err, artifacts.ErrNotFound)
	_, err = files.Metadata(t.Context(), testOwner(), item.ID)
	require.ErrorIs(t, err, artifacts.ErrNotFound)
}
