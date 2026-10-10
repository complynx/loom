// Package artifacts stores immutable upload content on a persistent volume.
package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
)

var (
	ErrTooLarge  = errors.New("artifact exceeds upload limit")
	ErrNotFound  = errors.New("artifact not found")
	ErrForbidden = errors.New("artifact access denied")
	ErrInvalid   = errors.New("invalid artifact input")
)

type Principal struct{ OwnerID, OrgID string }
type Limits struct {
	MaxUploadBytes, QuotaBytes int64
	// MaxConcurrentUploads defaults to four, bounding transient upload storage.
	MaxConcurrentUploads int
}

// MetadataStore must enforce the quota atomically with insertion, including concurrent uploads.
type MetadataStore interface {
	CommitArtifact(context.Context, domain.Artifact, int64) error
	GetArtifact(context.Context, string) (domain.Artifact, error)
}

type Service struct {
	activity sync.RWMutex
	uploads  chan struct{}
	root     *os.Root
	store    MetadataStore
	limits   Limits
}

func New(path string, store MetadataStore, limits Limits) (*Service, error) {
	if limits.MaxConcurrentUploads == 0 {
		limits.MaxConcurrentUploads = 4
	}
	if limits.MaxConcurrentUploads < 1 || store == nil || limits.MaxUploadBytes <= 0 || limits.MaxUploadBytes == math.MaxInt64 || limits.QuotaBytes <= 0 {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("create artifact volume: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact volume: %w", err)
	}
	return &Service{root: root, store: store, limits: limits, uploads: make(chan struct{}, limits.MaxConcurrentUploads)}, nil
}

func (s *Service) Close() error { return s.root.Close() }

// Upload reads at most MaxUploadBytes+1 bytes. Callers must arrange to close a
// blocking input when its context is canceled (HTTP request bodies support this).
func (s *Service) Upload(ctx context.Context, p Principal, declaredMIME string, input io.Reader) (domain.Artifact, error) {
	return s.upload(ctx, p, declaredMIME, input, true, "")
}
func (s *Service) upload(ctx context.Context, p Principal, declaredMIME string, input io.Reader, externalDisclosure bool, sourceServiceID string) (domain.Artifact, error) {
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	case <-ctx.Done():
		return domain.Artifact{}, ctx.Err()
	}
	s.activity.RLock()
	defer s.activity.RUnlock()
	if p.OwnerID == "" || input == nil {
		return domain.Artifact{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, err
	}
	if declaredMIME != "" {
		mediaType, _, err := mime.ParseMediaType(declaredMIME)
		if err != nil {
			return domain.Artifact{}, ErrInvalid
		}
		declaredMIME = mediaType
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return domain.Artifact{}, err
	}
	id := hex.EncodeToString(random)
	tmp := id + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(tmp) }()
	hash := sha256.New()
	reader := &contextReader{ctx: ctx, reader: io.LimitReader(input, s.limits.MaxUploadBytes+1)}
	header := make([]byte, 512)
	n, err := io.ReadFull(reader, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return domain.Artifact{}, err
	}
	header = header[:n]
	w := io.MultiWriter(f, hash)
	if _, err := w.Write(header); err != nil {
		return domain.Artifact{}, err
	}
	rest, err := io.Copy(w, reader)
	if err != nil {
		return domain.Artifact{}, err
	}
	size := int64(n) + rest
	if size > s.limits.MaxUploadBytes {
		return domain.Artifact{}, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, err
	}
	if declaredMIME == "" {
		declaredMIME = http.DetectContentType(header)
	}
	if err := f.Sync(); err != nil {
		return domain.Artifact{}, err
	}
	if err := f.Close(); err != nil {
		return domain.Artifact{}, err
	}
	if err := s.root.Rename(tmp, id); err != nil {
		return domain.Artifact{}, err
	}
	if runtime.GOOS == "linux" {
		dir, err := s.root.Open(".")
		if err != nil {
			return domain.Artifact{}, err
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return domain.Artifact{}, syncErr
		}
		if closeErr != nil {
			return domain.Artifact{}, closeErr
		}
	}
	a := domain.Artifact{ID: id, OwnerID: p.OwnerID, OrgID: p.OrgID, MIME: declaredMIME, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, CreatedAt: time.Now().UTC(), ExternalDisclosureAllowed: externalDisclosure, SourceServiceID: sourceServiceID}
	if err := s.store.CommitArtifact(ctx, a, s.limits.QuotaBytes); err != nil {
		// A failed commit response can mean the transaction actually committed.
		// Retain content until a successful metadata lookup resolves that ambiguity.
		resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		existing, lookupErr := s.store.GetArtifact(resolveCtx, id)
		if lookupErr == nil && existing.ID == id {
			return existing, nil
		}
		if errors.Is(lookupErr, domain.ErrNotFound) {
			_ = s.root.Remove(id)
		}
		return domain.Artifact{}, err
	}
	return a, nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func (s *Service) Metadata(ctx context.Context, p Principal, id string) (domain.Artifact, error) {
	if !validID(id) {
		return domain.Artifact{}, ErrNotFound
	}
	a, err := s.store.GetArtifact(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Artifact{}, ErrNotFound
	}
	if err != nil {
		return domain.Artifact{}, err
	}
	if p.OwnerID == "" || a.OwnerID != p.OwnerID || a.OrgID != p.OrgID {
		return domain.Artifact{}, ErrForbidden
	}
	return a, nil
}

