package app

import (
	"context"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/mcpoauth"
)

func (r *runtimeComponents) initExternalBroker(cfg config.Config) error {
	settings := cfg.ExternalAgents.OAuthBroker
	if settings == nil {
		return nil
	}
	issuer, service := cfg.Identity.Issuer, settings.ConfirmationService
	broker, err := mcpoauth.New(mcpoauth.Options{
		Resource: cfg.ExternalAgents.Resource, UpstreamIssuer: issuer,
		ClientID: settings.ClientID, ClientSecret: settings.ClientSecret, Key: settings.KeyBytes(),
		Scopes: cfg.ExternalAgents.Scopes,
		Identify: func(ctx context.Context, token string) (mcpoauth.Owner, error) {
			caller, err := r.externalAuth.VerifyExternal(ctx, token)
			if err != nil {
				return mcpoauth.Owner{}, err
			}
			if caller.ClientID != settings.ClientID {
				return mcpoauth.Owner{}, identity.ErrUnauthorized
			}
			p := caller.Principal
			return mcpoauth.Owner{Issuer: p.Issuer, Subject: p.Subject, Org: p.OrgID}, nil
		},
		Authorize: func(ctx context.Context, owner mcpoauth.Owner, id, client, name string, expiry time.Time) error {
			p := domain.Principal{Issuer: owner.Issuer, Subject: owner.Subject, OrgID: owner.Org}
			return r.repository.CreateExternalAgentAuthorization(ctx, p, id, client, name, expiry)
		},
		CheckAuthorization: func(ctx context.Context, owner mcpoauth.Owner, id, client string) (bool, error) {
			return r.repository.HasExternalAgentAuthorization(ctx, domain.ExternalCaller{
				Principal:       domain.Principal{Issuer: owner.Issuer, Subject: owner.Subject, OrgID: owner.Org},
				ClientID:        client,
				AuthorizationID: id,
			})
		},
		Confirmer: func() string { return r.registry.ConnectionConfirmer(issuer, service) },
		Notify: func(ctx context.Context, confirmer string, approval mcpoauth.Approval) error {
			secret := cfg.ExternalAgents.TokenServices[service]
			return r.registry.DeliverConnectionApproval(ctx, service, secret, confirmer, approval)
		},
	})
	if err == nil {
		r.externalBroker, r.externalBrokerClient = broker, settings.ClientID
	}
	return err
}

func (r *runtimeComponents) authenticateBroker(ctx context.Context, req *http.Request) (domain.ExternalCaller, error) {
	token, err := identity.ExternalBearer(req)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	return r.verifyExternal(ctx, token)
}

func (r *runtimeComponents) verifyExternal(ctx context.Context, token string) (domain.ExternalCaller, error) {
	if r.externalBroker == nil {
		return r.externalAuth.VerifyExternal(ctx, token)
	}
	upstream, client, authorizationID, ok := r.externalBroker.Unwrap(token)
	if !ok {
		return domain.ExternalCaller{}, identity.ErrUnauthorized
	}
	caller, err := r.externalAuth.VerifyExternal(ctx, upstream)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if caller.ClientID != r.externalBrokerClient {
		return domain.ExternalCaller{}, identity.ErrUnauthorized
	}
	caller.ClientID, caller.BrokerToken, caller.AuthorizationID = client, token, authorizationID
	active, err := r.repository.HasExternalAgentAuthorization(ctx, caller)
	if err != nil {
		return domain.ExternalCaller{}, err
	}
	if !active {
		return domain.ExternalCaller{}, identity.ErrUnauthorized
	}
	return caller, nil
}
