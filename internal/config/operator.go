package config

import (
	"errors"
	"net/url"
)

// Operator configures a public PKCE client, separate from introspection credentials.
type Operator struct {
	ClientID    string `yaml:"client_id"`
	RedirectURI string `yaml:"redirect_uri"`
}

func (o Operator) Validate() error {
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
