package domain

import (
	"net/http"
	"regexp"
)

// DiscoveryOptions changes prompt presentation, never service authority.
type DiscoveryOptions struct {
	ExpandServices []string `json:"expand_services,omitempty" maxItems:"10" uniqueItems:"true"`
}

var discoveryServiceID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// ValidateDiscovery bounds a caller's optional initial service expansions.
func ValidateDiscovery(options *DiscoveryOptions) error {
	if options == nil {
		return nil
	}
	const maxExpandedServices = 10
	if len(options.ExpandServices) > maxExpandedServices {
		return NewError(http.StatusUnprocessableEntity, "invalid_discovery")
	}
	seen := map[string]bool{}
	for _, id := range options.ExpandServices {
		if seen[id] || (id != "$loom" && !discoveryServiceID.MatchString(id)) {
			return NewError(http.StatusUnprocessableEntity, "invalid_discovery")
		}
		seen[id] = true
	}
	return nil
}
