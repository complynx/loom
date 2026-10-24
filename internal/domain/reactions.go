package domain

import (
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxReactionCatalogEntries = 256

// ReactionPolicy is an optional client capability, fixed for the run.
// Values are literal emoji or custom:<id>; descriptions are untrusted client data.
type ReactionPolicy struct {
	AllEmoji bool             `json:"all_emoji,omitempty"`
	Emoji    []string         `json:"emoji,omitempty"`
	Custom   []CustomReaction `json:"custom,omitempty"`
}

type CustomReaction struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type ReactionChange struct {
	MessageSeq int64  `json:"message_seq"`
	Reaction   string `json:"reaction"`
	Active     bool   `json:"active"`
}

type SetReaction struct {
	ReactionChange

	SourceEventID string `json:"source_event_id"`
}

func invalidReaction() error {
	return NewError(http.StatusUnprocessableEntity, "invalid_reaction")
}

// ValidateReactionValue bounds wire identifiers. Clients own emoji rendering and
// Unicode version support; Loom does not ship a separate emoji registry.
func ValidateReactionValue(value string) error {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return invalidReaction()
	}
	if id, custom := strings.CutPrefix(value, "custom:"); custom {
		if !validReactionID(id) {
			return invalidReaction()
		}
		return nil
	}
	hasSymbol := false
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.IsLetter(r) && r != '\u2139' {
			return invalidReaction()
		}
		hasSymbol = hasSymbol || unicode.IsSymbol(r) || r == '\u2139' || r == '\u3030' || r == '\u303d' || r == '\u20e3'
	}
	if !hasSymbol {
		return invalidReaction()
	}
	return nil
}

func (p *ReactionPolicy) Enabled() bool {
	return p != nil && (p.AllEmoji || len(p.Emoji)+len(p.Custom) > 0)
}

func validReactionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func (p *ReactionPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if len(p.Emoji)+len(p.Custom) > maxReactionCatalogEntries {
		return invalidReaction()
	}
	seen := map[string]bool{}
	for _, value := range p.Emoji {
		if ValidateReactionValue(value) != nil || strings.HasPrefix(value, "custom:") || seen[value] {
			return invalidReaction()
		}
		seen[value] = true
	}
	for _, value := range p.Custom {
		if !validReactionID(value.ID) || value.Description == "" || len(value.Description) > 256 ||
			seen["custom:"+value.ID] {
			return invalidReaction()
		}
		seen["custom:"+value.ID] = true
	}
	return nil
}

func (p *ReactionPolicy) Allows(value string) bool {
	if p == nil || ValidateReactionValue(value) != nil {
		return false
	}
	if id, custom := strings.CutPrefix(value, "custom:"); custom {
		for _, item := range p.Custom {
			if item.ID == id {
				return true
			}
		}
		return false
	}
	if p.AllEmoji {
		return true
	}
	return slices.Contains(p.Emoji, value)
}
