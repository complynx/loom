# Operator login

## Existing OIDC reverse proxy

Set `operator.proxy_origin: https://panel.example` (origin only, no trailing path)
to use browser SSO supplied by an existing reverse proxy. Leave `client_id` and
`redirect_uri` empty; proxy mode and the direct PKCE client are mutually exclusive.
Set `identity.admin_organization_id` explicitly; proxy mode cannot start without it.
The browser automatically loads diagnostics after entering through SSO. Reconnect
reloads `/admin`; Disconnect clears this panel's view and polling, not the shared
SSO session. No access token is returned to JavaScript or stored in browser storage.

Protect `/admin` and `/admin/` with the proxy's OIDC middleware. It must overwrite
the upstream `Authorization` header with the authenticated user's **access token**.
The access token must target the configured API project and include the operator's
role grants. Loom validates issuer, audience, activity and expiry through its
existing authenticator, subject to the normal bounded
[permissions cache](identity-cache.md).

For this browser path only, Loom takes the organization from
`identity.admin_organization_id` and maps only roles granted in that organization
to permissions. `diagnostics.read` is still required. A resource-owner claim is
not required and does not select the panel organization. Foreign-organization
roles do not grant access. Service identities and delegation/session headers are
rejected. Cached token claims are not rewritten: the normal `/v1` API, delegation
and direct PKCE flow retain their existing organization contract.
A warm cache can delay role-removal visibility until its TTL or an administrative
purge.

Browser fetches include same-origin cookies and use only these read-only aliases:

- `GET/HEAD /admin/api/diagnostics`
- `GET/HEAD /admin/api/diagnostics/health`
- `GET/HEAD /admin/api/diagnostics/runs`
- `GET/HEAD /admin/api/diagnostics/runs/{id}`

Aliases reuse the normal diagnostic handlers, authorization and recovery-mode
rules. Other administrative endpoints are not exposed through this browser path;
writes return 405. Mismatched Origin and cross-site Fetch Metadata are rejected.
API fetches do not follow login redirects. An expired proxy session clears the panel and polling and asks for Reconnect; response data is not cached. Keep normal
`/v1/...` routes outside the browser-login middleware so service Bearer calls
remain machine-readable. Cookies alone never authenticate direct Loom requests.

When the existing OIDC client and Loom API application share a Zitadel project,
keep standard `openid profile email` scopes and enable **Assert Roles on
Authentication** in the project's general settings. Configure `identity.audience`
as that shared project ID. No Zitadel-specific scope is required for this panel
mode. Check the installed introspection response contains the expected audience
and operator roles under the configured admin organization. Other project layouts
must request the correct API audience and roles explicitly.
Renew the proxy session after changing role assertion settings. This mode does
not remove the API application's introspection credentials.

## Direct public PKCE client

The `/admin` shell is public. Sign in uses OAuth Authorization Code with S256 PKCE. The backend discovers endpoints from `identity.issuer`; discovery issuer and endpoint origins must match that trust root. It keeps a random verifier and a browser-bound, single-use state for five minutes, with at most 1024 pending logins per process. Restarting the process cancels pending logins.

The callback lands on `/admin`. JavaScript removes the code and state from the address bar before exchanging them through a same-origin POST. A SameSite=Lax HttpOnly cookie binds the exchange to the browser. The server validates the returned access token through the existing identity authenticator and requires `diagnostics.read`. This does not grant roles or bypass audience/organization checks. Tokens remain in page memory; disconnect, expiry, reload and API authorization denial clear access. Refresh tokens and ID tokens are ignored. Disconnect clears this panel session; it does not sign out of Zitadel.

### Register the client

1. In the configured Zitadel project, register a separate public **User Agent** OIDC application using Code with PKCE and authentication method **none**. Do not reuse `identity.client_id` or `identity.client_secret`: those credentials belong to API introspection. No client secret goes to the page or the login configuration.
2. Register the exact approved HTTPS redirect URI `https://<panel-host>/admin`. No wildcard, query or fragment. For a local QA issuer and panel, enable Zitadel development mode and use a loopback callback such as `http://localhost:58380/admin`.
3. Use the project ID as `identity.audience`. Login requests `openid`, `urn:zitadel:iam:user:resourceowner`, `urn:zitadel:iam:org:project:id:<project-id>:aud`, and `urn:zitadel:iam:org:project:roles`. Enable project role assertion and check the application role assertion settings. Verify that the access token/introspection response contains the user resource-owner organization ID, user project roles, and the intended API audience. Authorization requires that organization ID; it does not infer organization membership from a role alone. Assign an appropriate project role to the user and map it to `diagnostics.read` in `identity.role_permissions`. Verify the intended organization; no cross-organization access is implied.
4. Set `operator.client_id` to the public client ID and `operator.redirect_uri` to the registered URI. Both empty values disable login; partial configuration fails validation. The direct login path stays disabled until this client is configured.
5. Serve the panel through TLS. Configure proxies/access logs to omit `/admin` callback query strings and exchange bodies; authorization codes, access tokens and introspection secrets must not be logged. The application does not log login payloads or raw issuer errors. Multi-replica deployments require sticky routing for the five-minute transaction, or a fresh sign-in if the callback reaches another process.

### Direct PKCE acceptance and rollback

Focused tests use an in-process stub issuer that serves discovery and validates the S256 verifier at token exchange. The stub exists only in tests and is not a production authentication option. Tests must prove correct client/redirect/scope, browser binding, state expiry and single-use exchange, safe issuer failures, and fresh authorization before an access token is returned.

For direct PKCE, verify authorized and unprivileged users, callback rejection/expiry, role revocation, overview/filter/detail/disconnect and absence of persistent browser tokens. See [QA](qa.md). Disable this direct path by clearing its client_id/redirect_uri and restarting. Proxy SSO is a separate mutually exclusive configuration; it does not need a public PKCE application.

References: [Zitadel application registration](https://zitadel.com/docs/guides/manage/console/applications-overview), [Zitadel quick start](https://zitadel.com/docs/guides/start/quickstart), [Zitadel OAuth scopes](https://zitadel.com/docs/apis/openidoauth/scopes).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
