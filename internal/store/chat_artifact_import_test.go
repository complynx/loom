package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/store"
)

func importSource(t *testing.T, authorize func(context.Context) error) (artifacts.Source, artifacts.Descriptor) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello")
	}))
	t.Cleanup(server.Close)
	digest := sha256.Sum256([]byte("hello"))
	return artifacts.Source{ServiceID: "test-module", BaseURL: server.URL, Client: server.Client(),
			AllowedMIMETypes: []string{"text/plain"}, Authorize: authorize},
		artifacts.Descriptor{Path: "/blob", MIME: "text/plain", Size: 5, SHA256: hex.EncodeToString(digest[:])}
}

func TestModuleImportRejectsDeletedChatWithoutConsumingQuota(t *testing.T) {
	t.Parallel()
	repository := testStore(t)
	ctx := context.Background()
	p := principal("import-owner")
	p.Permissions["chat.delete"] = true
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	chat, err := repository.CreateChat(ctx, p)
	require.NoError(t, err)
	path := t.TempDir()
	files, err := artifacts.New(path, repository, artifacts.Limits{MaxUploadBytes: 5, QuotaBytes: 5})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	calls := 0
	source, descriptor := importSource(t, func(context.Context) error {
		calls++
		if calls == 2 {
			return repository.DeleteChat(ctx, p, chat.ID)
		}
		return nil
	})
	_, err = files.ImportToChat(ctx, p, uid, chat.ID, source, descriptor)
	require.ErrorIs(t, err, domain.ErrNotFound)
	ids, err := repository.ListArtifactIDs(ctx)
	require.NoError(t, err)
	require.Empty(t, ids)
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	require.Empty(t, entries)
	// The failed import leaves the entire quota available to ordinary uploads.
	_, err = files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		strings.NewReader("hello"),
	)
	require.NoError(t, err)
}

func TestModuleImportIsReadableAndCollectedWithChat(t *testing.T) {
	t.Parallel()
	repository := testStore(t)
	ctx := context.Background()
	p := principal("import-owner")
	p.Permissions["chat.delete"] = true
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	chat, err := repository.CreateChat(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 5, QuotaBytes: 5})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	source, descriptor := importSource(t, func(context.Context) error { return nil })
	item, err := files.ImportToChat(ctx, p, uid, chat.ID, source, descriptor)
	require.NoError(t, err)
	metadata, err := files.Metadata(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, item.ID)
	require.NoError(t, err)
	require.Equal(t, item.ID, metadata.ID)
	_, body, err := files.Download(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, metadata.ID)
	require.NoError(t, err)
	content, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "hello", string(content))
	require.NoError(t, repository.DeleteChat(ctx, p, chat.ID))
	deleted, err := repository.ListDeletedArtifacts(ctx, 10)
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	require.Equal(t, item.ID, strings.ReplaceAll(deleted[0].ID, "-", ""))
	collected, err := files.CollectDeleted(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, collected)
	_, err = files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		strings.NewReader("hello"),
	)
	require.NoError(t, err)
}

type lostChatCommitStore struct {
	*store.Store

	lookupUnavailable bool
}

func (s lostChatCommitStore) CommitChatArtifact(
	ctx context.Context,
	p domain.Principal,
	cid string,
	item domain.Artifact,
	quota int64,
) error {
	if err := s.Store.CommitChatArtifact(ctx, p, cid, item, quota); err != nil {
		return err
	}
	return errors.New("commit response lost")
}

func (s lostChatCommitStore) GetArtifact(ctx context.Context, id string) (domain.Artifact, error) {
	if s.lookupUnavailable {
		return domain.Artifact{}, errors.New("database unavailable")
	}
	return s.Store.GetArtifact(ctx, id)
}

func TestModuleImportPreservesCommittedBlobAfterLostCommitResponse(t *testing.T) {
	t.Parallel()
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolved", true: "unresolved"}[unavailable], func(t *testing.T) {
			t.Parallel()
			repository := testStore(t)
			ctx := context.Background()
			p := principal("import-owner")
			p.Permissions["chat.delete"] = true
			uid, err := repository.ResolveUser(ctx, p)
			require.NoError(t, err)
			chat, err := repository.CreateChat(ctx, p)
			require.NoError(t, err)
			path := t.TempDir()
			files, err := artifacts.New(
				path,
				lostChatCommitStore{Store: repository, lookupUnavailable: unavailable},
				artifacts.Limits{MaxUploadBytes: 5, QuotaBytes: 5},
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, files.Close()) })
			source, descriptor := importSource(t, func(context.Context) error { return nil })
			item, err := files.ImportToChat(ctx, p, uid, chat.ID, source, descriptor)
			if unavailable {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, item.ID)
			}
			ids, err := repository.ListArtifactIDs(ctx)
			require.NoError(t, err)
			require.Len(t, ids, 1)
			// After database availability returns, the committed content must remain readable.
			recovered, err := artifacts.New(path, repository, artifacts.Limits{MaxUploadBytes: 5, QuotaBytes: 5})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, recovered.Close()) })
			_, body, err := recovered.Download(
				ctx,
				artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
				strings.ReplaceAll(ids[0], "-", ""),
			)
			require.NoError(t, err)
			content, err := io.ReadAll(body)
			require.NoError(t, err)
			require.Equal(t, "hello", string(content))
			require.NoError(t, body.Close())
			require.NoError(t, repository.DeleteChat(ctx, p, chat.ID))
			deleted, err := repository.ListDeletedArtifacts(ctx, 10)
			require.NoError(t, err)
			require.Len(t, deleted, 1)
		})
	}
}

func TestModuleImportCancellationLeavesQuotaAvailable(t *testing.T) {
	t.Parallel()
	repository := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := principal("import-owner")
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	chat, err := repository.CreateChat(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 5, QuotaBytes: 5})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	calls := 0
	source, descriptor := importSource(t, func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil
	})
	_, err = files.ImportToChat(ctx, p, uid, chat.ID, source, descriptor)
	require.ErrorIs(t, err, context.Canceled)
	ids, err := repository.ListArtifactIDs(context.Background())
	require.NoError(t, err)
	require.Empty(t, ids)
	_, err = files.Upload(
		context.Background(),
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		strings.NewReader("hello"),
	)
	require.NoError(t, err)
}