func (s *Service) Download(ctx context.Context, p Principal, id string) (domain.Artifact, io.ReadCloser, error) {
	a, err := s.Metadata(ctx, p, id)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	info, err := s.root.Lstat(id)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Size {
		return domain.Artifact{}, nil, ErrInvalid
	}
	f, err := s.root.Open(id)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	return a, &contextReadCloser{contextReader: contextReader{ctx: ctx, reader: f}, closer: f}, nil
}

// AllowDisclosure grants access only when this destination is explicitly allowed.
// The caller must additionally enforce model capabilities and per-run approval.
func AllowDisclosure(a domain.Artifact, p Principal, provider string, allowedProviders []string) error {
	if p.OwnerID == "" || a.OwnerID != p.OwnerID || a.OrgID != p.OrgID {
		return ErrForbidden
	}
	if !a.ExternalDisclosureAllowed {
		return ErrForbidden
	}
	for _, allowed := range allowedProviders {
		if provider != "" && provider == allowed {
			return nil
		}
	}
	return ErrForbidden
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextReadCloser struct {
	contextReader
	closer io.Closer
}

func (r *contextReadCloser) Close() error { return r.closer.Close() }

// Reconcile removes aged, definitively unreferenced content after a crash. Failed
// database reads leave files intact. Uploads are excluded for the reconciliation
// pass so a metadata lookup cannot race with an upload's commit.
func (s *Service) Reconcile(ctx context.Context, minAge time.Duration) (int, error) {
	if minAge < time.Hour {
		return 0, ErrInvalid
	}
	s.activity.Lock()
	defer s.activity.Unlock()
	dir, err := s.root.Open(".")
	if err != nil {
		return 0, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		name := entry.Name()
		id := strings.TrimSuffix(name, ".tmp")
		if !validID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, err
		}
		if !info.Mode().IsRegular() || time.Since(info.ModTime()) < minAge {
			continue
		}
		if name == id {
			_, err = s.store.GetArtifact(ctx, id)
			if err == nil {
				continue
			}
			if !errors.Is(err, domain.ErrNotFound) {
				return removed, err
			}
		}
		if err := s.root.Remove(name); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ModelArtifact contains verified image bytes for a provider adapter. Binary
// content is never forwarded unless the destination and capability are allowed.
type ModelArtifact struct {
	Metadata domain.Artifact
	Bytes    []byte
}

func (s *Service) ForModel(ctx context.Context, p Principal, id, provider string, allowedProviders []string, supportsImages bool, maxBytes int64) (ModelArtifact, error) {
	a, err := s.Metadata(ctx, p, id)
	if err != nil {
		return ModelArtifact{}, err
	}
	if err := AllowDisclosure(a, p, provider, allowedProviders); err != nil {
		return ModelArtifact{}, err
	}
	if !supportsImages || maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return ModelArtifact{}, ErrForbidden
	}
	if a.Size > maxBytes {
		return ModelArtifact{}, ErrTooLarge
	}
	_, r, err := s.Download(ctx, p, id)
	if err != nil {
		return ModelArtifact{}, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return ModelArtifact{}, err
	}
	if int64(len(data)) > maxBytes {
		return ModelArtifact{}, ErrTooLarge
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != a.SHA256 {
		return ModelArtifact{}, ErrInvalid
	}
	detected := http.DetectContentType(data)
	declared, _, err := mime.ParseMediaType(a.MIME)
	if err != nil || detected != declared {
		return ModelArtifact{}, ErrInvalid
	}
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return ModelArtifact{}, ErrInvalid
	}
	return ModelArtifact{Metadata: a, Bytes: data}, nil
}
