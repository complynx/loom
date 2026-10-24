package config

import (
	"errors"
	"slices"
	"strings"
)

// ValidateServiceAccounts requires machine markers that survive self-access revocation.
func (c Identity) ValidateServiceAccounts() error {
	for subject, permissions := range c.ServiceAccounts {
		if strings.TrimSpace(subject) == "" || !slices.Contains(c.ServiceSubjects, subject) {
			return errors.New("service account subject must be present in identity.service_subjects")
		}
		for _, permission := range permissions {
			if strings.TrimSpace(permission) == "" {
				return errors.New("service account permissions must not be blank")
			}
		}
	}
	return nil
}
