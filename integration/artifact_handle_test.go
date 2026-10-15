package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

type artifactTestAuth struct{}

func (artifactTestAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	p, err := (testAuth{}).Authenticate(ctx, r)
	if err == nil {
		p.Permissions["artifacts.read"] = true
	}
	return p, err
}
func TestHandleStreamsOriginalBytesAndDeniesForeignCaller(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	request := httptest.NewRequest(http.MethodGet, "http://test", nil)
	request.Header.Set("Authorization", "Bearer alice")
	p, err := (artifactTestAuth{}).Authenticate(ctx, request)
	require.NoError(t, err)
	uid, err := f.repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(
		t.TempDir(),
		f.repository,
		artifacts.Limits{MaxUploadBytes: 2 << 20, QuotaBytes: 4 << 20},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	data := bytes.Repeat([]byte("stream original data "), 40000)
	artifact, err := files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		bytes.NewReader(data),
	)
	require.NoError(t, err)
	run := f.createRun(t, runRequest("", "", "export blob"), "run")
	f.server.Close()
	f.server = httptest.NewServer(api.New(f.repository, artifactTestAuth{}, api.Options{Artifacts: files}))
	t.Cleanup(f.server.Close)
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/artifacts/handles",
		"",
		map[string]string{"artifact_id": artifact.ID},
	)
	require.Equal(t, 201, code, string(raw))
	handle := decode[domain.ArtifactHandle](t, raw)
	code, raw = f.request(t, "alice", "GET", handle.ContentPath, "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, data, raw)
	code, _ = f.request(t, "bob", "GET", handle.ContentPath, "", nil)
	require.Equal(t, 404, code)
	_, err = f.pool.Exec(ctx, "UPDATE artifact_handles SET expires_at=now()-interval '1 second' WHERE id=$1", handle.ID)
	require.NoError(t, err)
	code, _ = f.request(t, "alice", "GET", handle.ContentPath, "", nil)
	require.Equal(t, 404, code)
}
