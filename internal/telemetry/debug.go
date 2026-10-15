package telemetry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/complynx/loom/internal/domain"
)

const (
	debugPayloadLimit = 4096
	redactedSecret    = "***"
)

var jwtPattern = regexp.MustCompile(`[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// EnablePayloads is an immutable startup option, permitted only at debug level.
func (t *Telemetry) EnablePayloads(enabled bool) { t.logPayloads = enabled }
func (t *Telemetry) DebugRequest(ctx context.Context, r *http.Request, p domain.Principal) {
	if !t.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	t.logger.DebugContext(
		ctx,
		"authorized request",
		"request_id",
		RequestID(ctx),
		"subject",
		p.Subject,
		"actor",
		p.Actor,
		"org_id",
		p.OrgID,
		"route",
		r.Pattern,
	)
	if t.logPayloads {
		t.logger.DebugContext(
			ctx,
			"request credentials redacted",
			"request_id",
			RequestID(ctx),
			"authorization",
			redactToken(r.Header.Get("Authorization")),
			"subject_token",
			redactToken(r.Header.Get("X-Subject-Token")),
		)
	}
}
func (t *Telemetry) DebugPayload(ctx context.Context, component string, payload any) {
	if !t.logPayloads || !t.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	redacted := redactValue(value)
	raw, err = json.Marshal(redacted)
	if err != nil {
		return
	}
	text := string(raw)
	truncated := len(text) > debugPayloadLimit
	if truncated {
		text = text[:debugPayloadLimit]
	}
	t.logger.DebugContext(
		ctx,
		"diagnostic payload",
		"component",
		component,
		"request_id",
		RequestID(ctx),
		"run_id",
		RunID(ctx),
		"payload",
		text,
		"truncated",
		truncated,
	)
}
func redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = redactField(key, child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = redactValue(child)
		}
		return v
	case string:
		return redactString(v)
	default:
		return v
	}
}
func redactToken(token string) string {
	if token == "" {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(token, "Bearer ") {
		prefix = "Bearer "
		token = strings.TrimPrefix(token, prefix)
	}
	redacted := redactJWTs(token)
	if redacted == token || strings.Count(token, ".") != 2 || jwtPattern.FindString(token) != token {
		return prefix + redactedSecret
	}
	return prefix + redacted
}
func redactJWTs(text string) string {
	return jwtPattern.ReplaceAllStringFunc(text, func(token string) string {
		parts := strings.Split(token, ".")
		header, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil || !json.Valid(header) {
			return token
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(header, &object) != nil || object["alg"] == nil {
			return token
		}
		return parts[0] + "." + parts[1] + ".***"
	})
}

func redactString(v string) any {
	var nested any
	if json.Unmarshal([]byte(v), &nested) == nil {
		switch nested.(type) {
		case map[string]any, []any:
			raw, err := json.Marshal(redactValue(nested))
			if err == nil {
				return string(raw)
			}
		}
	}
	return redactJWTs(v)
}

func redactField(key string, child any) any {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "token") || lower == authorizationComponent {
		if text, ok := child.(string); ok {
			return redactToken(text)
		}
		return redactedSecret
	}
	if strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
		return redactedSecret
	}
	switch lower {
	case "api_key", "apikey", "encrypted_content", "image_url", "base64":
		return redactedSecret
	}
	return redactValue(child)
}
