package store

import (
	"slices"

	"github.com/complynx/clx-scripter/internal/domain"
)

// effectiveModels intersects current descriptors with the user's allowed efforts.
func effectiveModels(catalog, entitlements []domain.Model) []domain.Model {
	if entitlements == nil {
		return catalog
	}
	out := []domain.Model{}
	for _, model := range catalog {
		for _, grant := range entitlements {
			if grant.ID != model.ID {
				continue
			}
			if allowed, ok := entitledModel(model, grant); ok {
				out = append(out, allowed)
			}
			break
		}
	}
	return out
}

func entitledModel(model, grant domain.Model) (domain.Model, bool) {
	if len(model.Efforts) == 0 {
		return model, len(grant.Efforts) == 0 && grant.MaxEffort == ""
	}
	ceiling := len(model.Efforts) - 1
	if grant.MaxEffort != "" {
		ceiling = -1
		// Use the last matching effort, as the original entitlement contract does.
		for index, effort := range model.Efforts {
			if effort == grant.MaxEffort {
				ceiling = index
			}
		}
	}
	if ceiling < 0 {
		return model, false
	}
	for index, effort := range model.Efforts {
		if effort == model.MaxEffort && index < ceiling {
			ceiling = index
		}
	}
	allowed := []string{}
	for _, effort := range model.Efforts[:ceiling+1] {
		if grant.Efforts == nil || slices.Contains(grant.Efforts, effort) {
			allowed = append(allowed, effort)
		}
	}
	if len(allowed) == 0 {
		return model, false
	}
	model.Efforts = allowed
	model.MaxEffort = allowed[len(allowed)-1]
	return model, true
}
