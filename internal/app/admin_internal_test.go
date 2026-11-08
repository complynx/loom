package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
)

func TestAdminSkillContainsOnlyAuthorizedSections(t *testing.T) {
	t.Parallel()
	tools := runtimeTools{cfg: config.Config{Identity: config.Identity{AdminOrganizationID: "operators"}}}
	for _, tc := range []struct {
		name, org, permission string
		want, absent          string
	}{
		{"billing", "customer", "billing.adjust", "credits_adjust", "settings_update"},
		{"global reader", "operators", "settings.read", "settings_get", "settings_update"},
		{"wrong global org", "customer", "settings.read", "", ""},
		{"policy", "customer", "users.policy.write", "policy_update", "credits_adjust"},
		{"preferences", "customer", "preferences.other.write", "preferences_update", "policy_update"},
		{"bot admin", "customer", "bot.admin", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := domain.Principal{OrgID: tc.org, Permissions: map[string]bool{"chat.read": true, tc.permission: true}}
			skill, ok := tools.adminSkill(p)
			if tc.want == "" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.NoError(t, domain.ValidateSkills([]domain.SkillDefinition{skill}))
			require.Contains(t, skill.Body, tc.want)
			require.NotContains(t, skill.Body, tc.absent)
			require.Equal(t, []string{"admin.capabilities"}, skill.Require)
			entries, _, err := tools.helpEntries(domain.Run{}, p, nil, nil)
			require.NoError(t, err)
			require.True(t, skillRequirementsAvailable(domain.Skill{Require: skill.Require}, entries))
			overview, err := environmentOverview(entries, &domain.DiscoveryOptions{ExpandServices: []string{"$loom"}})
			require.NoError(t, err)
			require.NotContains(t, string(overview), "admin.")
			page, err := helpList(entries, helpRequest{Module: adminModule, Query: "admin"}, nil)
			require.NoError(t, err)
			require.Contains(t, string(page), tc.want)
			require.NotContains(t, string(page), tc.absent)
			metadata, err := json.Marshal(
				skillMetadata([]domain.Skill{{Name: skill.Name, Body: skill.Body, Require: skill.Require}}),
			)
			require.NoError(t, err)
			require.NotContains(t, string(metadata), tc.want)
			for _, method := range tools.adminMethods(p) {
				require.NotEmpty(t, adminHelpCards()[adminModule+"."+method].Text)
				require.Equal(t, adminWrite(method), builtinHelpEffect(adminModule, method) == "write")
			}
		})
	}
}

func TestAdminInputRejectsIncompleteWritesBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	tools := runtimeTools{}
	for _, tc := range []struct{ method, raw string }{
		{"settings_update", "{}"},
		{"funding_defaults_update", `{"version":1,"defaults":null}`},
		{"funding_defaults_update", `{"version":1,"defaults":{"enabled":true}}`},
		{"funding_update", `{"owner_id":"owner","policy":null}`},
		{"funding_update", `{"owner_id":"owner","policy":{"enabled":true}}`},
		{"policy_update", `{"owner_id":"owner","policy":{"version":0}}`},
		{"preferences_update", `{"owner_id":"","preferences":{"model_id":"test","effort":"medium","version":1}}`},
		{"credits_adjust", `{"owner_id":"owner","amount":10,"reason":"test"}`},
		{"credits_adjust", `{"owner_id":"owner","amount":null,"idempotency_key":"key","reason":"test"}`},
		{"balance_get", `{"owner_id":"owner","unexpected":true}`},
		{"funding_get", "null"},
	} {
		t.Run(tc.method+tc.raw, func(t *testing.T) {
			t.Parallel()
			_, err := tools.adminCall(t.Context(), domain.Principal{}, tc.method, json.RawMessage(tc.raw))
			require.Error(t, err)
		})
	}
	for _, raw := range []string{`{"amount":9007199254740992}`, `{"amount":-9007199254740992}`, `{"amount":0.5}`} {
		require.EqualError(t, checkAdminIntegers(json.RawMessage(raw)), "admin_unsafe_integer")
	}
	require.NoError(
		t,
		checkAdminIntegers(
			json.RawMessage(`{"amount":9007199254740991,"negative":-9007199254740991,"id":"9007199254740992"}`),
		),
	)
}

