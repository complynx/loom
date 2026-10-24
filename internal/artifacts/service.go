// Package artifacts stores immutable content temporarily on a disk volume.
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

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/mediaimage"
)

const (
	artifactRandomBytes = 16
	artifactFileMode    = 0o600
	contentSniffBytes   = 512
	reconcileBatchSize  = 256
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
	TTL                  time.Duration
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
	scanDir  *os.File
	pending  os.DirEntry
}

func New(path string, store MetadataStore, limits Limits) (*Service, error) {
	if limits.TTL == 0 {
		limits.TTL = domain.DefaultArtifactTTL
	}
	if limits.TTL < time.Second || limits.TTL > 7*24*time.Hour {
		return nil, ErrInvalid
	}
	if limits.MaxConcurrentUploads == 0 {
		limits.MaxConcurrentUploads = 4
	}
	if limits.MaxConcurrentUploads < 1 || store == nil || limits.MaxUploadBytes <= 0 ||
		limits.MaxUploadBytes == math.MaxInt64 ||
		limits.QuotaBytes <= 0 {
		return nil, ErrInvalid
	}
	if mkdirAllErr := os.MkdirAll(path, 0700); mkdirAllErr != nil {
		return nil, fmt.Errorf("create artifact volume: %w", mkdirAllErr)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact volume: %w", err)
	}
	return &Service{
		root:    root,
		store:   store,
		limits:  limits,
		uploads: make(chan struct{}, limits.MaxConcurrentUploads),
	}, nil
}

func (s *Service) Close() error {
	s.activity.Lock()
	defer s.activity.Unlock()
	return errors.Join(s.closeScan(), s.root.Close())
}

func (s *Service) closeScan() error {
	dir := s.scanDir
	s.scanDir = nil
	s.pending = nil
	if dir == nil {
		return nil
	}
	return dir.Close()
}

// Upload reads at most MaxUploadBytes+1 bytes. Callers must arrange to close a
// blocking input when its context is canceled (HTTP request bodies support this).
func (s *Service) Upload(
	ctx context.Context,
	p Principal,
	declaredMIME string,
	input io.Reader,
) (domain.Artifact, error) {
	return s.upload(ctx, p, declaredMIME, input, true, "", s.store.CommitArtifact)
}

func (s *Service) upload(
	ctx context.Context,
	p Principal,
	declaredMIME string,
	input io.Reader,
	externalDisclosure bool,
	sourceServiceID string,
	commit func(context.Context, domain.Artifact, int64) error,
) (domain.Artifact, error) {
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
	if contextErr := ctx.Err(); contextErr != nil {
		return domain.Artifact{}, contextErr
	}
	if declaredMIME != "" {
		mediaType, _, parseMediaTypeErr := mime.ParseMediaType(declaredMIME)
		if parseMediaTypeErr != nil {
			return domain.Artifact{}, ErrInvalid
		}
		declaredMIME = mediaType
	}
	random := make([]byte, artifactRandomBytes)
	if _, readErr := rand.Read(random); readErr != nil {
		return domain.Artifact{}, readErr
	}
	id := hex.EncodeToString(random)
	tmp := id + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, artifactFileMode)
	if err != nil {
		return domain.Artifact{}, err
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(tmp) }()
	hash := sha256.New()
	size, header, err := copyUpload(ctx, f, hash, input, s.limits.MaxUploadBytes)
	if err != nil {
		return domain.Artifact{}, err
	}
	if size > s.limits.MaxUploadBytes {
		return domain.Artifact{}, ErrTooLarge
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return domain.Artifact{}, contextErr
	}
	if declaredMIME == "" {
		declaredMIME = http.DetectContentType(header)
	}
	if syncErr := f.Sync(); syncErr != nil {
		return domain.Artifact{}, syncErr
	}
	if closeErr := f.Close(); closeErr != nil {
		return domain.Artifact{}, closeErr
	}
	if renameErr := s.root.Rename(tmp, id); renameErr != nil {
		return domain.Artifact{}, renameErr
	}
	if directorySyncErr := s.syncDirectory(); directorySyncErr != nil {
		return domain.Artifact{}, directorySyncErr
	}

	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	a := domain.Artifact{
		ID:                        id,
		OwnerID:                   p.OwnerID,
		OrgID:                     p.OrgID,
		MIME:                      declaredMIME,
		SHA256:                    hex.EncodeToString(hash.Sum(nil)),
		Size:                      size,
		CreatedAt:                 createdAt,
		ExternalDisclosureAllowed: externalDisclosure,
		SourceServiceID:           sourceServiceID,
		ExpiresAt:                 createdAt.Add(s.limits.TTL),
	}
	return s.commitUpload(ctx, a, commit)
}

func (s *Service) commitUpload(
	ctx context.Context,
	a domain.Artifact,
	commit func(context.Context, domain.Artifact, int64) error,
) (domain.Artifact, error) {
	if commitArtifactErr := commit(ctx, a, s.limits.QuotaBytes); commitArtifactErr != nil {
		// A failed commit response can mean the transaction actually committed.
		// Retain content until a successful metadata lookup resolves that ambiguity.
		resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		existing, lookupErr := s.store.GetArtifact(resolveCtx, a.ID)
		existingID, valid := compactArtifactID(existing.ID)
		if lookupErr == nil && valid && existingID == a.ID {
			existing.ID = a.ID
			return existing, nil
		}
		if errors.Is(lookupErr, domain.ErrNotFound) {
			_ = s.root.Remove(a.ID)
		}
		return domain.Artifact{}, commitArtifactErr
	}
	return a, nil
}

