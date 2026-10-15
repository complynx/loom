package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

const artifactTransferTimeout = 30 * time.Second

func (s *server) artifactPrincipal(ctx context.Context, p domain.Principal) (artifacts.Principal, error) {
	uid, err := s.store.ResolveUser(ctx, p)
	return artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, err
}
func artifactError(err error) error {
	switch {
	case errors.Is(err, artifacts.ErrTooLarge):
		return apiError(http.StatusRequestEntityTooLarge, "artifact_too_large", "Artifact exceeds the upload limit")
	case errors.Is(err, artifacts.ErrNotFound), errors.Is(err, artifacts.ErrForbidden):
		return apiError(http.StatusNotFound, "not_found", "Artifact is unavailable")
	case errors.Is(err, artifacts.ErrInvalid):
		return apiError(http.StatusUnprocessableEntity, "invalid_artifact", "Artifact does not match the contract")
	default:
		return err
	}
}
func (s *server) uploadArtifact(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Artifact storage is unavailable")
	}
	owner, err := s.artifactPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	if deadlineErr := controller.SetReadDeadline(
		time.Now().Add(artifactTransferTimeout),
	); deadlineErr != nil &&
		!errors.Is(deadlineErr, http.ErrNotSupported) {
		return apiError(http.StatusRequestTimeout, "request_timeout", "Upload could not be read")
	}
	stop := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stop()
	artifact, err := s.options.Artifacts.Upload(r.Context(), owner, r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		return artifactError(err)
	}
	write(w, http.StatusCreated, artifact)
	return nil
}
func (s *server) artifactMetadata(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Artifact storage is unavailable")
	}
	owner, err := s.artifactPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	artifact, err := s.options.Artifacts.Metadata(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		return artifactError(err)
	}
	write(w, http.StatusOK, artifact)
	return nil
}
func (s *server) artifactContent(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if s.options.Artifacts == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Artifact storage is unavailable")
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
				fresh, authErr := s.auth.Authenticate(ctx, r)
				if authErr != nil || fresh.Issuer != p.Issuer || fresh.Subject != p.Subject || fresh.Actor != p.Actor ||
					fresh.OrgID != p.OrgID ||
					!fresh.Has("artifacts.read") ||
					(!fresh.ExpiresAt.IsZero() && !time.Now().Before(fresh.ExpiresAt)) {
					cancel()
					return
				}
			}
		}
	}()
	w.Header().Set("Content-Type", artifact.MIME)
	w.Header().Set("Content-Disposition", `attachment; filename="artifact"`)
	w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	controller := http.NewResponseController(w)
	if deadlineErr := controller.SetWriteDeadline(
		time.Now().Add(artifactTransferTimeout),
	); deadlineErr != nil &&
		!errors.Is(deadlineErr, http.ErrNotSupported) {
		return nil
	}
	request := r.WithContext(ctx)
	if request.Method == http.MethodHead {
		request.Header = request.Header.Clone()
		request.Header.Del("Range")
		request.Header.Del("If-Range")
	}
	http.ServeContent(w, request, "artifact", artifact.CreatedAt, reader)
	return nil
}
