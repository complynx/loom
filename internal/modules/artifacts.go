package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

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

func (r *Registry) importArtifacts(ctx context.Context, p Principal, s registered, moduleName, runID string, m Method, raw json.RawMessage, guard func(context.Context) error, principal func(context.Context) (Principal, error)) ([]json.RawMessage, error) {
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
	if len(descriptors) > 16 {
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
	authorize := func(ctx context.Context) error {
		if guard != nil {
			if err := guard(ctx); err != nil {
				return err
			}
		}
		current := p
		if principal != nil {
			freshPrincipal, authErr := principal(ctx)
			current = freshPrincipal
			err := authErr
			if err != nil {
				return err
			}
		}
		if current.UserID != p.UserID || current.Issuer != p.Issuer || current.Subject != p.Subject || current.Actor != p.Actor || current.Organization != p.Organization {
			return ErrForbidden
		}
		fresh, method, err := r.lookupFresh(ctx, current, moduleName, m.ID)
		if err != nil {
			return err
		}
		if fresh.endpoint.ContainerID != s.endpoint.ContainerID || fresh.endpoint.Address != s.endpoint.Address || fresh.endpoint.URI != s.endpoint.URI {
			return ErrUnavailable
		}
		if contractHash(fresh, moduleName, method) != contractHash(s, moduleName, m) {
			return ErrUnavailable
		}
		if current.Token == "" {
			return ErrForbidden
		}
		headers.Set("Authorization", "Bearer "+current.Token)
		return nil
	}
	source := ArtifactSource{RunID: runID, ServiceID: s.manifest.ServiceID, ExternalDisclosure: m.ExternalDisclosure, BaseURL: origin.String(), Client: s.client, Headers: headers, AllowedMIMETypes: append([]string(nil), m.ArtifactMIMETypes...), Authorize: authorize}
	result := make([]json.RawMessage, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if err := validPath(descriptor.DownloadPath); err != nil {
			return nil, err
		}
		allowed := false
		for _, mime := range m.ArtifactMIMETypes {
			if mime == descriptor.MIMEType {
				allowed = true
			}
		}
		if !allowed {
			return nil, ErrForbidden
		}
		handle, err := r.opts.ImportArtifact(ctx, p, descriptor, source)
		if err != nil {
			return nil, err
		}
		if !json.Valid(handle) {
			return nil, errors.New("invalid imported artifact handle")
		}
		result = append(result, handle)
	}
	return result, nil
}
