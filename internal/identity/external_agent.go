package identity

import (
	"context"
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

// AuthenticateExternal accepts only a user's own access token. It rejects delegation
// headers, service identities and service accounts. The returned principal keeps the
// user's module permissions and drops every Loom operation right: the caller grants
// those from the user's external agent profile.
func (a *Authenticator) AuthenticateExternal(ctx context.Context, r *http.Request) (domain.ExternalCaller, error) {
	token, err := bearer(r.Header.Get("Authorization"))
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if r.Header.Get("X-Subject-Token") != "" || r.Header.Get("X-Auth-Session-Id") != "" {
		return domain.ExternalCaller{}, ErrUnauthorized
	}
	p, info, err := a.authenticateClaims(ctx, token)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if p.ServiceAccount || p.OrgID == "" {
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
