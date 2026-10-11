package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/store"
)

func TestSemanticToolIsHiddenWhenDisabled(t *testing.T) {
	t.Parallel()
	p := domain.Principal{
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		Permissions: map[string]bool{"chat.read": true},
	}
	tools := runtimeTools{
		registry:  &modules.Registry{},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	catalog, err := tools.tools(t.Context(), domain.Run{}, p, "")
	require.NoError(t, err)
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "semantic")
	require.Contains(t, string(raw), "literal")
	require.Contains(t, string(raw), "history.read")
	tools.cfg.Search.SemanticEnabled = true
	catalog, err = tools.tools(t.Context(), domain.Run{}, p, "")
	require.NoError(t, err)
	raw, err = json.Marshal(catalog)
	require.NoError(t, err)
	require.Contains(t, string(raw), "history.semantic")
}

func searchToolStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLX_TEST_DATABASE_DSN")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "tools_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	require.NoError(t, store.Migrate(ctx, parsed.String()))
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, dropErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, dropErr)
		admin.Close()
	})
	repository := store.New(pool, nil)
	require.NoError(
		t,
		repository.Bootstrap(
			ctx,
			domain.Settings{
				InitialCredits:     10000,
				MaxRunMicrocredits: 1000,
				DefaultModelID:     "test",
				DefaultEffort:      "medium",
				ExternalDisclosure: true,
				Models: []domain.Model{
					{
						ID:              "test",
						Provider:        "openai",
						ProviderModel:   "test",
						Efforts:         []string{"medium"},
						MaxEffort:       "medium",
						SupportsImages:  true,
						MaxOutputTokens: 100,
					},
				},
			},
		),
	)
	return repository, pool
}

func TestArtifactHandlesAndPagedBytesNeverNeedJSData(t *testing.T) {
	t.Parallel()
	repository, pool := searchToolStore(t)
	ctx := t.Context()
	p := domain.Principal{
		Issuer:      "issuer",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"chat.read": true, "runs.create": true, "artifacts.read": true},
	}
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 2 << 20, QuotaBytes: 4 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	data := bytes.Repeat([]byte("hello "), 120000)
	blob, err := files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		bytes.NewReader(data),
	)
	require.NoError(t, err)
	run, err := repository.CreateRun(ctx, p, "run", domain.CreateRun{Input: domain.Input{Text: "inspect"}})
	require.NoError(t, err)
	tools := runtimeTools{
		repository: repository,
		files:      files,
		cfg: config.Config{
			Providers: map[string]config.Provider{"openai": {}},
			Artifacts: config.Artifacts{MaxUploadBytes: 2 << 20},
		},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	raw, _ := json.Marshal(map[string]any{"artifact_id": blob.ID})
	reference, err := tools.openArtifact(ctx, run, p, raw)
	require.NoError(t, err)
	require.NotContains(t, string(reference), "base64")
	var handle domain.ArtifactHandle
	require.NoError(t, json.Unmarshal(reference, &handle))
	require.NotEmpty(t, handle.ID)
	bytesRead := []byte{}
	offset := int64(0)
	for {
		raw, _ = json.Marshal(map[string]any{"artifact_id": blob.ID, "offset": offset, "limit": 100000})
		page, readErr := tools.readArtifact(ctx, run, raw)
		require.NoError(t, readErr)
		var result struct {
			Data    string `json:"data"`
			Next    int64  `json:"next_offset"`
			Partial bool   `json:"partial"`
		}
		require.NoError(t, json.Unmarshal(page, &result))
		chunk, decodeErr := base64.StdEncoding.DecodeString(result.Data)
		require.NoError(t, decodeErr)
		bytesRead = append(bytesRead, chunk...)
		if !result.Partial {
			break
		}
		require.Greater(t, result.Next, offset)
		offset = result.Next
	}
	require.Equal(t, data, bytesRead)
	png, _ := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jfWQAAAAASUVORK5CYII=",
	)
	image, err := files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"image/png",
		bytes.NewReader(png),
	)
	require.NoError(t, err)
	imageHandle, err := repository.CreateArtifactHandle(ctx, p, run.ID, image.ID)
	require.NoError(t, err)
	items, err := tools.attachArtifact(ctx, run, imageHandle.ID)
	require.NoError(t, err)
	require.Contains(t, string(items[0]), "input_image")
	require.Contains(t, string(items[0]), "data:image/png;base64,")
	delete(p.Permissions, "artifacts.read")
	_, err = tools.attachArtifact(ctx, run, imageHandle.ID)
	require.Error(t, err)
	p.Permissions["artifacts.read"] = true
	_, err = pool.Exec(
		ctx,
		"UPDATE artifact_handles SET expires_at=now()-interval '1 second' WHERE id=$1",
		imageHandle.ID,
	)
	require.NoError(t, err)
	_, err = tools.attachArtifact(ctx, run, imageHandle.ID)
	require.Error(t, err)
}
