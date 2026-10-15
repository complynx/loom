package api

import (
	"context"
	"net/http"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type operatorRequestKey struct{}

type operatorAuthenticator interface {
	AuthenticateOperator(context.Context, *http.Request) (domain.Principal, error)
}

func (s *server) authenticate(r *http.Request) (domain.Principal, error) {
	if browser, _ := r.Context().Value(operatorRequestKey{}).(bool); !browser {
		return s.auth.Authenticate(r.Context(), r)
	}
	auth, ok := s.auth.(operatorAuthenticator)
	if !ok || s.options.AdminOrganizationID == "" {
		return domain.Principal{}, identity.ErrUnavailable
	}
	p, err := auth.AuthenticateOperator(r.Context(), r)
	if err == nil && p.OrgID != s.options.AdminOrganizationID {
		return domain.Principal{}, identity.ErrUnauthorized
	}
	return p, err
}
