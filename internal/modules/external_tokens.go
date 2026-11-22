package modules

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/mcpoauth"
)

const externalTokenLimit = 64 << 10

// ExternalAgentTokens declares the endpoint where Loom gets a user's service
// token for an external agent.
type ExternalAgentTokens struct {
	Path         string `json:"path"`
	ApprovalPath string `json:"approval_path,omitempty"`
}

func validateExternalAgentEndpoints(tokens *ExternalAgentTokens) error {
	if tokens == nil {
		return nil
	}
	if err := validPath(tokens.Path); err != nil {
		return err
	}
	if tokens.ApprovalPath != "" {
		return validPath(tokens.ApprovalPath)
	}
	return nil
}

func (r *Registry) ConnectionConfirmer(issuer, serviceID string) string {
	for _, service := range r.snapshot() {
		m := service.manifest
		if m.ServiceID == serviceID && m.Caller != nil && m.Caller.Auth.Issuer == issuer &&
			m.ExternalAgentTokens != nil {
			return m.Caller.Auth.Subject
		}
	}
	return ""
}

func (r *Registry) DeliverConnectionApproval(
	ctx context.Context, serviceID, secret, confirmer string, approval mcpoauth.Approval,
) error {
	for _, service := range r.snapshot() {
		m := service.manifest
		if m.ServiceID != serviceID || m.Caller == nil || m.Caller.Auth.Issuer != approval.Issuer ||
			m.Caller.Auth.Subject != confirmer || secret == "" || m.ExternalAgentTokens == nil {
			continue
		}
		payload, _ := json.Marshal(map[string]any{fieldProtocolVersion: 1, "connection_approval": approval})
		_, err := invoke(ctx, service, Principal{Token: secret},
			m.ExternalAgentTokens.Path, payload, externalTokenLimit, r.opts.RequestTimeout)
		return err
	}
	return ErrUnavailable
}

// DeliverExternalApproval sends the prepared decision to the registered
// confirming client. Its subject and issuer came from the profile.
func (r *Registry) DeliverExternalApproval(
	ctx context.Context, issuer, confirmer string, secrets map[string]string, approval domain.ExternalApproval,
) error {
	for _, service := range r.snapshot() {
		manifest := service.manifest
		if manifest.Caller == nil || manifest.Caller.Auth.Issuer != issuer ||
			manifest.Caller.Auth.Subject != confirmer || manifest.ExternalAgentTokens == nil ||
			manifest.ExternalAgentTokens.ApprovalPath == "" || secrets[manifest.ServiceID] == "" {
			continue
		}
		if manifest.ServiceID != approval.Service {
			approval.Arguments = nil
		}
		payload, _ := json.Marshal(approval)
		_, err := invoke(ctx, service, Principal{Token: secrets[manifest.ServiceID]},
			manifest.ExternalAgentTokens.ApprovalPath, payload, externalTokenLimit, r.opts.RequestTimeout)
		return err
	}
	return ErrUnavailable
}

// TokenService is a discovered service that issues service tokens for external agents.
type TokenService struct {
	ServiceID string
	// Summary is service-authored text. Treat it as untrusted data.
	Summary string
}

// ExternalTokenServices lists the fresh services that declare a token endpoint,
// sorted by service ID.
func (r *Registry) ExternalTokenServices() []TokenService {
	out := []TokenService{}
	for _, service := range r.snapshot() {
		if service.manifest.ExternalAgentTokens != nil {
			out = append(out, TokenService{
				ServiceID: service.manifest.ServiceID,
				Summary:   DiscoverySummary(service.manifest.Summary, service.manifest.Description),
			})
		}
	}
	slices.SortFunc(out, func(a, b TokenService) int { return strings.Compare(a.ServiceID, b.ServiceID) })
	return out
}

// ExternalToken asks one token service for the service token of the user who
// owns subjectToken. secret authenticates Loom to that service: the agent holds
// subjectToken too and must not be able to get a service token by itself.
// It returns the token and its stated lifetime in seconds, which is nil when
// the service stated none. The token is not verified here; the caller must
// verify it.
func (r *Registry) ExternalToken(
	ctx context.Context,
	serviceID, secret, subjectToken string,
) (string, *int64, error) {
	if secret == "" || subjectToken == "" {
		return "", nil, ErrUnavailable
	}
	for _, service := range r.snapshot() {
		if service.manifest.ServiceID != serviceID || service.manifest.ExternalAgentTokens == nil {
			continue
		}
		payload, _ := json.Marshal(map[string]any{fieldProtocolVersion: 1, "subject_token": subjectToken})
		raw, err := invoke(
			ctx,
			service,
			Principal{Token: secret},
			service.manifest.ExternalAgentTokens.Path,
			payload,
			externalTokenLimit,
			r.opts.RequestTimeout,
		)
		if err != nil {
			return "", nil, err
		}
		var response struct {
			Token   string `json:"access_token"`
			Expires *int64 `json:"expires_in"`
		}
		if err = json.Unmarshal(raw, &response); err != nil {
			return "", nil, err
		}
		return response.Token, response.Expires, nil
	}
	return "", nil, ErrUnavailable
}
