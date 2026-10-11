# Zitadel integration QA — 2026-10-11

Local behavioral checks: **27 PASS**, zero failed harness assertions in the final run. This is identity + HTTP handler + PostgreSQL acceptance. It is **not** full queue/model/sandbox runtime acceptance or production acceptance.

Tested Zitadel matches `../server_configs/configs/zitadel/docker-compose.yml`: **v4.16.3**, image `ghcr.io/zitadel/zitadel@sha256:f3738fd984131d3f02e386d37fa480d1a30e42b7d4202e2b322f12f7cf556b64`. PostgreSQL 17 Alpine image: `postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24`. Go 1.27.2 runs the repository's actual `identity.Authenticator`, `api.New`, migrations and Store. HTTP handlers use `httptest` requests/recorders; token endpoints, discovery, JWKS and introspection are real HTTP requests to actual Zitadel, and SQL uses actual PostgreSQL.

## Evidence

- [api-identity.txt](api-identity.txt): 25 assertions, including real human JWT, opaque token, actual expiration, signature/issuer/audience failures, ID-token rejection, role mapping, trusted service delegation, permission intersection, encrypted session verification, default chat persistence and 401/403 behavior. Delegated/default chat response equality is checked.
- [provider-outage.txt](provider-outage.txt): 2 assertions after stopping only the isolated Zitadel container. Auth returns `ErrUnavailable`; API returns HTTP 503.
- [capabilities.txt](capabilities.txt): installed-version provider probes and their unresolved failures.
- [driver.go.txt](driver.go.txt), [outage.go.txt](outage.go.txt): exact final test drivers. They read temporary credential files and never print their values.
- [stand.compose.yaml](stand.compose.yaml): sanitized stand configuration, all images pinned by digest.
- [provision.ps1](provision.ps1): role/user/machine/grant fixture provisioning. Run only against a disposable initialized stand.

## Required token scopes

The successful tokens requested these scopes:

```text
openid
urn:zitadel:iam:org:project:id:<project-id>:aud
urn:zitadel:iam:org:project:roles
urn:zitadel:iam:user:resourceowner
```

Without the resourceowner scope, this version returned roles but no `urn:zitadel:iam:user:resourceowner:id` introspection claim. The harness then cannot map the roles to an organization. This is a deployment prerequisite, not a reason to infer organization from untrusted input.

## Stand-only bootstrap

The stand uses a bootstrap machine PAT written to `/data/admin.pat`. The PAT creates a project (`projectRoleAssertion=true`), Basic-auth API application for introspection, two roles, a human, and a machine configured to issue JWT access tokens. The machine secret's returned `clientId`, not its user ID, is used for `client_credentials`.

Human credentials were obtained through a separate OIDC application with authorization-code, refresh-token and token-exchange grants enabled. Only in this disposable stand, impersonation policy was enabled and the machine was assigned `ORG_OWNER` and `ORG_END_USER_IMPERSONATOR`. The `user_id` exchange uses that authorized machine actor and returns real human access tokens; no browser login flow was tested. These privileged bootstrap settings are not a production recommendation.

For the expiration case, stand OIDC access-token lifetime was set to five seconds; the issued token reported `expires_in=4`. The lifetime was restored afterward. A final live deactivation invalidated the user's JWT and stored delegated session. Reactivation does not restore previously invalidated tokens; a fresh token is needed.

## Reproduction commands

From repository root, create `management.local/zitadel-qa/`, copy the compose template there as `compose.yaml`, and generate isolated credentials in environment variables `QA_POSTGRES_PASSWORD`, `QA_MASTERKEY` (32 characters) and `QA_HUMAN_PASSWORD`. Do not add that directory to Git.

```powershell
docker compose -p clx-zitadel-qa-20261011 -f management.local/zitadel-qa/compose.yaml up -d db
# Wait for PostgreSQL readiness before starting init.
docker compose -p clx-zitadel-qa-20261011 -f management.local/zitadel-qa/compose.yaml up -d zitadel
# Wait until http://localhost:18080/.well-known/openid-configuration responds.
```

Bootstrap via `/management/v1/projects`, `/projects/<project-id>/apps/api`, `/projects/<project-id>/apps/oidc`, `/admin/v1/policies/security` and `/management/v1/orgs/me/members`. Save responses only inside the ignored stand directory. `provision.ps1` expects `project.json` there and the bootstrap PAT at `data/admin.pat`. It saves `provisioned.json`; application responses go in `app.json` and `oidc-app.json`.

Machine token request to `/oauth/v2/token`: `grant_type=client_credentials`, returned machine `clientId`/`clientSecret`, scopes above. Save response as `service-token.json`.

Human token requests use OIDC application HTTP Basic credentials and these fields:

```text
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<human-user-id>
subject_token_type=urn:zitadel:params:oauth:token-type:user_id
actor_token=<machine-access-token>
actor_token_type=urn:ietf:params:oauth:token-type:access_token
requested_token_type=urn:ietf:params:oauth:token-type:jwt
scope=<same scopes>
```

Save as `user-token.json`; repeat with `requested_token_type=urn:ietf:params:oauth:token-type:access_token` as `opaque-token.json`. Mint the five-second JWT into `expired-token.json`, wait beyond expiry and restore settings. Copy the drivers into the ignored directory. Set `CLX_QA_DSN` to the stand's PostgreSQL loopback DSN (`localhost:15433`, database `postgres`). It must refer to a disposable database: the driver migrates it and creates real users/chats/settings.

```powershell
go run ./management.local/zitadel-qa/driver.go
# This driver deactivates the disposable human after the happy-path checks.
docker stop -t 5 clx-zitadel-qa-id-20261011
go run ./management.local/zitadel-qa/outage.go
docker compose -p clx-zitadel-qa-20261011 -f management.local/zitadel-qa/compose.yaml down --volumes
```

## Open gates

**Provider capability probe failed:** exchange JWT and opaque tokens both received RFC7009 revoke HTTP 401 `invalid_client`, `token was not issued for this client`, even though introspection `client_id` matched the Basic-auth OAuth client. No successful token-level revocation is claimed. This needs a focused provider-version investigation with an ordinary authorization-code access token as a comparison.

**Refresh support unproven:** an `offline_access` machine actor followed by human exchange returned `actor_token invalid`, including a repeated actor with `expires_in=43199`. No usable refresh token/refresh grant was established. Harness refreshable exchange remains intentionally unsupported. Current online introspection and deactivation checks passed.

**Not tested:** browser authorization-code + PKCE, cross-organization delegation, live role removal after issuance, signing-key rotation, actual runtime run reauthorization after queue restart, model inference, Docker discovery/service-classifier integration, and production host/network/TLS configuration. Existing unit/integration tests are not substituted for these installed-version scenarios.

## Cleanup and limits

Only containers `clx-zitadel-qa-db-20261011`, `clx-zitadel-qa-id-20261011` and network `clx-zitadel-qa-net-20261011` were created. Docker's default address pool was exhausted, so the isolated network used explicit subnet `10.254.250.0/24`. Existing networks/containers/volumes were not changed. Stand resources and credential files were removed after evidence collection. No production server was contacted.

Official versioned setup/API sources used for bootstrap: [setup defaults](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/cmd/setup/steps.yaml), [management API schema](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/management.proto), [app enums](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/app.proto). Current [token-exchange guide](https://zitadel.com/docs/guides/integrate/token-exchange) informed probes; current docs were not treated as proof for v4.16.3.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk