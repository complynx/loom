package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/domain"
)

func (s *server) artifactPrincipal(ctx context.Context, p domain.Principal) (artifacts.Principal, error) {
	uid, err := s.store.ResolveUser(ctx, p)
	return artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, err
}
func artifactError(err error) error {
	switch {
	case errors.Is(err, artifacts.ErrTooLarge):
		return apiError(413, "artifact_too_large", "Artifact exceeds the upload limit")
	case errors.Is(err, artifacts.ErrNotFound), errors.Is(err, artifacts.ErrForbidden):
		return apiError(404, "not_found", "Artifact is unavailable")
	case errors.Is(err, artifacts.ErrInvalid):
		return apiError(422, "invalid_artifact", "Artifact does not match the contract")
	default:
		return err
	}
}
func (s *server) uploadArtifact(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(503, "dependency_unavailable", "Artifact storage is unavailable")
	}
	owner, err := s.artifactPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return apiError(408, "request_timeout", "Upload could not be read")
	}
	stop := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stop()
	artifact, err := s.options.Artifacts.Upload(r.Context(), owner, r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		return artifactError(err)
	}
	write(w, 201, artifact)
	return nil
}
func (s *server) artifactMetadata(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(503, "dependency_unavailable", "Artifact storage is unavailable")
	}
	owner, err := s.artifactPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	artifact, err := s.options.Artifacts.Metadata(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		return artifactError(err)
	}
	write(w, 200, artifact)
	return nil
}
func (s *server) artifactContent(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(503, "dependency_unavailable", "Artifact storage is unavailable")
	}
	owner, err := s.artifactPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	artifact, reader, err := s.options.Artifacts.Download(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		return artifactError(err)
	}
	defer func() { _ = reader.Close() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()
	// Long downloads stop when authorization expires or is revoked.
	go func() {
		ticker := time.NewTicker(s.options.ReauthorizeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fresh, err := s.auth.Authenticate(ctx, r)
				if err != nil || fresh.Issuer != p.Issuer || fresh.Subject != p.Subject || fresh.Actor != p.Actor || fresh.OrgID != p.OrgID || !fresh.Has("artifacts.read") || (!fresh.ExpiresAt.IsZero() && !time.Now().Before(fresh.ExpiresAt)) {
					cancel()
					return
				}
			}
		}
	}()
	w.Header().Set("Content-Type", artifact.MIME)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="artifact"`)
	w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	_, _ = io.Copy(w, reader)
	return nil
}
