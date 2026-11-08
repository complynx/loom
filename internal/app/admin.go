package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/sandbox"
)

const adminModule = "admin"
const adminSkillName = "administer-loom"
const maxScriptInteger int64 = 9007199254740991

const (
	adminCapabilities          = "capabilities"
	adminSettingsGet           = "settings_get"
	adminSettingsUpdate        = "settings_update"
	adminFundingDefaultsGet    = "funding_defaults_get"
	adminFundingDefaultsUpdate = "funding_defaults_update"
	adminBalanceGet            = "balance_get"
	adminFundingGet            = "funding_get"
	adminFundingUpdate         = "funding_update"
	adminCreditsAdjust         = "credits_adjust"
	adminPolicyGet             = "policy_get"
	adminPolicyUpdate          = "policy_update"
	adminPreferencesGet        = "preferences_get"
	adminPreferencesUpdate     = "preferences_update"
)

func (t runtimeTools) appendAdminBindings(bindings []sandbox.Binding, p domain.Principal) []sandbox.Binding {
	if methods := t.adminMethods(p); len(methods) > 0 {
		return append(bindings, sandbox.Binding{Module: adminModule, Methods: methods})
	}
	return bindings
}

// adminMethods is shared by bindings, discovery and the generated administration skill.
func (t runtimeTools) adminMethods(p domain.Principal) []string {
	if !p.Has("chat.read") || p.OrgID == "" {
		return nil
	}
	methods := []string{}
	for _, group := range []struct {
		permission string
		methods    []string
	}{
		{"settings.read", []string{adminSettingsGet, adminFundingDefaultsGet}},
		{"settings.write", []string{adminSettingsUpdate, adminFundingDefaultsUpdate}},
		{"billing.adjust", []string{adminBalanceGet, adminFundingGet, adminFundingUpdate, adminCreditsAdjust}},
		{"users.policy.write", []string{adminPolicyGet, adminPolicyUpdate}},
		{"preferences.other.write", []string{adminPreferencesGet, adminPreferencesUpdate}},
	} {
		if strings.HasPrefix(group.permission, "settings.") &&
			(t.cfg.Identity.AdminOrganizationID == "" || p.OrgID != t.cfg.Identity.AdminOrganizationID) {
			continue
		}
		if p.Has(group.permission) {
			methods = append(methods, group.methods...)
		}
	}
	if len(methods) > 0 {
		methods = append(methods, adminCapabilities)
	}
	slices.Sort(methods)
	return methods
}

func adminWrite(method string) bool {
	return strings.HasSuffix(method, "_update") || method == adminCreditsAdjust
}

