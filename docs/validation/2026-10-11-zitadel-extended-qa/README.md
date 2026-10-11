# Extended live Zitadel QA — 2026-10-11

**18 PASS, zero failed final assertions.** This adds focused actual-provider checks to the earlier [27-check identity/API stand](../2026-10-11-zitadel/README.md). It does not prove queue restart, browser login, real-model inference, or production acceptance.

The disposable stand used Zitadel **v4.16.3**, pinned image digest `f3738fd984131d3f02e386d37fa480d1a30e42b7d4202e2b322f12f7cf556b64`, and the pinned PostgreSQL 17 image from the earlier stand. Actual issuer, JWT signature, JWKS and introspection HTTP requests were used with the repository's `identity.Authenticator`, `VerifySession` and HTTP handler. No identity responses were mocked.

## Evidence

- [live-boundaries.txt](live-boundaries.txt): **9 PASS**. Individually valid JWTs from two actual organizations; cross-organization service delegation rejected. Removing a real user grant after token issuance removes permissions from that same JWT and its previously sealed delegated session through live introspection. Restoring the grant with a fresh credential restores the role. A real five-second access token expires; a new authenticator rejects the persisted expired session; a fresh user credential and newly sealed session authorize successfully.
- [rotation-outage.txt](rotation-outage.txt): **9 PASS**. The documented WebKeyService creates and activates an actual RSA signing key. A newly issued JWT has the new `kid`. The existing authenticator, whose JWKS cache was populated before key creation, accepts the rotated token by fetching the new key. The previous token remains valid while its old public key is retained. A fresh authenticator also accepts the new key. After stopping the actual issuer, both warmed and cold authenticators return `ErrUnavailable`; the HTTP handler returns 503 despite cached valid signing keys.
- [driver.go.txt](driver.go.txt) and [rotation-outage.go.txt](rotation-outage.go.txt): final repeatable drivers. Credential material is read from ignored scratch and never printed.
- [provision.ps1](provision.ps1) and [stand.compose.yaml](stand.compose.yaml): disposable fixtures and pinned stand definition.

## Reproduction

Run from the repository root on a machine with Docker and the Go toolchain from `go.mod`. This procedure mutates only the disposable stand and stops it during the final test. Ports **18090** and **15443** and subnet **10.254.249.0/24** must be free. Create the ignored `management.local/zitadel-extended-qa/data` directory and copy `stand.compose.yaml` into its parent as `compose.yaml`.

Generate stand-only values for `QA_POSTGRES_PASSWORD`, `QA_MASTERKEY` (32 characters) and `QA_HUMAN_PASSWORD`; keep them private in the shell environment.

```powershell
docker compose -p clx-zitadel-extended-qa-20261011 -f management.local/zitadel-extended-qa/compose.yaml up -d db
# Wait for PostgreSQL readiness.
docker compose -p clx-zitadel-extended-qa-20261011 -f management.local/zitadel-extended-qa/compose.yaml up -d zitadel
# Wait for http://localhost:18090/.well-known/openid-configuration.
& ./docs/validation/2026-10-11-zitadel-extended-qa/provision.ps1
Copy-Item docs/validation/2026-10-11-zitadel-extended-qa/driver.go.txt management.local/zitadel-extended-qa/driver.go
go run ./management.local/zitadel-extended-qa/driver.go
Copy-Item docs/validation/2026-10-11-zitadel-extended-qa/rotation-outage.go.txt management.local/zitadel-extended-qa/rotation-outage.go
go run ./management.local/zitadel-extended-qa/rotation-outage.go
docker compose -p clx-zitadel-extended-qa-20261011 -f management.local/zitadel-extended-qa/compose.yaml down --volumes
```

Remove only the owned ignored scratch directory afterward. The drivers mutate grant state and intentionally stop the issuer; provision a fresh stand for a clean repeated run. Run each driver separately with `go run <file>`, because both are standalone main packages.

## Provider prerequisites and limits

The disposable bootstrap PAT creates project roles, Basic-auth introspection and OIDC token-exchange applications, a machine and two humans in different organizations. Fixture impersonation is explicitly enabled and the machine gets `ORG_OWNER` / `ORG_END_USER_IMPERSONATOR` in both disposable organizations. The second human has no project role; its JWT independently authenticates with the required project audience and its own resource-owner organization, which is sufficient to prove rejection at the delegation organization boundary. The first human has the real project role used in removal tests.

The tested WebKeyService requires the instance's `web_key` feature. The initialized v4.16.3 stand exposed active/initial web keys and accepted its documented `/v2beta/web_keys` and `/v2beta/web_keys/{id}/activate` endpoints. We deliberately activated the new key immediately to exercise a cold unknown `kid` in a warmed application cache. Official API comments advise allowing the JWKS cache duration before normal operational activation. This disposable boundary probe is not a production rotation schedule.

The expiry check creates a new authenticator around a saved encrypted envelope in memory. It proves identity session rechecking across a fresh authenticator; **it does not execute a queue, process restart, persisted database session recovery or run reauthorization endpoint**. Other verification lanes must supply those proofs. No automatic refresh-token recovery is claimed. Previous revocation/refresh-token provider capability failures remain unresolved. Browser authorization-code/PKCE and real model integration remain untested here.

An initial driver attempt used POST for already-existing OIDC settings and got provider HTTP 409 after the first six successful checks. The final driver uses PUT. The final 9-check run completed successfully; this fixture correction required no product change.

Only owned containers `clx-zitadel-extended-qa-db-20261011`, `clx-zitadel-extended-qa-id-20261011` and network `clx-zitadel-extended-qa-net-20261011` were created. They and all scratch credentials were removed after evidence collection. Existing Docker resources were not changed. No production server, production credentials or paid model was used.

Pinned primary sources: [management API](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/management.proto), [OIDC settings API](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/admin.proto), [WebKeyService v2beta](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/webkey/v2beta/webkey_service.proto), [WebKeyService v2](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/proto/zitadel/webkey/v2/webkey_service.proto), [pinned defaults](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/cmd/defaults.yaml).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
