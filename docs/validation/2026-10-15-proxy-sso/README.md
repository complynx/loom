# Browser diagnostics through existing OIDC ingress

Implemented `operator.proxy_origin` as an alternative to the existing direct
PKCE client. The operator-local profile selects proxy mode; customer hostnames,
Traefik patches and production instructions remain outside this repository.

Verified locally:

- Proxy GET/HEAD diagnostics reuse the normal Bearer authenticator, rights,
  handlers and recovery-mode rules. A cookie or identity header without a valid
  Bearer is rejected. Missing diagnostics permission and foreign origins are
  rejected; browser writes, unrelated admin paths and encoded traversal are denied.
- Proxy mode is opt-in. Origin validation rejects insecure non-loopback URLs,
  paths, queries and mixed PKCE/proxy configuration. The normal machine API works
  without browser redirects.
- Node behavioral UI checks cover cookie-bearing same-origin diagnostic requests
  without a JavaScript token, read-only URL selection, permission denial,
  expired-SSO redirects, disconnect/reconnect, existing PKCE races and pagination.
  Redirects are not followed; expiry clears the panel and its polling timer.
- Full Linux `go test -race -parallel=4 -tags=integration ./...` passes with real
  isolated PostgreSQL and the pinned media environment. Golden lint reports zero
  issues. No production IAM or provider endpoints were used.
- Rendered Compose confirms the high-priority browser router alone uses OIDC,
  the normal API router has no login middleware, the upstream access-token header
  is preserved, and the shared-project profile requests
  standard openid/profile/email scopes only. Traefik configuration validates without a
  Loom audience environment variable; Loom retains its own audience check. No shared-middleware patch is needed.

Not verified on the target deployment: the existing OIDC client's project/grants,
shared-project audience and role assertions in the configured admin organization with Assert Roles on
Authentication enabled, cookie renewal after changing
scopes, ingress certificates and the complete browser-to-Traefik-to-Zitadel flow.
Installed plugin v0.13.0 documentation was checked for the existing absolute
callback and shared-cookie configuration. Reusing a client does not automatically
grant Loom access. Normal permission-cache TTL and purge rules remain in force.

See the [operator login contract](../../operator-login.md). No production
deployment or paid calls were performed. Owned QA containers were removed.

Fixed admin-organization acceptance: an HTTP stub issuer returns tokens with
no resource-owner claim. Browser health succeeds only with a mapped role grant in
the configured admin organization. The same cached token still fails the normal
API's missing-organization check. Foreign grants, absent roles, wrong audience,
expired/revoked tokens, static/discovered service identities and delegation/session
headers are denied. Cache purge exposes removed roles; classifier unavailability
fails closed. Browser organization selection never comes from HTTP headers.
Fixed-organization follow-up: the first full Linux race run failed an existing
queue-recovery test (2 recovered rows instead of 3). Ten isolated repeats passed;
the cause remains unconfirmed and is recorded in the development track. Focused
identity/API/config tests, UI checks and Golden lint passed. Independent review
reported no actionable findings. Do not interpret isolated repeats as a fix for
the queue test.
The second full Linux race/integration run, forced uncached with `-count=1`, passed
all packages. The queue-recovery finding remains open until its cause is proven.