// Administration uses fresh Loom authority, never a bot role or skill text.
// The builtin dispatcher also checks the run's current lease and execution profile.
func (t runtimeTools) admin(
	ctx context.Context, run domain.Run, method string, raw json.RawMessage,
) (json.RawMessage, error) {
	ctx = identity.RequireFresh(ctx)
	p, _, err := t.fresh(ctx, run)
	if err != nil {
		return nil, err
	}
	if run.NativeFilesOnly() || p.OrgID != run.OrgID || !slices.Contains(t.adminMethods(p), method) {
		return nil, domain.Forbidden()
	}
	if err = checkAdminIntegers(raw); err != nil {
		return nil, err
	}
	result, err := t.adminCall(ctx, p, method, raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err = checkAdminIntegers(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t runtimeTools) adminCall(
	ctx context.Context, p domain.Principal, method string, raw json.RawMessage,
) (any, error) {
	switch method {
	case adminCapabilities, adminSettingsGet, adminFundingDefaultsGet:
		var in struct{}
		if err := decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		if method == adminCapabilities {
			return t.adminMethods(p), nil
		}
		settings, err := t.repository.GetSettings(ctx)
		if method == adminFundingDefaultsGet {
			return domain.MonthlyFundingConfiguration{
				Defaults: domain.FundingDefaults(settings.MonthlyFunding), Version: settings.Version,
			}, err
		}
		return settings, err
	case adminSettingsUpdate:
		var in domain.Settings
		if err := decodeAdminInput(raw, &in, "version"); err != nil {
			return nil, err
		}
		return t.repository.UpdateSettings(ctx, p, in.Version, in)
	case adminFundingDefaultsUpdate:
		var in domain.MonthlyFundingConfiguration
		if err := decodeAdminInput(
			raw,
			&in,
			"version",
			"defaults.enabled",
			"defaults.base_microcredits",
			"defaults.factor_ppm",
		); err != nil {
			return nil, err
		}
		return t.repository.UpdateMonthlyFundingDefaults(ctx, p, in)
	default:
		return t.adminUserCall(ctx, p, method, raw)
	}
}

func (t runtimeTools) adminUserCall(
	ctx context.Context, p domain.Principal, method string, raw json.RawMessage,
) (any, error) {
	// Method-specific envelopes keep owner IDs separate from the versioned resource.
	switch method {
	case adminBalanceGet, adminFundingGet, adminPolicyGet, adminPreferencesGet:
		var in struct {
			OwnerID string `json:"owner_id"`
		}
		if err := decodeAdminInput(raw, &in, "owner_id"); err != nil {
			return nil, err
		}
		switch method {
		case adminBalanceGet:
			return t.repository.UserBalance(ctx, p, in.OwnerID)
		case adminFundingGet:
			return t.repository.MonthlyFunding(ctx, p, in.OwnerID)
		case adminPolicyGet:
			return t.repository.Policy(ctx, p, in.OwnerID)
		default:
			return t.repository.UserPreferences(ctx, p, in.OwnerID)
		}
	case adminFundingUpdate:
		var in struct {
			OwnerID string                      `json:"owner_id"`
			Policy  domain.MonthlyFundingPolicy `json:"policy"`
		}
		if err := decodeAdminInput(raw, &in, "owner_id", "policy.version"); err != nil {
			return nil, err
		}
		return t.repository.UpdateMonthlyFunding(ctx, p, in.OwnerID, in.Policy)
	case adminPolicyUpdate:
		var in struct {
			OwnerID string            `json:"owner_id"`
			Policy  domain.UserPolicy `json:"policy"`
		}
		if err := decodeAdminInput(
			raw,
			&in,
			"owner_id",
			"policy.version",
			"policy.models",
			"policy.active_run_limit",
		); err != nil {
			return nil, err
		}
		return t.repository.UpdatePolicy(ctx, p, in.OwnerID, in.Policy.Version, in.Policy)
	case adminPreferencesUpdate:
		var in struct {
			OwnerID     string             `json:"owner_id"`
			Preferences domain.Preferences `json:"preferences"`
		}
		if err := decodeAdminInput(
			raw,
			&in,
			"owner_id",
			"preferences.version",
			"preferences.model_id",
			"preferences.effort",
		); err != nil {
			return nil, err
		}
		return t.repository.UpdatePreferences(ctx, p, in.OwnerID, in.Preferences.Version, in.Preferences)
	case adminCreditsAdjust:
		var in struct {
			OwnerID        string `json:"owner_id"`
			IdempotencyKey string `json:"idempotency_key"`
			Amount         int64  `json:"amount"`
			Reason         string `json:"reason"`
		}
		if err := decodeAdminInput(raw, &in, "owner_id", "idempotency_key", "amount", "reason"); err != nil {
			return nil, err
		}
		return t.repository.AdjustCredits(ctx, p, in.OwnerID, in.IdempotencyKey, in.Amount, in.Reason)
	default:
		return nil, domain.NotFound()
	}
}

// Required fields must be explicit: missing versions must never silently become create requests.
func decodeAdminInput(raw json.RawMessage, target any, required ...string) error {
	if err := decodeDiagnosticsInput(raw, target); err != nil {
		return err
	}
	for _, path := range required {
		value := raw
		for field := range strings.SplitSeq(path, ".") {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(value, &object); err != nil {
				return domain.NewError(http.StatusUnprocessableEntity, invalidArgumentsCode)
			}
			value = bytes.TrimSpace(object[field])
			if len(value) == 0 || bytes.Equal(value, []byte("null")) || bytes.Equal(value, []byte(`""`)) {
				return domain.NewError(http.StatusUnprocessableEntity, invalidArgumentsCode)
			}
		}
	}
	return nil
}

// Refuse lossy JS roundtrips; the HTTP API remains available for larger int64 values.
func checkAdminIntegers(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return domain.NewError(http.StatusUnprocessableEntity, invalidArgumentsCode)
		}
		if number, ok := token.(json.Number); ok {
			value, parseErr := number.Int64()
			if parseErr != nil || value < -maxScriptInteger || value > maxScriptInteger {
				return domain.NewError(http.StatusUnprocessableEntity, "admin_unsafe_integer")
			}
		}
	}
}