func TestAdminSkillReloadAndCallsRecheckAuthority(t *testing.T) {
	t.Parallel()
	p := domain.Principal{
		Subject:     "operator",
		OrgID:       "org",
		Permissions: map[string]bool{"chat.read": true, "billing.adjust": true},
	}
	tools := runtimeTools{
		registry:  &modules.Registry{},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	run := domain.Run{OrgID: "org"}
	read := json.RawMessage(`{"name":"administer-loom","source":"builtin"}`)
	loaded, err := tools.skills(t.Context(), run, builtinReadMethod, read)
	require.NoError(t, err)
	require.Contains(t, string(loaded), "credits_adjust")
	p.Permissions = map[string]bool{"chat.read": true, "users.policy.write": true}
	loaded, err = tools.skills(t.Context(), run, builtinReadMethod, read)
	require.NoError(t, err)
	require.NotContains(t, string(loaded), "credits_adjust")
	require.Contains(t, string(loaded), "policy_update")
	_, err = tools.admin(t.Context(), run, "credits_adjust", json.RawMessage("{}"))
	require.EqualError(t, err, "forbidden")
	_, err = tools.admin(t.Context(), domain.Run{OrgID: "other"}, "capabilities", json.RawMessage("{}"))
	require.EqualError(t, err, "forbidden")
	run.ExecutionProfile = domain.ProfileNativeFiles
	_, err = tools.admin(t.Context(), run, "capabilities", json.RawMessage("{}"))
	require.EqualError(t, err, "forbidden")
}

func TestAdminScriptFundingModelsAndScope(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	p := domain.Principal{
		Issuer: "issuer", Subject: "operator", Actor: "operator", OrgID: "org",
		Permissions: map[string]bool{
			"runs.create": true, "chat.read": true, "settings.read": true, "settings.write": true,
			"billing.adjust": true, "users.policy.write": true, "preferences.other.write": true,
		},
	}
	settings, err := repository.GetSettings(t.Context())
	require.NoError(t, err)
	settings.ActiveRunLimit, settings.HistoryRecentLimit, settings.MaxSteps, settings.QuestionTimeoutSeconds = 1, 50, 10, 60
	_, err = repository.UpdateSettings(t.Context(), p, settings.Version, settings)
	require.NoError(t, err)
	target := p
	target.Subject, target.Actor = "target", "target"
	owner, err := repository.ResolveUser(t.Context(), target)
	require.NoError(t, err)
	target.OrgID, target.Subject, target.Actor = "other-org", "outsider", "outsider"
	outsider, err := repository.ResolveUser(t.Context(), target)
	require.NoError(t, err)
	run, err := repository.CreateRun(
		t.Context(),
		p,
		"admin-script",
		domain.CreateRun{Input: domain.Input{Text: "Administer Loom"}},
	)
	require.NoError(t, err)
	run, err = repository.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil }
	tools := runtimeTools{
		repository: repository, registry: &modules.Registry{}, executor: sandbox.Inline{}, authorize: authorize,
		cfg: config.Config{
			Identity: config.Identity{AdminOrganizationID: "org"},
			Sandbox:  config.Sandbox{ScriptTimeoutCeilingMS: 5000},
		},
	}
	tools.bridge = NewModuleBridge(tools.registry, nil, repository, authorize)
	ownerJSON, err := json.Marshal(owner)
	require.NoError(t, err)
	out, err := tools.executeScript(t.Context(), run, `
const owner_id = `+string(ownerJSON)+`;
const before = await modules.admin.funding_defaults_get({});
const saved = await modules.admin.funding_defaults_update({defaults:{enabled:true,base_microcredits:10000,factor_ppm:1000000},version:before.version});
const f = await modules.admin.funding_get({owner_id});
await modules.admin.funding_update({owner_id,policy:{enabled:true,base_microcredits:null,factor_ppm:2000000,version:f.policy.version}});
const adjustment = {owner_id,idempotency_key:"one-grant",amount:500,reason:"Requested grant"};
await modules.admin.credits_adjust(adjustment);
await modules.admin.credits_adjust(adjustment);
const policy = await modules.admin.policy_get({owner_id});
await modules.admin.policy_update({owner_id,policy});
const preferences = await modules.admin.preferences_get({owner_id});
await modules.admin.preferences_update({owner_id,preferences});
const settings = await modules.admin.settings_get({});
settings.max_steps = 12;
await modules.admin.settings_update(settings);
return {balance:await modules.admin.balance_get({owner_id}),funding:await modules.admin.funding_get({owner_id}),saved};
`)
	require.NoError(t, err)
	var result struct {
		Balance domain.Balance              `json:"balance"`
		Funding domain.MonthlyFundingStatus `json:"funding"`
	}
	require.NoError(t, json.Unmarshal(out, &result))
	require.EqualValues(t, 10500, result.Balance.AvailableMicrocredits)
	require.EqualValues(t, 20000, result.Funding.TargetMicrocredits)
	current, err := repository.GetSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, 12, current.MaxSteps)
	require.True(t, current.MonthlyFunding.Enabled)
	_, err = tools.admin(
		t.Context(),
		run,
		"funding_defaults_update",
		json.RawMessage(`{"defaults":{"enabled":false,"base_microcredits":0,"factor_ppm":0},"version":0}`),
	)
	require.Error(t, err)
	for _, method := range []string{"balance_get", "funding_get", "policy_get", "preferences_get"} {
		input, marshalErr := json.Marshal(map[string]string{"owner_id": outsider})
		require.NoError(t, marshalErr)
		_, callErr := tools.admin(t.Context(), run, method, input)
		require.Error(t, callErr)
	}
	delete(p.Permissions, "billing.adjust")
	_, err = tools.builtin(
		t.Context(),
		run,
		adminModule,
		"credits_adjust",
		json.RawMessage(
			`{"owner_id":`+string(ownerJSON)+`,"idempotency_key":"revoked","amount":500,"reason":"Denied"}`,
		),
	)
	require.EqualError(t, err, "forbidden")
	delete(p.Permissions, "chat.read")
	_, err = tools.admin(t.Context(), run, "capabilities", json.RawMessage("{}"))
	require.EqualError(t, err, "forbidden")
}
