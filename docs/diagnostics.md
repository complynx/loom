# Operator diagnostics

Diagnostics require valid authorization with `diagnostics.read`. Durable data is
limited to the caller's organization; this permission grants no cross-organization
access. Use a Bearer token for direct API calls or the configured
[browser login](operator-login.md).

## Read operational state

| Request | Response |
| --- | --- |
| `GET /v1/admin/diagnostics` | `{overview,runtime}`: organization run/queue counts, reservations, unknown inference, unresolved writes, platform exposure and cached dependency health |
| `GET /v1/admin/diagnostics/health` | Cached runtime health and sample timestamps; the handler does not query the database, but authentication may need issuer/database access |
| `GET /v1/admin/diagnostics/runs?state=&after_id=&limit=50` | `{items,next_cursor,partial}`; empty state selects nonterminal runs, `all` selects every state, or use one run state |
| `GET /v1/admin/diagnostics/runs/{id}` | `{run,steps,writes,attempts,provider_retry,...}` with safe credit, lease, fence, operation and retry metadata |

Run-list `limit` is 1–100 (default 50). Continue with `after_id=next_cursor`, keeping
the same state filter. Ordering is stable UUID order, not creation time. In detail,
`steps_partial`, `writes_partial` and `attempts_partial` tell you whether each list
omits older entries; each list contains at most the 100 most recent records.
Diagnostic run objects use `id` and `state`, unlike the normal run API's `run_id`
and `status`. Use the returned `fence` when [preparing restored work](api/files-recovery.md#prepare-a-restored-run).

Exposure is a reserved estimate, not an invoice. Responses exclude prompts,
credentials, raw provider errors, write arguments and artifact bytes. A cached
health sample may be stale; check its timestamp before interpreting it.
Prometheus on the private listener remains the operational path when user
authentication is unavailable. High-cardinality IDs belong in logs, not metric labels.

## Scripts and browser panel

Scripts can call `modules.diagnostics.health({})`, `.overview({})`,
`.runs({state,after_id,limit})` and `.run({run_id})`. Discovery hides these functions
without current rights. Every call checks authorization and the run lease. Remote
module discovery failure does not remove these builtins for authorized operators.

The `/admin` panel is read-only and refreshes every 15 seconds. Its shell contains
no operational data. Proxy SSO loads data after upstream login; direct PKCE offers
a sign-in control. The login modes are mutually exclusive. In direct mode the
token stays in page memory; proxy mode returns no access token to JavaScript.
Disconnect clears this panel's data and polling, not the shared SSO session.
Use TLS and follow [Operator login](operator-login.md) to configure either mode.
