package integration_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

const rangedArtifactText = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestArtifactRangeAndConditionalDelivery(t *testing.T) {
	t.Parallel()
	f, item, handle := rangeFixture(t)
	for _, path := range []string{"/v1/artifacts/" + item.ID + "/content", handle.ContentPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			for _, test := range artifactRangeCases(`"` + item.SHA256 + `"`) {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()
					assertRangeCase(t, f, path, test)
				})
			}
		})
	}
}

func TestExpiredArtifactHandleIgnoresRangeAndETag(t *testing.T) {
	t.Parallel()
	f, item, handle := rangeFixture(t)
	_, err := f.pool.Exec(
		t.Context(),
		"UPDATE artifact_handles SET expires_at=now()-interval '1 second' WHERE id=$1",
		handle.ID,
	)
	require.NoError(t, err)
	response, _ := rangeRequest(
		t,
		f,
		"GET",
		handle.ContentPath,
		"alice",
		map[string]string{"Range": "bytes=5-9", "If-None-Match": `"` + item.SHA256 + `"`},
	)
	require.Equal(t, 404, response.StatusCode)
}

func rangeFixture(t *testing.T) (*fixture, domain.Artifact, domain.ArtifactHandle) {
	t.Helper()
	f := newFixture(t)
	request := httptest.NewRequest(http.MethodGet, "http://test", nil)
	request.Header.Set("Authorization", "Bearer alice")
	p, err := (artifactTestAuth{}).Authenticate(t.Context(), request)
	require.NoError(t, err)
	uid, err := f.repository.ResolveUser(t.Context(), p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), f.repository, artifacts.Limits{MaxUploadBytes: 100, QuotaBytes: 100})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	item, err := files.Upload(
		t.Context(),
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"text/plain",
		bytes.NewBufferString(rangedArtifactText),
	)
	require.NoError(t, err)
	run := f.createRun(t, runRequest("", "", "range read"), "range-run")
	f.server.Close()
	f.server = httptest.NewServer(api.New(f.repository, artifactTestAuth{}, api.Options{Artifacts: files}))
	t.Cleanup(f.server.Close)
	status, body := f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/artifacts/handles",
		"",
		map[string]string{"artifact_id": item.ID},
	)
	require.Equal(t, 201, status, string(body))
	return f, item, decode[domain.ArtifactHandle](t, body)
}

func rangeRequest(
	t *testing.T,
	f *fixture,
	method, path, actor string,
	headers map[string]string,
) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+actor)
	for name, value := range headers {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return response, body
}

func TestArtifactMultipartRanges(t *testing.T) {
	t.Parallel()
	f, _, handle := rangeFixture(t)
	response, body := rangeRequest(
		t,
		f,
		"GET",
		handle.ContentPath,
		"alice",
		map[string]string{"Range": "bytes=0-0,35-35"},
	)
	require.Equal(t, http.StatusPartialContent, response.StatusCode)
	kind, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/byteranges", kind)
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for _, expected := range []string{"0", "Z"} {
		part, partErr := reader.NextPart()
		require.NoError(t, partErr)
		content, readErr := io.ReadAll(part)
		require.NoError(t, readErr)
		require.Equal(t, expected, string(content))
		require.NoError(t, part.Close())
	}
	_, err = reader.NextPart()
	require.ErrorIs(t, err, io.EOF)
}

type artifactRangeCase struct {
	name, method, actor, rangeHeader, condition, value, body string
	status                                                   int
}

func artifactRangeCases(etag string) []artifactRangeCase {
	return []artifactRangeCase{
		{name: "full", method: "GET", actor: "alice", body: rangedArtifactText, status: 200},
		{name: "middle", method: "GET", actor: "alice", rangeHeader: "bytes=5-9", body: "56789", status: 206},
		{name: "suffix", method: "GET", actor: "alice", rangeHeader: "bytes=-4", body: "WXYZ", status: 206},
		{name: "open-end", method: "GET", actor: "alice", rangeHeader: "bytes=32-", body: "WXYZ", status: 206},
		{name: "head", method: "HEAD", actor: "alice", status: 200},
		{name: "head-range", method: "HEAD", actor: "alice", rangeHeader: "bytes=5-9", status: 200},
		{name: "head-invalid-range", method: "HEAD", actor: "alice", rangeHeader: "bytes=9-5", status: 200},
		{name: "matching-etag", method: "GET", actor: "alice", condition: "If-None-Match", value: etag, status: 304},
		{
			name:        "matching-if-range",
			method:      "GET",
			actor:       "alice",
			rangeHeader: "bytes=5-9",
			condition:   "If-Range",
			value:       etag,
			body:        "56789",
			status:      206,
		},
		{
			name:        "stale-if-range",
			method:      "GET",
			actor:       "alice",
			rangeHeader: "bytes=5-9",
			condition:   "If-Range",
			value:       `"stale"`,
			body:        rangedArtifactText,
			status:      200,
		},
		{
			name:      "failed-precondition",
			method:    "GET",
			actor:     "alice",
			condition: "If-Match",
			value:     `"stale"`,
			status:    412,
		},
		{name: "outside", method: "GET", actor: "alice", rangeHeader: "bytes=99-", status: 416},
		{name: "backwards", method: "GET", actor: "alice", rangeHeader: "bytes=9-5", status: 416},
		{name: "foreign-range", method: "GET", actor: "bob", rangeHeader: "bytes=5-9", status: 404},
		{name: "foreign-etag", method: "GET", actor: "bob", condition: "If-None-Match", value: etag, status: 404},
	}
}

func assertRangeCase(t *testing.T, f *fixture, path string, test artifactRangeCase) {
	t.Helper()
	headers := map[string]string{"Range": test.rangeHeader}
	if test.condition != "" {
		headers[test.condition] = test.value
	}
	response, body := rangeRequest(t, f, test.method, path, test.actor, headers)
	require.Equal(t, test.status, response.StatusCode, string(body))
	if test.status < 400 {
		require.Equal(t, test.body, string(body))
	}
	if test.status == 206 {
		require.Equal(t, "bytes", response.Header.Get("Accept-Ranges"))
		require.NotEmpty(t, response.Header.Get("Content-Range"))
	}
	if test.name == "outside" {
		require.Equal(t, "bytes */36", response.Header.Get("Content-Range"))
	}
	if test.method == "HEAD" {
		require.Equal(t, strconv.Itoa(len(rangedArtifactText)), response.Header.Get("Content-Length"))
	}
}
