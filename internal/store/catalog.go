package store

import (
	"github.com/complynx/clx-scripter/internal/domain"
)

// effectiveModels intersects current descriptors with the user's allowed efforts.
func effectiveModels(catalog, entitlements []domain.Model) []domain.Model {
	if entitlements == nil {
		return catalog
	}
	out := []domain.Model{}
	for _, m := range catalog {
		for _, e := range entitlements {
			if e.ID != m.ID {
				continue
			}
			if len(m.Efforts) == 0 {
				if len(e.Efforts) == 0 && e.MaxEffort == "" {
					out = append(out, m)
				}
				break
			}
			cap := len(m.Efforts) - 1
			if e.MaxEffort != "" {
				cap = -1
				for i, v := range m.Efforts {
					if v == e.MaxEffort {
						cap = i
					}
				}
			}
			if cap < 0 {
				break
			}
			for i, v := range m.Efforts {
				if v == m.MaxEffort && i < cap {
					cap = i
				}
			}
			allowed := []string{}
			for i, v := range m.Efforts {
				if i > cap {
					break
				}
				include := e.Efforts == nil
				for _, grant := range e.Efforts {
					if grant == v {
						include = true
					}
				}
				if include {
					allowed = append(allowed, v)
				}
			}
			if len(allowed) == 0 {
				break
			}
			m.Efforts = allowed
			m.MaxEffort = allowed[len(allowed)-1]
			out = append(out, m)
			break
		}
	}
	return out
}
