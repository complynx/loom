package identity

import (
	"context"
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

// AuthenticateExternal accepts only a user's own access token. It rejects delegation
// headers, service identities and service accounts. See VerifyExternal.
func (a *Authenticator) AuthenticateExternal(ctx context.Context, r *http.Request) (domain.ExternalCaller, error) {
	token, err := ExternalBearer(r)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	return a.VerifyExternal(ctx, token)
}

// ExternalBearer reads a direct access token and rejects delegated identity headers.
func ExternalBearer(r *http.Request) (string, error) {
	if r.Header.Get("X-Subject-Token") != "" || r.Header.Get("X-Auth-Session-Id") != "" {
		return "", ErrUnauthorized
	}
	return bearer(r.Header.Get("Authorization"))
}

// VerifyExternal checks a user's own access token with the positive claims cache.
// The returned principal keeps the user's module permissions and drops every Loom
// operation right: the caller grants those from the user's external agent profile.
// Call it again before each use of a held token, so revocation and role changes
// apply within the cache policy.
func (a *Authenticator) VerifyExternal(ctx context.Context, token string) (domain.ExternalCaller, error) {
	p, info, err := a.authenticateClaims(ctx, token)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if p.ServiceAccount || p.OrgID == "" || info.ClientID == "" {
		return domain.ExternalCaller{}, ErrUnauthorized
	}
	service, err := a.classifyService(ctx, p)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if service {
		return domain.ExternalCaller{}, ErrUnauthorized
	}
	permissions := map[string]bool{}
	for permission, enabled := range p.Permissions {
		if enabled && !harnessPermission(permission) {
			permissions[permission] = true
		}
	}
	p.Permissions = permissions
	return domain.ExternalCaller{Principal: p, ClientID: info.ClientID, Token: token}, nil
}
