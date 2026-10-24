package identity

import (
	"context"
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

func serviceAccountRequest(r *http.Request) error {
	if r.Header.Get("X-Subject-Token") != "" {
		return ErrUnauthorized
	}
	return nil
}

func (a *Authenticator) sessionSubjectIsService(ctx context.Context, expected, user domain.Principal) (bool, error) {
	if user.ServiceAccount {
		return expected.Actor != expected.Subject, nil
	}
	return a.classifyService(ctx, user)
}
