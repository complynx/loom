# Operator diagnostics

All data endpoints require a valid bearer credential with `diagnostics.read`. Durable data is limited to the caller organization. No extra cross-organization administrator access is implied.

- `GET /v1/admin/diagnostics`: run/queue counts, reservations, unknown inference, unresolved writes, platform exposure estimates and cached dependency health.
- `GET /v1/admin/diagnostics/health`: cached runtime health and sample timestamp; no database query in the handler. Authentication may still depend on issuer/database availability.
- `GET /v1/admin/diagnostics/runs?state=&after_id=&limit=50`: empty state selects nonterminal runs; `all` selects all. Limit 1..100. Follow `next_cursor`; pagination uses stable UUID order, not creation time.
- `GET /v1/admin/diagnostics/runs/{id}`: safe run, credit, step, write and provider retry metadata. Each related list has at most 100 recent entries and an explicit partial flag.

The same operations are available inside scripts as `modules.diagnostics.health({})`, `.overview({})`, `.runs({state,after_id,limit})` and `.run({run_id})`. Discovery hides the functions without current rights. Each call checks current authorization and the run lease. Remote discovery failure does not remove these builtins for authorized operators.

The plain `/admin` panel is read-only and refreshes every 15 seconds. Proxy SSO automatically loads data after upstream login; direct PKCE shows a sign-in control. See [operator login](operator-login.md) for the mutually exclusive modes. In direct mode the token is held only in page memory; proxy mode returns no access token to JavaScript. Disconnect clears displayed data and polling, not the shared SSO session. The shell contains no operational data. Use TLS.

Responses exclude prompts, credentials, raw provider errors, write arguments and artifact bytes. Exposure is a reserved estimate, not an invoice. Prometheus remains the private operational path when user authentication is unavailable. High-cardinality IDs are log fields, never metric labels.

The panel supports an existing proxy SSO. Test direct PKCE separately if configured; it is not a prerequisite for proxy login. Verify denial/revocation, organization isolation, bounded pagination, safe detail and incident usability using the [QA guide](qa.md).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
