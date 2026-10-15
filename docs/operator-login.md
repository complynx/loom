# Operator login rollout

The `/admin` shell is public. Sign in uses OAuth Authorization Code with S256 PKCE. The backend discovers endpoints from `identity.issuer`; discovery issuer and endpoint origins must match that trust root. It keeps a random verifier and a browser-bound, single-use state for five minutes, with at most 1024 pending logins per process. Restarting the process cancels pending logins.

The callback lands on `/admin`. JavaScript removes the code and state from the address bar before exchanging them through a same-origin POST. A SameSite=Lax HttpOnly cookie binds the exchange to the browser. The server validates the returned access token through the existing identity authenticator and requires `diagnostics.read`. This does not grant roles or bypass audience/organization checks. Tokens remain in page memory; disconnect, expiry, reload and API authorization denial clear access. Refresh tokens and ID tokens are ignored. Disconnect clears this panel session; it does not sign out of Zitadel.

## Register the client

1. In the configured Zitadel project, register a separate public **User Agent** OIDC application using Code with PKCE and authentication method **none**. Do not reuse `identity.client_id` or `identity.client_secret`: those credentials belong to API introspection. No client secret goes to the page or the login configuration.
2. Register the exact approved HTTPS redirect URI `https://<panel-host>/admin`. No wildcard, query or fragment. For a local QA issuer and panel, enable Zitadel development mode and use a loopback callback such as `http://localhost:58380/admin`.
3. Use the project ID as `identity.audience`. Login requests `openid`, `urn:zitadel:iam:user:resourceowner`, `urn:zitadel:iam:org:project:id:<project-id>:aud`, and `urn:zitadel:iam:org:project:roles`. Enable project role assertion and check the application role assertion settings. Verify that the access token/introspection response contains the user resource-owner organization ID, user project roles, and the intended API audience. Authorization requires that organization ID; it does not infer organization membership from a role alone. Assign an appropriate project role to the user and map it to `diagnostics.read` in `identity.role_permissions`. Verify the intended organization; no cross-organization access is implied.
4. Set `operator.client_id` to the public client ID and `operator.redirect_uri` to the registered URI. Both empty values disable login; partial configuration fails validation. Production login stays disabled until registration is approved and configured.
5. Serve the panel through TLS. Configure proxies/access logs to omit `/admin` callback query strings and exchange bodies; authorization codes, access tokens and introspection secrets must not be logged. The application does not log login payloads or raw issuer errors. Multi-replica deployments require sticky routing for the five-minute transaction, or a fresh sign-in if the callback reaches another process.

## Acceptance and rollback

Focused tests use an in-process stub issuer that serves discovery and validates the S256 verifier at token exchange. The stub exists only in tests and is not a production authentication option. Tests must prove correct client/redirect/scope, browser binding, state expiry and single-use exchange, safe issuer failures, and fresh authorization before an access token is returned.

[Local real Zitadel acceptance](validation/2026-10-11-panel-login-qa/README.md) records completed browser scenarios; it is not production acceptance. For rollout, sign in with an authorized user and prove overview/filter/detail/disconnect; repeat with an unprivileged user, rejected callback, expired transaction and revoked role. Check that reload needs a new authorization flow and storage has no token. Production rollout remains gated on the same flow against the approved client, approved redirect, project audience, roles and TLS. Disable login by clearing both operator settings and restarting; diagnostic APIs still require valid bearer authorization.

References: [Zitadel application registration](https://zitadel.com/docs/guides/manage/console/applications-overview), [Zitadel quick start](https://zitadel.com/docs/guides/start/quickstart), [Zitadel OAuth scopes](https://zitadel.com/docs/apis/openidoauth/scopes).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
