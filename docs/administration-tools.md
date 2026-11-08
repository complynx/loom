# Administration from chat

Loom exposes built-in administrative methods through `modules.admin` in
`script`. These methods reuse the store operations used by the HTTP API.
They do not change role mappings or give a bot administrator Loom authority.

## Discovery and skill

The built-in `administer-loom` skill is generated for the current principal.
Only authorized options and their relevant procedures appear in its body.
The initial model context contains its short trigger and
`admin.capabilities` dependency, not the method list or full instructions.
Read the skill with:

```js
return await modules.skills.read({name:"administer-loom",source:"builtin"});
```

Skill loading checks fresh identity-provider authority. Losing one permission
removes its sections without hiding other authorized sections. Without any
administrative capability, the skill is absent. The `native_files` execution
profile excludes both the skill and these methods.

Administrative methods use search visibility. They stay out of the initial
environment overview and unfiltered lists. Read only the selected contracts:

```js
return await modules.help.read({module:"admin",method:"funding_get"});
```

Search with `modules.help.list({module:"admin",query:"funding"})`.
Follow help continuation cursors. `modules.admin.capabilities({})` returns
the currently authorized method names.

## Authority

All methods require `chat.read`. Each call reauthenticates with the positive
claims cache bypassed and requires the run's organization. The script dispatcher
also checks the current run lease, fence and execution profile before dispatch.

| Methods | Additional permission | Scope |
| --- | --- | --- |
| `settings_get`, `funding_defaults_get` | `settings.read` | Configured administrative organization |
| `settings_update`, `funding_defaults_update` | `settings.write` | Configured administrative organization |
| `balance_get`, `funding_get`, `funding_update`, `credits_adjust` | `billing.adjust` | Target membership in caller's organization |
| `policy_get`, `policy_update` | `users.policy.write` | Target membership in caller's organization |
| `preferences_get`, `preferences_update` | `preferences.other.write` | Target membership in caller's organization |

`capabilities` needs at least one of these authorized method groups.
Settings and their funding defaults are global. Owner wallets and preferences
are shared across that owner's memberships. A user policy applies within
the caller's organization. No user-directory lookup is added: callers need an
exact authorized internal `owner_id`.

Existing `modules.diagnostics` methods remain read-only and require
`diagnostics.read`. The generated skill points to `diagnose-run` only when
that permission is available. Recovery and billing reconciliation remain
separate operator procedures.

## Writes and recovery

Administrative writes are audited local built-ins. Call them through
`modules.admin`, not the external `module_call` tool. A user request must
authorize the exact change; loading a skill or reading state authorizes no write.

- `settings_update` takes the complete settings object and current version.
  Preserve unrelated fields. Prefer `funding_defaults_update` for funding-only
  edits; it preserves unrelated settings on the server.
- Owner methods take `owner_id`. Updates wrap the versioned object in
  `policy` or `preferences`. Read the method help for the exact shape.
  Versions must be explicit. Version 0 creates an absent policy; later changes
  require the current version. A preference update requires its current version.
- Funding policy replacement uses nullable overrides. Omitted nullable fields
  reset to null. Preserve existing values unless the user requested their reset.
- `policy_get` returns effective models and the globally capped active run
  limit. Writing that result makes an explicit policy from those effective
  values. Do not treat the result as the original uncapped policy.
- `credits_adjust` requires `owner_id`, `idempotency_key`, signed `amount`
  in microcredits and a nonempty `reason`. Repeating the identical request and
  key does not add another adjustment. A different key is another adjustment.

On a version conflict, reload and reassess the requested edit. After a lost
response, inspect state before retrying. An adjustment retry must use the exact
original key and parameters. Read back where authorized and report any limit
on verification. See [monthly funding](monthly-funding.md) for target,
reservation, inheritance and refill semantics.

All input and output numbers must be JavaScript safe integers, from
`-9007199254740991` through `9007199254740991`. Otherwise the method returns
`admin_unsafe_integer`; use the HTTP API for exact int64 values and never round.
A write can have committed before its response fails this check. Inspect its
state with the exact HTTP API before any retry. Scoped funding methods avoid
roundtripping unrelated settings through JavaScript.

## Validation

Focused tests cover generated sections, discovery, incomplete writes,
revoked authority, scope, execution profiles and integer bounds:

```sh
go test ./internal/app -run TestAdmin -count=1
```

Set `CLX_TEST_DATABASE_DSN` to a disposable PostgreSQL database to include
the script-to-store checks for funding, settings, policies, preferences,
idempotent credit adjustment and target organization membership.
