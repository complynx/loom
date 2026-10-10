package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/complynx/clx-scripter/internal/domain"
)

// Descriptor identifies a blob at the same trusted service which returned it.
// Path is a decoded absolute path; signed external URLs are deliberately excluded.
type Descriptor struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Source is constructed by the host broker, never from model or module output.
// Headers carry delegated user credentials; Authorize rechecks the current
// service registration, user delegation and method permissions.
type Source struct {
	ServiceID                 string
	ExternalDisclosureAllowed bool
	BaseURL                   string
	Client                    *http.Client
	Headers                   http.Header
	AllowedMIMETypes          []string
	Authorize                 func(context.Context) error
}

// Import converts a bounded authenticated service blob into an owned immutable
// artifact. It rejects redirects, traversal, MIME mismatch, changed content and
// revoked delegation before any metadata is committed.
func (s *Service) Import(ctx context.Context, p Principal, source Source, descriptor Descriptor) (domain.Artifact, error) {
	if source.ServiceID == "" {
		return domain.Artifact{}, ErrInvalid
	}
	if source.Client == nil || source.Authorize == nil {
		return domain.Artifact{}, ErrForbidden
	}
	base, err := url.Parse(source.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return domain.Artifact{}, ErrInvalid
	}
	if !safeBlobPath(descriptor.Path) || descriptor.Size < 0 || descriptor.Size > s.limits.MaxUploadBytes {
		return domain.Artifact{}, ErrInvalid
	}
	digest, err := hex.DecodeString(descriptor.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return domain.Artifact{}, ErrInvalid
	}
	contentType, _, err := mime.ParseMediaType(descriptor.MIME)
	if err != nil {
		return domain.Artifact{}, ErrInvalid
	}
	allowed := false
	for _, candidate := range source.AllowedMIMETypes {
		if candidate == contentType {
			allowed = true
		}
	}
	if !allowed {
		return domain.Artifact{}, ErrForbidden
	}
	if err := source.Authorize(ctx); err != nil {
		return domain.Artifact{}, err
	}
	endpoint := *base
	endpoint.Path = descriptor.Path
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return domain.Artifact{}, err
	}
	request.Header = source.Headers.Clone()
	// A local copy keeps the registry's shared client immutable. Credentials must
	// never follow even a same-origin redirect to a different privileged endpoint.
	client := *source.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return domain.Artifact{}, ErrInvalid
	}
	actualMIME, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || actualMIME != contentType {
		return domain.Artifact{}, ErrInvalid
	}
	if response.ContentLength >= 0 && response.ContentLength != descriptor.Size {
		return domain.Artifact{}, ErrInvalid
	}
	reader := &verifiedBlobReader{ctx: ctx, reader: response.Body, expectedSize: descriptor.Size, expectedDigest: strings.ToLower(descriptor.SHA256), hash: sha256.New(), authorize: source.Authorize}
	return s.upload(ctx, p, contentType, reader, source.ExternalDisclosureAllowed, source.ServiceID)
}

func safeBlobPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\%?#:") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return !strings.ContainsAny(path, "\r\n\x00")
}

type verifiedBlobReader struct {
	ctx            context.Context
	reader         io.Reader
	expectedSize   int64
	expectedDigest string
	hash           hash.Hash
	size           int64
	authorize      func(context.Context) error
}

func (r *verifiedBlobReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.size += int64(n)
		_, _ = r.hash.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if r.size != r.expectedSize || hex.EncodeToString(r.hash.Sum(nil)) != r.expectedDigest {
			return n, ErrInvalid
		}
		if authErr := r.authorize(r.ctx); authErr != nil {
			return n, authErr
		}
	}
	return n, err
}
