package identity

import (
	"context"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/domain"
)

// AuthenticateOperator selects the configured admin organization for browser SSO.
// It derives permissions only from roles granted in that organization. Cached
// token claims stay unchanged for normal API authentication and delegation.
func (a *Authenticator) AuthenticateOperator(ctx context.Context, r *http.Request) (domain.Principal, error) {
	if a.cfg.AdminOrganizationID == "" || r.Header.Get("X-Subject-Token") != "" ||
		r.Header.Get("X-Auth-Session-Id") != "" {
		return domain.Principal{}, ErrUnauthorized
	}
	token, err := bearer(r.Header.Get("Authorization"))
	if err != nil {
		return domain.Principal{}, err
	}
	info, err := a.cache.load(ctx, token, a.introspectToken)
	if err != nil {
		return domain.Principal{}, err
	}
	p := domain.Principal{
		Issuer: info.Issuer, Subject: info.Subject, Actor: info.Subject,
		OrgID: info.OrgID, ExpiresAt: time.Unix(info.Expires, 0),
	}
	service, err := a.classifyService(ctx, p)
	if err != nil {
		return domain.Principal{}, err
	}
	if service {
		return domain.Principal{}, ErrUnauthorized
	}
	info.OrgID = a.cfg.AdminOrganizationID
	p.OrgID, p.Permissions = info.OrgID, a.rolePermissions(info)
	return p, nil
}
