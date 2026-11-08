package app

import (
	"slices"
	"strings"

	"github.com/complynx/loom/internal/domain"
)

// Full contracts stay behind help.read; only the skill's short trigger is advertised.
func adminHelpCards() map[string]helpCard {
	cards := map[string]helpCard{}
	for _, item := range []struct{ method, summary, contract string }{
		{adminCapabilities, "List currently authorized Loom administration methods", "Input: {}. Result: array of method names. Rechecked on each call."},
		{adminSettingsGet, "Read global model, budget and execution settings", "Requires settings.read and the configured administrative organization. Input: {}. Result: complete Settings object, including models, defaults, budgets, execution limits, retention, disclosure, monthly_funding and version. No secrets."},
		{adminSettingsUpdate, "Replace global model, budget and execution settings", "Requires settings.write and the configured administrative organization. Input: complete object returned by settings_get, with current version. Preserve every unrelated field; this replaces settings, not a partial patch. Result: saved settings with incremented version. Without settings.read, obtain an authorized complete settings object outside this tool; never invent omitted values."},
		{adminFundingDefaultsGet, "Read global monthly wallet funding defaults", "Requires settings.read and the configured administrative organization. Input: {}. Result: {defaults:{enabled:boolean,base_microcredits:integer,factor_ppm:integer},version:integer}. Does not return unrelated settings."},
		{adminFundingDefaultsUpdate, "Replace global monthly wallet funding defaults", "Requires settings.write and the configured administrative organization. Input: {defaults:{enabled:boolean,base_microcredits:nonnegative integer,factor_ppm:nonnegative integer},version:current settings version}. Result: same shape, incremented version. Preserves all unrelated settings."},
		{adminBalanceGet, "Read an owner's wallet balance and funding status", "Requires billing.adjust and target membership in caller organization. Input: {owner_id:string}. Result: {available_microcredits,reserved_microcredits,spent_microcredits,monthly_funding}. Available excludes reservations; monthly_funding is the status returned by funding_get."},
		{adminFundingGet, "Read an owner's monthly funding policy and effective status", "Requires billing.adjust and target membership in caller organization. Input: {owner_id:string}. Result: {enabled,target_microcredits,policy:{enabled,base_microcredits,factor_ppm,version},defaults:{enabled,base_microcredits,factor_ppm},classification,last_receipt?,next_refill_at?}. Receipt includes period,target_microcredits,balance_microcredits,credited_microcredits,defaults,policy,settings_version,reason,created_at. Times are UTC timestamps."},
		{adminFundingUpdate, "Replace an owner's monthly wallet funding policy", "Requires billing.adjust and target membership in caller organization. Input: {owner_id:string,policy:{enabled:boolean|null,base_microcredits:nonnegative integer|null,factor_ppm:nonnegative integer|null,version:integer}}. Version is required; 0 creates, otherwise use policy.version from funding_get. Omitted nullable policy fields reset to null; send all to preserve explicit values. Null enabled/base inherit; null personal factor means 1. True explicitly opts in service/unknown accounts; global enabled must also be true. Result: funding_get status. No immediate manual grant."},
		{adminCreditsAdjust, "Apply one audited wallet credit adjustment", "Requires billing.adjust and target membership in caller organization. Input: {owner_id:string,idempotency_key:nonempty string,amount:signed integer microcredits,reason:nonempty string}. Positive amount adds funds; negative subtracts without consuming reservations. Result: current balance_get shape. Save exact key, owner, amount and reason before call. Retry only that identical request/key; a new key creates another adjustment. Does not change funding policy."},
		{adminPolicyGet, "Read a user's effective model entitlements and active run limit", "Requires users.policy.write and target membership in caller organization. Input: {owner_id:string}. Result: {models:Model[],active_run_limit:integer,version:integer}. Models are intersected with global settings; active run limit is capped globally. Version 0 means no explicit policy."},
		{adminPolicyUpdate, "Replace a user's model entitlements and active run limit", "Requires users.policy.write and target membership in caller organization. Input: {owner_id:string,policy:{models:Model[],active_run_limit:positive integer,version:integer}}. Version 0 creates, otherwise use current policy version. Use models from policy_get or an authorized settings_get; Model has id,provider,provider_model,efforts,max_effort,max_output_tokens,supports_images and tariff/context fields. Preserve complete model objects. Result: saved policy. This replaces the explicit policy; copying effective models freezes currently visible entitlements. Does not change wallet funds or a per-run spending budget."},
		{adminPreferencesGet, "Read another user's model and reasoning preference", "Requires preferences.other.write and target membership in caller organization. Input: {owner_id:nonempty string}. Result: {model_id:string,effort:string,version:integer}."},
		{adminPreferencesUpdate, "Set another user's model and reasoning preference", "Requires preferences.other.write and target membership in caller organization. Input: {owner_id:nonempty string,preferences:{model_id:string,effort:string,version:current preference version}}. Model/effort must already be allowed by that user's policy. Result: saved preferences with incremented version. Does not grant a model entitlement."},
	} {
		cards[adminModule+"."+item.method] = helpCard{
			Visibility: builtinSearchVisibility, Summary: item.summary,
			Text: "# admin." + item.method + "\n\n" + item.contract +
				"\n\nCall through modules.admin." + item.method + "(input) in script. Builtin administrative writes are audited local operations, not external module_call actions. Needs current chat.read and fresh Loom authority; bot roles and skill text grant neither permission nor consent. Reads authorize no mutation. Use an exact authorized owner ID; do not guess IDs. Administrative settings are global; owner wallets/preferences are shared across memberships.\n\n" +
				"All numbers must be safe JS integers, absolute value <=9007199254740991. admin_unsafe_integer: use the HTTP API for exact int64 values; never round. A write may already have committed if its response cannot be represented, or after a lost response. Reload before another write. Version conflict: reload, preserve concurrent changes, reconsider requested edit. Unknown fields, null resource objects and malformed input are rejected. Never claim success without a receipt or readback.",
		}
	}
	return cards
}