func validID(id string) bool {
	if len(id) != artifactRandomBytes*2 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// PostgreSQL represents artifact UUIDs with separators; filenames use compact IDs.
func compactArtifactID(id string) (string, bool) {
	if validID(id) {
		return id, true
	}
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return "", false
	}
	compact := strings.ReplaceAll(id, "-", "")
	return compact, validID(compact)
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
	if !artifactExpiry(a).After(time.Now()) {
		return domain.Artifact{}, ErrNotFound
	}
	storedID, valid := compactArtifactID(a.ID)
	if !valid || storedID != id {
		return domain.Artifact{}, ErrInvalid
	}
	a.ID = id
	return a, nil
}

func artifactExpiry(a domain.Artifact) time.Time {
	if a.ExpiresAt.IsZero() {
		return a.CreatedAt.Add(domain.DefaultArtifactTTL)
	}
	return a.ExpiresAt
}

func (s *Service) Download(ctx context.Context, p Principal, id string) (domain.Artifact, io.ReadSeekCloser, error) {
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
	return a, &contextReadSeekCloser{
		ctx: ctx, reader: f, closer: f, seeker: f, expiresAt: artifactExpiry(a),
	}, nil
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
	ctx       context.Context
	reader    io.Reader
	expiresAt time.Time
}

func (r *contextReader) Read(p []byte) (int, error) {
	if !r.expiresAt.IsZero() && !r.expiresAt.After(time.Now()) {
		return 0, ErrNotFound
	}
	if contextErr := r.ctx.Err(); contextErr != nil {
		return 0, contextErr
	}
	return r.reader.Read(p)
}

type contextReadCloser struct {
	contextReader

	closer io.Closer
}

func (r *contextReadCloser) Close() error { return r.closer.Close() }

type contextReadSeekCloser struct {
	contextReadCloser

	seeker io.Seeker
}

func (r *contextReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.seeker.Seek(offset, whence)
}

// Reconcile removes aged, definitively unreferenced content after a crash. Each
// pass scans at most 256 entries and keeps its directory position for the next
// pass. Failed reads retain the current entry for retry. Uploads are excluded
// during each pass so metadata lookup cannot race with an upload's commit.
func (s *Service) Reconcile(ctx context.Context, minAge time.Duration) (int, error) {
	if minAge < time.Hour {
		return 0, ErrInvalid
	}
	s.activity.Lock()
	defer s.activity.Unlock()
	removed := 0
	for range reconcileBatchSize {
		if contextErr := ctx.Err(); contextErr != nil {
			return removed, contextErr
		}
		if s.pending == nil {
			if nextErr := s.nextScanEntry(); nextErr != nil {
				if errors.Is(nextErr, io.EOF) {
					return removed, nil
				}
				return removed, nextErr
			}
		}
		didRemove, reconcileErr := s.reconcileEntry(ctx, s.pending, minAge)
		if reconcileErr != nil {
			return removed, reconcileErr
		}
		s.pending = nil
		if didRemove {
			removed++
		}
	}
	return removed, nil
}

func (s *Service) nextScanEntry() error {
	if s.scanDir == nil {
		dir, err := s.root.Open(".")
		if err != nil {
			return err
		}
		s.scanDir = dir
	}
	entries, err := s.scanDir.ReadDir(1)
	if len(entries) != 0 {
		s.pending = entries[0]
		return nil
	}
	if err != nil {
		if closeErr := s.closeScan(); closeErr != nil {
			return closeErr
		}
		return err
	}
	return nil
}

func (s *Service) reconcileEntry(ctx context.Context, entry os.DirEntry, minAge time.Duration) (bool, error) {
	name := entry.Name()
	id := strings.TrimSuffix(name, ".tmp")
	if !validID(id) {
		return false, nil
	}
	info, err := entry.Info()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || time.Since(info.ModTime()) < minAge {
		return false, nil
	}
	if name == id {
		_, err = s.store.GetArtifact(ctx, id)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return false, err
		}
	}
	if removeErr := s.root.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return false, removeErr
	}
	return true, nil
}

// ModelArtifact contains verified image bytes for a provider adapter. Binary
// content is never forwarded unless the destination and capability are allowed.
type ModelArtifact struct {
	Metadata domain.Artifact
	Bytes    []byte
}

func (s *Service) ForModel(
	ctx context.Context,
	p Principal,
	id, provider string,
	allowedProviders []string,
	supportsImages bool,
	maxBytes int64,
) (ModelArtifact, error) {
	a, err := s.Metadata(ctx, p, id)
	if err != nil {
		return ModelArtifact{}, err
	}
	if allowDisclosureErr := AllowDisclosure(a, p, provider, allowedProviders); allowDisclosureErr != nil {
		return ModelArtifact{}, allowDisclosureErr
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
	animated, err := mediaimage.Animated(data)
	if err != nil {
		return ModelArtifact{}, ErrInvalid
	}
	if animated {
		return ModelArtifact{}, mediaimage.ErrAnimated
	}
	return ModelArtifact{Metadata: a, Bytes: data}, nil
}

func (s *Service) syncDirectory() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func copyUpload(
	ctx context.Context,
	file io.Writer,
	digest io.Writer,
	input io.Reader,
	maxBytes int64,
) (int64, []byte, error) {
	reader := &contextReader{ctx: ctx, reader: io.LimitReader(input, maxBytes+1)}
	header := make([]byte, contentSniffBytes)
	n, err := io.ReadFull(reader, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, nil, err
	}
	header = header[:n]
	writer := io.MultiWriter(file, digest)
	if _, writeErr := writer.Write(header); writeErr != nil {
		return 0, nil, writeErr
	}
	rest, err := io.Copy(writer, reader)
	if err != nil {
		return 0, nil, err
	}
	return int64(n) + rest, header, nil
}
