package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type identityCacheReceipt struct {
	Generation int64 `json:"generation"`
}

func administrativeOrganizationRequired(path string) bool {
	return strings.HasPrefix(path, "/v1/admin/settings") || path == "/v1/admin/identity/cache/clear"
}

func validPrincipal(p domain.Principal) bool {
	return p.Subject != "" && p.OrgID != "" && (p.ExpiresAt.IsZero() || time.Now().Before(p.ExpiresAt))
}

// Sensitive writes need current identity-provider authority, even with a warm cache.
func freshAuthorization(pattern string) bool {
	if strings.HasPrefix(pattern, "GET ") || strings.HasPrefix(pattern, "DELETE /v1/auth/sessions/") {
		return false
	}
	return strings.Contains(pattern, " /v1/skills/") || strings.Contains(pattern, " /v1/admin/") ||
		strings.Contains(pattern, " /v1/auth/sessions") ||
		strings.HasSuffix(pattern, "/input") || strings.HasSuffix(pattern, "/resume")
}

func (s *server) clearIdentityCache(w http.ResponseWriter, r *http.Request, _ domain.Principal) error {
	if s.options.ClearIdentityCache == nil {
		return identity.ErrUnavailable
	}
	epoch, err := s.options.ClearIdentityCache(r.Context())
	if err != nil {
		return identity.ErrUnavailable
	}
	write(w, http.StatusOK, identityCacheReceipt{Generation: epoch})
	return nil
}