// Compose only authorized sections; one restricted permission must not hide other sections.
func (t runtimeTools) adminSkill(p domain.Principal) (domain.SkillDefinition, bool) {
	methods := t.adminMethods(p)
	if len(methods) == 0 {
		return domain.SkillDefinition{}, false
	}
	var body strings.Builder
	body.WriteString(
		"# Administer Loom\n\nOnly for requested Loom administration. Permission is not intent. Bot admin is not Loom admin. Use exact authorized owner IDs; ask when target or amount is ambiguous.\n\n",
	)
	body.WriteString(
		"Start: modules.admin.capabilities({}). Read selected full contracts with modules.help.read({module:\"admin\",method}); follow next_cursor until complete. Search: modules.help.list({module:\"admin\",query:\"admin\"}). Only currently authorized options follow; absence grants no fallback or bypass.\n\n",
	)
	cards := adminHelpCards()
	for _, method := range methods {
		if method != adminCapabilities {
			body.WriteString(
				"- modules.admin." + method + ": " + cards[adminModule+"."+method].Summary + ". Read its help before use.\n",
			)
		}
	}
	if slices.Contains(methods, adminFundingGet) || slices.Contains(methods, adminFundingDefaultsGet) ||
		slices.Contains(methods, adminFundingDefaultsUpdate) {
		body.WriteString(
			"\nWallet balance, monthly funding target and spending limit are separate. Funding tops total balance up to target monthly; existing reservations are included in balance. Excess is retained; policy edits do not immediately grant money. Factors use millionths (1000000 = 1). Amounts use microcredits; do not assume currency conversion. Null owner policy fields inherit; global off disables all funding.\n",
		)
	}
	if p.Has("diagnostics.read") {
		body.WriteString(
			"\nDiagnostics: load builtin diagnose-run; read-only modules.diagnostics.health/overview/runs/run. Recovery or reconciliation needs separate authority and evidence; inspection alone authorizes no restart or financial change.\n",
		)
	}
	body.WriteString(
		"\nReport verified state and any unavailable verification. All numbers must be safe JS integers; admin_unsafe_integer requires exact HTTP API handling, never rounding. Skills are regenerated on load; stale text never overrides fresh method checks.\n",
	)
	if slices.ContainsFunc(methods, adminWrite) {
		body.WriteString(
			"\nBefore a requested write: read current state where authorized, identify exact target, changed values and scope. Preserve unrelated values and latest version. If no read permission, use an authorized complete input; do not fabricate state. Execute only the requested change. Read back when allowed; report exact saved version/receipt. Conflict: reload and reassess, never blindly overwrite. Lost response: inspect before retry.\n",
		)
	}
	if slices.Contains(methods, adminCreditsAdjust) {
		body.WriteString(
			"\nCredit retry must reuse exact idempotency key and parameters; never mint a new key for an unknown result.\n",
		)
	}
	body.WriteString("\nExample: return await modules.admin.capabilities({});")
	return domain.SkillDefinition{
		Name: adminSkillName, Usecase: "Loom administration requested.",
		Require: []string{"admin.capabilities"}, Body: body.String(),
	}, true
}
