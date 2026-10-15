# Operator diagnostics

All data endpoints require a fresh bearer credential with `diagnostics.read`. Durable data is limited to the caller organization. No extra cross-organization administrator access is implied.

- `GET /v1/admin/diagnostics`: run/queue counts, reservations, unknown inference, unresolved writes, platform exposure estimates and cached dependency health.
- `GET /v1/admin/diagnostics/health`: cached runtime health and sample timestamp; no database query in the handler. Authentication may still depend on issuer/database availability.
- `GET /v1/admin/diagnostics/runs?state=&after_id=&limit=50`: empty state selects nonterminal runs; `all` selects all. Limit 1..100. Follow `next_cursor`; pagination uses stable UUID order, not creation time.
- `GET /v1/admin/diagnostics/runs/{id}`: safe run, credit, step, write and provider retry metadata. Each related list has at most 100 recent entries and an explicit partial flag.

The same operations are available inside scripts as `modules.diagnostics.health({})`, `.overview({})`, `.runs({state,after_id,limit})` and `.run({run_id})`. Discovery hides the functions without current rights. Each call checks current authorization and the run lease. Remote discovery failure does not remove these builtins for authorized operators.

The plain `/admin` panel uses these APIs. Select Sign in with Zitadel, inspect overview/run detail, filter and refresh. See [operator login rollout](operator-login.md) for public client registration. It refreshes every 15 seconds. Access-token storage is page memory only; disconnect clears it and displayed data. The static shell is public; it contains no operational data. Use TLS on the server. There are no write controls.

Responses exclude prompts, credentials, raw provider errors, write arguments and artifact bytes. Exposure is a reserved estimate, not an invoice. Prometheus remains the private operational path when user authentication is unavailable. High-cardinality IDs are log fields, never metric labels.

Local checks cover permission denial/revocation, organization isolation, bounded pagination and safe detail. The login tests use a local stub issuer and cover PKCE, session binding, expiry, replay and authorization denial. [Local real Zitadel browser acceptance](validation/2026-10-11-panel-login-qa/README.md) covers login, pagination/detail, role removal/restoration and disconnect. Production client acceptance remains a separate rollout gate. Production traffic and incident usability remain unverified.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
