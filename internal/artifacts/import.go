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
	"slices"
	"strings"

	"github.com/complynx/loom/internal/domain"
)

// Descriptor identifies a blob at the same trusted service which returned it.
// Path is a decoded absolute path; signed external URLs are deliberately excluded.
type Descriptor struct {
	ExternalID string `json:"external_id,omitempty"`
	Path       string `json:"path"`
	MIME       string `json:"mime"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
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
func (s *Service) Import(
	ctx context.Context,
	p Principal,
	source Source,
	descriptor Descriptor,
) (domain.Artifact, error) {
	return s.importWithCommit(ctx, p, source, descriptor, s.store.CommitArtifact)
}

// ImportToChat commits imported metadata and its required chat reference together.
// Stores without atomic chat attachment cannot accept this import.
func (s *Service) ImportToChat(
	ctx context.Context,
	p domain.Principal,
	ownerID, chatID string,
	source Source,
	descriptor Descriptor,
) (domain.Artifact, error) {
	store, ok := s.store.(interface {
		CommitChatArtifact(context.Context, domain.Principal, string, domain.Artifact, int64) error
	})
	if !ok {
		return domain.Artifact{}, ErrInvalid
	}
	return s.importWithCommit(ctx, Principal{OwnerID: ownerID, OrgID: p.OrgID}, source, descriptor,
		func(ctx context.Context, item domain.Artifact, quota int64) error {
			return store.CommitChatArtifact(ctx, p, chatID, item, quota)
		})
}

func (s *Service) importWithCommit(
	ctx context.Context,
	p Principal,
	source Source,
	descriptor Descriptor,
	commit func(context.Context, domain.Artifact, int64) error,
) (domain.Artifact, error) {
	if !validExternalSource(source.ServiceID, descriptor.ExternalID) {
		return domain.Artifact{}, ErrInvalid
	}
	if source.Client == nil || source.Authorize == nil {
		return domain.Artifact{}, ErrForbidden
	}
	base, err := url.Parse(source.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil ||
		base.RawQuery != "" ||
		base.Fragment != "" {
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
	if !slices.Contains(source.AllowedMIMETypes, contentType) {
		return domain.Artifact{}, ErrForbidden
	}
	if authorizeErr := source.Authorize(ctx); authorizeErr != nil {
		return domain.Artifact{}, authorizeErr
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
	reader := &verifiedBlobReader{
		ctx:            ctx,
		reader:         response.Body,
		expectedSize:   descriptor.Size,
		expectedDigest: strings.ToLower(descriptor.SHA256),
		hash:           sha256.New(),
		authorize:      source.Authorize,
	}
	item, err := s.upload(ctx, p, contentType, reader, source.ExternalDisclosureAllowed, source.ServiceID,
		func(c context.Context, a domain.Artifact, quota int64) error {
			a.SourceExternalID = descriptor.ExternalID
			return commit(c, a, quota)
		})
	item.SourceExternalID = descriptor.ExternalID
	return item, err
}

func safeBlobPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\%?#:") {
		return false
	}
	for segment := range strings.SplitSeq(path, "/") {
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
	if contextErr := r.ctx.Err(); contextErr != nil {
		return 0, contextErr
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

func validExternalSource(serviceID, externalID string) bool {
	return serviceID != "" && len(externalID) <= 1024
}
