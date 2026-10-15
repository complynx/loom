package config

import (
	"errors"
	"net/url"
)

// Operator selects browser SSO through a proxy or a separate public PKCE client.
type Operator struct {
	ClientID    string `yaml:"client_id"`
	RedirectURI string `yaml:"redirect_uri"`
	ProxyOrigin string `yaml:"proxy_origin"`
}

func (c Config) validateOperator() error {
	if err := c.Operator.Validate(); err != nil {
		return err
	}
	if c.Operator.ProxyOrigin != "" && c.Identity.AdminOrganizationID == "" {
		return errors.New("operator proxy login requires identity.admin_organization_id")
	}
	return nil
}

func (o Operator) Validate() error {
	if o.ProxyOrigin != "" {
		if o.ClientID != "" || o.RedirectURI != "" {
			return errors.New("operator proxy_origin and PKCE client are mutually exclusive")
		}
		if err := validateURL(o.ProxyOrigin); err != nil {
			return err
		}
		u, _ := url.Parse(o.ProxyOrigin)
		if u.Path != "" || u.RawPath != "" || u.ForceQuery {
			return errors.New("operator proxy_origin must be an origin without a path")
		}
		return nil
	}
	if o.ClientID == "" && o.RedirectURI == "" {
		return nil
	}
	if o.ClientID == "" || o.RedirectURI == "" {
		return errors.New("operator client_id and redirect_uri must be configured together")
	}
	if err := validateURL(o.RedirectURI); err != nil {
		return err
	}
	u, _ := url.Parse(o.RedirectURI)
	if u.Path != "/admin" || u.RawPath != "" {
		return errors.New("operator redirect_uri must use /admin")
	}
	return nil
}
