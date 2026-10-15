package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/complynx/loom/internal/artifacts"
)

func blobDescriptor(body string) artifacts.Descriptor {
	hash := sha256.Sum256([]byte(body))
	return artifacts.Descriptor{
		Path:   "/blobs/object",
		MIME:   "text/plain",
		Size:   int64(len(body)),
		SHA256: hex.EncodeToString(hash[:]),
	}
}
func blobSource(t *testing.T) (artifacts.Source, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/blobs/object" || r.Header.Get("Authorization") != "Bearer delegated-user" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello")
	}))
	t.Cleanup(server.Close)
	return artifacts.Source{
		ServiceID:        "trusted-service",
		BaseURL:          server.URL,
		Client:           server.Client(),
		Headers:          http.Header{"Authorization": {"Bearer delegated-user"}},
		AllowedMIMETypes: []string{"text/plain"},
		Authorize:        func(context.Context) error { return nil },
	}, calls
}
func TestServiceBlobImportProducesOwnedReadableHandle(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 100, 200)
	source, calls := blobSource(t)
	a, err := s.Import(context.Background(), testOwner(), source, blobDescriptor("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || a.OwnerID != testOwner().OwnerID || a.OrgID != testOwner().OrgID ||
		a.SourceServiceID != "trusted-service" ||
		a.ExternalDisclosureAllowed {
		t.Fatalf("import metadata: %+v calls=%d", a, calls.Load())
	}
	_, r, err := s.Download(context.Background(), testOwner(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	body, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(body, []byte("hello")) {
		t.Fatalf("body=%q err=%v", body, err)
	}
}
func TestServiceBlobImportRejectsOriginOverrideAndTraversal(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 100, 200)
	source, calls := blobSource(t)
	for _, path := range []string{"https://attacker.invalid/file", "//attacker.invalid/file", "/blobs/../secret", "/blobs/%2e%2e/secret", "/blobs/%252e%252e/secret", "/blobs\\secret", "/blobs/object?token=secret"} {
		descriptor := blobDescriptor("hello")
		descriptor.Path = path
		if _, importErr := s.Import(
			context.Background(),
			testOwner(),
			source,
			descriptor,
		); !errors.Is(
			importErr,
			artifacts.ErrInvalid,
		) {
			t.Fatalf("accepted %q: %v", path, importErr)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid descriptor reached source: %d", calls.Load())
	}
}
func TestServiceBlobImportDoesNotFollowRedirectOrLeakCredentials(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 100, 200)
	var leaked atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		t.Errorf("unexpected external request with authorization %q", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	origin := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) },
		),
	)
	defer origin.Close()
	source := artifacts.Source{
		ServiceID:        "trusted-service",
		BaseURL:          origin.URL,
		Client:           origin.Client(),
		Headers:          http.Header{"Authorization": {"Bearer secret"}},
		AllowedMIMETypes: []string{"text/plain"},
		Authorize:        func(context.Context) error { return nil },
	}
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		blobDescriptor("hello"),
	); !errors.Is(
		importErr,
		artifacts.ErrInvalid,
	) {
		t.Fatalf("redirect accepted: %v", importErr)
	}
	if leaked.Load() != 0 {
		t.Fatal("credentials followed redirect")
	}
}
func TestServiceBlobImportRejectsRevocationAndChangedContent(t *testing.T) {
	t.Parallel()
	s, store := service(t, 100, 200)
	source, calls := blobSource(t)
	source.Authorize = func(context.Context) error { return artifacts.ErrForbidden }
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		blobDescriptor("hello"),
	); !errors.Is(
		importErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("initial revocation: %v", importErr)
	}
	if calls.Load() != 0 {
		t.Fatal("revoked request fetched content")
	}
	var checks int
	source.Authorize = func(context.Context) error {
		checks++
		if checks > 1 {
			return artifacts.ErrForbidden
		}
		return nil
	}
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		blobDescriptor("hello"),
	); !errors.Is(
		importErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("late revocation: %v", importErr)
	}
	if len(store.rows) != 0 {
		t.Fatal("revoked content committed")
	}
	source.Authorize = func(context.Context) error { return nil }
	wrong := blobDescriptor("world")
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		wrong,
	); !errors.Is(
		importErr,
		artifacts.ErrInvalid,
	) {
		t.Fatalf("changed content: %v", importErr)
	}
	if len(store.rows) != 0 {
		t.Fatal("corrupt content committed")
	}
}
func TestServiceBlobImportHonorsMIMEAndQuota(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 100, 7)
	source, _ := blobSource(t)
	source.AllowedMIMETypes = []string{"image/png"}
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		blobDescriptor("hello"),
	); !errors.Is(
		importErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("unsupported MIME: %v", importErr)
	}
	source.AllowedMIMETypes = []string{"text/plain"}
	if _, uploadErr := s.Upload(
		context.Background(),
		testOwner(),
		"text/plain",
		strings.NewReader("123"),
	); uploadErr != nil {
		t.Fatal(uploadErr)
	}
	if _, importErr := s.Import(
		context.Background(),
		testOwner(),
		source,
		blobDescriptor("hello"),
	); importErr == nil {
		t.Fatal("import exceeded quota")
	}
}

func TestImportedDisclosurePolicyCannotBeOverriddenByGlobalAllowList(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 100, 200)
	source, _ := blobSource(t)
	a, err := s.Import(context.Background(), testOwner(), source, blobDescriptor("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if allowDisclosureErr := artifacts.AllowDisclosure(
		a,
		testOwner(),
		"openai",
		[]string{"openai"},
	); !errors.Is(
		allowDisclosureErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("restricted source disclosure allowed: %v", allowDisclosureErr)
	}
	source.ExternalDisclosureAllowed = true
	allowed, err := s.Import(context.Background(), testOwner(), source, blobDescriptor("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if allowDisclosureErr := artifacts.AllowDisclosure(
		allowed,
		testOwner(),
		"openai",
		[]string{"openai"},
	); allowDisclosureErr != nil {
		t.Fatalf("source explicitly allowed disclosure: %v", allowDisclosureErr)
	}
}
