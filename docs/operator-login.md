# Operator login

Choose one browser login mode: existing proxy SSO or a separate public PKCE client.
Both authorize access to the read-only [diagnostics panel](diagnostics.md).
Direct `/v1` requests continue to require Bearer authorization.

## Existing OIDC reverse proxy

Set `operator.proxy_origin: https://panel.example` to the browser origin without a
path or trailing slash. Leave `operator.client_id` and `operator.redirect_uri`
empty; proxy and direct PKCE modes are mutually exclusive. Set
`identity.admin_organization_id` explicitly; proxy mode cannot start without it.

Protect `/admin` and `/admin/` with the proxy's OIDC middleware. Overwrite upstream
`Authorization` with the authenticated user's **access token**, targeting the
configured API audience and including operator role grants. Cookies alone never
authenticate a direct Loom request. Keep `/v1/...` outside browser-login middleware
so machine callers receive API responses instead of login redirects.

Loom validates issuer, audience, token activity and expiry through its normal
authenticator and [bounded permissions cache](identity-cache.md). For this proxy
browser path only, it selects `identity.admin_organization_id` and maps roles
granted in that organization to permissions. `diagnostics.read` is required.
A resource-owner organization claim is not required and does not select the panel
organization. Foreign-organization roles grant no access. Service identities and
delegation/session headers are rejected. Normal `/v1` and direct PKCE authorization
keep their resource-owner organization rule. Cached claims are not rewritten;
role removal may remain invisible until cache expiry or an administrative purge.

The browser uses same-origin cookies with these aliases:

- `GET/HEAD /admin/api/diagnostics`
- `GET/HEAD /admin/api/diagnostics/health`
- `GET/HEAD /admin/api/diagnostics/runs`
- `GET/HEAD /admin/api/diagnostics/runs/{id}`

Aliases reuse diagnostic authorization and recovery-mode restrictions. Other admin
operations are not exposed here; writes return 405. A mismatched `Origin` or
cross-site Fetch Metadata is rejected. API fetches do not follow login redirects.
An expired proxy session clears panel data/polling and asks for Reconnect, which
reloads `/admin`. Disconnect clears this panel, not the shared SSO session.
No access token is returned to JavaScript or saved in browser storage.

When the proxy OIDC client and Loom API application share a Zitadel project, keep
`openid profile email` scopes, enable **Assert Roles on Authentication** in that
project, and use its project ID as `identity.audience`. No extra Zitadel scope is
required for this layout. Verify the actual introspection response contains that
audience and operator roles under the admin organization; renew the proxy session
after role-assertion changes. Other project layouts need explicit API audience and
role requests. Keep the API application's introspection credentials configured.

## Direct public PKCE client

The public `/admin` shell starts OAuth Authorization Code with S256 PKCE. It discovers
endpoints from `identity.issuer`; discovery issuer and authorization/token endpoint
origins must match that trust root. Transactions are browser-bound, single-use and
expire after five minutes; at most 1024 are pending per process. Process restarts
cancel pending logins. Multiple replicas need sticky routing for the transaction,
or the operator must restart sign-in if the callback reaches another process.

### Register the client

1. Register a separate public Zitadel **User Agent** OIDC application with Code/PKCE
   and authentication method **none**. Do not reuse `identity.client_id` or
   `identity.client_secret`: those are API introspection credentials. No client
   secret belongs in the page or operator-login configuration.
2. Register exactly `https://<panel-host>/admin`, without wildcard, query or fragment.
   For local development, a loopback URI such as `http://localhost:58380/admin`
   requires the issuer's development mode.
3. Set `identity.audience` to the project ID. Login requests `openid`,
   `urn:zitadel:iam:user:resourceowner`,
   `urn:zitadel:iam:org:project:id:<project-id>:aud` and
   `urn:zitadel:iam:org:project:roles`. Enable role assertion. Verify the access
   token/introspection includes the resource-owner organization ID, project roles
   granted in that organization, and API audience. Assign and map an operator role
   to `diagnostics.read` in `identity.role_permissions`; roles alone do not supply
   the required organization claim.
4. Set `operator.client_id` and `operator.redirect_uri` to that public client and
   registered URI. Both empty disables this path; partial configuration fails.
5. Serve through TLS. Omit `/admin` callback query strings and exchange bodies from
   proxy/access logs. They can contain codes and tokens. The application does not
   log login payloads or raw issuer errors.

The callback lands on `/admin`. JavaScript removes code/state from the address bar
before a same-origin POST to `/admin/login/exchange`. A SameSite=Lax HttpOnly cookie
binds the exchange to its browser. The server validates the returned access token
through the existing authenticator and requires `diagnostics.read`; login does not
assign roles or bypass audience/organization checks.

Tokens stay in page memory. Disconnect, expiry, reload or API authorization denial
clears access. Refresh and ID tokens are ignored. Disconnect does not sign out of
the issuer. Disable direct login by clearing both operator client fields and
restarting; proxy mode is a separate configuration, not a prerequisite.

<a id="direct-pkce-acceptance-and-rollback"></a>
## Verify login configuration

Check an authorized operator and an unprivileged account, role revocation,
organization isolation, expired/replayed callbacks, and panel reconnect/disconnect.
Confirm tokens are absent from persistent browser storage and proxy logs. Use
[QA](qa.md) for focused scenarios. The in-process test issuer is a test fixture,
not a production authentication option.

References: [Zitadel application registration](https://zitadel.com/docs/guides/manage/console/applications-overview),
[OAuth scopes](https://zitadel.com/docs/apis/openidoauth/scopes).
