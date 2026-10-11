package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
)

const maxModuleArtifacts = 16

type ArtifactDescriptor struct {
	DownloadPath string `json:"download_path"`
	MIMEType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
}
type ArtifactSource struct {
	BaseURL            string
	RunID              string
	ServiceID          string
	ExternalDisclosure bool
	Client             *http.Client
	Headers            http.Header
	AllowedMIMETypes   []string
	// Authorize is called serially before GET and after EOF. Clone Headers after
	// authorization; request headers must not alias this per-import mutable map.
	Authorize func(context.Context) error
}
type ArtifactImporter func(context.Context, Principal, ArtifactDescriptor, ArtifactSource) (json.RawMessage, error)

func (r *Registry) importArtifacts(
	ctx context.Context,
	p Principal,
	s registered,
	moduleName, runID string,
	m Method,
	raw json.RawMessage,
	guard func(context.Context) error,
	principal func(context.Context) (Principal, error),
) ([]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var descriptors []ArtifactDescriptor
	if err := json.Unmarshal(raw, &descriptors); err != nil {
		return nil, err
	}
	if len(descriptors) == 0 {
		return nil, nil
	}
	if r.opts.ImportArtifact == nil {
		return nil, errors.New("artifact import not enabled")
	}
	if len(descriptors) > maxModuleArtifacts {
		return nil, errors.New("too many module artifacts")
	}
	origin, err := url.Parse(s.endpoint.URI)
	if err != nil {
		return nil, err
	}
	origin.Path = ""
	origin.RawPath = ""
	origin.RawQuery = ""
	origin.Fragment = ""
	headers := http.Header{}
	if p.Token != "" {
		headers.Set("Authorization", "Bearer "+p.Token)
	}
	authority := artifactAuthority{
		registry:  r,
		expected:  p,
		service:   s,
		module:    moduleName,
		method:    m,
		headers:   headers,
		guard:     guard,
		principal: principal,
	}
	source := ArtifactSource{
		RunID:              runID,
		ServiceID:          s.manifest.ServiceID,
		ExternalDisclosure: m.ExternalDisclosure,
		BaseURL:            origin.String(),
		Client:             s.client,
		Headers:            headers,
		AllowedMIMETypes:   append([]string(nil), m.ArtifactMIMETypes...),
		Authorize:          authority.authorize,
	}
	result := make([]json.RawMessage, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if pathErr := validPath(descriptor.DownloadPath); pathErr != nil {
			return nil, pathErr
		}
		if !slices.Contains(m.ArtifactMIMETypes, descriptor.MIMEType) {
			return nil, ErrForbidden
		}
		handle, importErr := r.opts.ImportArtifact(ctx, p, descriptor, source)
		if importErr != nil {
			return nil, importErr
		}
		if !json.Valid(handle) {
			return nil, errors.New("invalid imported artifact handle")
		}
		result = append(result, handle)
	}
	return result, nil
}

// artifactAuthority refreshes permissions before download and after EOF.
type artifactAuthority struct {
	registry  *Registry
	expected  Principal
	service   registered
	module    string
	method    Method
	headers   http.Header
	guard     func(context.Context) error
	principal func(context.Context) (Principal, error)
}

func (a artifactAuthority) authorize(ctx context.Context) error {
	if a.guard != nil {
		if err := a.guard(ctx); err != nil {
			return err
		}
	}
	current := a.expected
	if a.principal != nil {
		freshPrincipal, authErr := a.principal(ctx)
		current = freshPrincipal
		if authErr != nil {
			return authErr
		}
	}
	if current.UserID != a.expected.UserID || current.Issuer != a.expected.Issuer ||
		current.Subject != a.expected.Subject ||
		current.Actor != a.expected.Actor ||
		current.Organization != a.expected.Organization {
		return ErrForbidden
	}
	fresh, method, err := a.registry.lookupFresh(ctx, current, a.module, a.method.ID)
	if err != nil {
		return err
	}
	if fresh.endpoint.ContainerID != a.service.endpoint.ContainerID ||
		fresh.endpoint.Address != a.service.endpoint.Address ||
		fresh.endpoint.URI != a.service.endpoint.URI {
		return ErrUnavailable
	}
	if contractHash(fresh, a.module, method) != contractHash(a.service, a.module, a.method) {
		return ErrUnavailable
	}
	if current.Token == "" {
		return ErrForbidden
	}
	a.headers.Set("Authorization", "Bearer "+current.Token)
	return nil
}
