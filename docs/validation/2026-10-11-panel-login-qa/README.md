# Panel browser login QA — 2026-10-11

This isolated fixture uses actual Zitadel **v4.16.3** and the repository's `api.NewOperatorLogin`, `identity.Authenticator`, API handlers and PostgreSQL-backed diagnostics. The issuer and introspection are real HTTP requests. The fixture does not instantiate a model provider, worker or queue. **Zero OpenAI calls are possible through this harness.**

The stand uses panel `http://localhost:58380/admin`, issuer `http://localhost:58390`, and dedicated PostgreSQL port `58339`. Its `panelqa` application database is separate from the issuer's `zitadel` database. Existing stands on 58280/58290 and PostgreSQL 55439 are untouched. Container names, network and database resources belong exclusively to `clx-panel-login-qa-20261011`.

## Fixture checks

[fixture-checks.txt](fixture-checks.txt) records eight actual-provider checks: auth method NONE, exact approved `/admin` callback, HTTP development mode, signed JWT access tokens, unauthenticated diagnostics HTTP 401, actual legacy-login selection, and verified email state for both humans. A direct GET `/admin/login` returned HTTP 302 to the real issuer with a login transaction cookie.

[seed-pagination.sql](seed-pagination.sql) inserted 51 inert `paused` metadata runs. They contain no model steps, provider attempts or execution jobs. This allows browser pagination and refresh checks without running inference.

## Actual browser results

Root CUA verified the real authorization-code/PKCE flow after the required organization scope fix: reader login returned to `/admin`, and the callback query was removed. The actual diagnostics overview reported 51 paused fixtures; the first page showed 50 runs and Next showed run `20000000-0000-0000-0000-000000000051`. After 30 seconds of automatic refresh, the second page still showed that run. Opening its detail succeeded. [panel-success.png](panel-success.png) records the final authorized panel without credentials.

[authentication-booleans.txt](authentication-booleans.txt) records only safe booleans from actual provider authentication. The login exchange and diagnostics requests succeeded with both organization and `diagnostics.read` present. No access token, password or authorization code is in this evidence.

The actual reader grant was then removed through the real issuer management API while the browser retained its issued access token. The next automatic refresh denied access and cleared overview, runs and open detail; Refresh became disabled. [role-removed.png](role-removed.png) records the cleared panel. A fresh SSO login with the removed grant also returned HTTP 403 `Diagnostics permission required` with no diagnostic data. Safe server evidence shows `success=true`, `org_present=true`, `diagnostics_read=false` for both the old-token diagnostics request and the new login exchange. The reader grant was restored through the actual issuer at **2026-10-11T15:16:18Z** for the retained demonstration stand. Root CUA then verified fresh reader login restored the 50-row first page. Disconnect cleared both tables and detail, disabled Refresh, and left Sign in available. The browser was left at the disconnected `/admin` panel.
The first successful provider callback returned HTTP 403 for the otherwise correctly granted reader. Investigation proved that the login request omitted `urn:zitadel:iam:user:resourceowner`; existing authorization requires this organization claim before mapping project roles. The product login scope and focused fixture assertion were corrected, the isolated harness was rebuilt, and the real browser login then passed. This was a product fix, separate from the provider fixture corrections described below.
## Reproduction

Copy [stand.compose.yaml](stand.compose.yaml) to ignored `management.local/panel-login-qa/compose.yaml`. Create `data/`. Generate private stand-only `QA_POSTGRES_PASSWORD`, 32-character `QA_MASTERKEY`, and `QA_HUMAN_PASSWORD` in the scratch `.env`. Keep secrets out of tracked files and logs.

```powershell
docker compose -p clx-panel-login-qa-20261011 -f management.local/panel-login-qa/compose.yaml --env-file management.local/panel-login-qa/.env up -d db
# Wait until PostgreSQL is ready before initializing Zitadel.
docker compose -p clx-panel-login-qa-20261011 -f management.local/panel-login-qa/compose.yaml --env-file management.local/panel-login-qa/.env up -d zitadel
# Wait for http://localhost:58390/.well-known/openid-configuration.
./docs/validation/2026-10-11-panel-login-qa/provision.ps1
docker exec clx-zitadel-panel-login-qa-db-20261011 psql -U postgres -c 'CREATE DATABASE panelqa;'
```

The [harness source](harness.go.txt) reads private fixture values from ignored scratch. Temporarily copy it to `management.local/panel-login-qa/harness.go`, compile to `harness.exe`, and immediately remove the `.go` copy before repository-wide Go checks. Run the binary from the repository root. It migrates only `panelqa` and listens on `127.0.0.1:58380`. The fixture role `panel-reader` maps to `diagnostics.read`. The public browser client has no secret; a separate Basic-auth API client performs introspection.

Reader username is `panel-reader` and denied username is `panel-denied`; their private generated password is saved only in scratch `state.json`. [remove-role.ps1](remove-role.ps1) removes the reader's actual project grant; [restore-role.ps1](restore-role.ps1) restores it. These scripts affect only the isolated issuer.

## Limits and cleanup

This harness proves the product API and browser shell against actual issuer and database fixtures. It does not execute the full application worker, discovery or sandbox; it does not prove production deployment or model inference. State tampering, replay and expiry are covered by the focused product fixture tests, not repeated against the actual browser. The separately provisioned denied user was not used in browser acceptance; privilege denial was proven with the actual reader after removing its grant. Issuer outage was not performed in this browser lane.

The isolated stand remains running for the requested local demonstration, with the reader grant restored and the browser disconnected. Use [cleanup.ps1](cleanup.ps1) when the demonstration is finished. Stop only the owned harness process, then run the exact owned Compose `down --volumes`. Remove only `management.local/panel-login-qa` after retaining public evidence. Do not prune Docker globally.

During initial bootstrap the issuer started before PostgreSQL accepted connections and exited; it was restarted after readiness. The first provisioning attempt used PUT for the provider's password endpoint and received HTTP 405. The final provisioner uses POST; the isolated stand was reset and successfully reprovisioned. Neither fixture correction changed product code. Browser login then exposed two additional fixture issues: v4.16.3 defaults require a separate Login V2 frontend, so the fixture uses the documented instance feature API to select the built-in legacy login; the v1 password API requires `noChangeRequired: true`, which the final provisioner sets explicitly for both users. Emails were verified from actual provider state. See [pinned defaults](https://raw.githubusercontent.com/zitadel/zitadel/v4.16.3/cmd/defaults.yaml), [instance feature API](https://zitadel.com/docs/reference/api/feature/zitadel.feature.v2.FeatureService.SetInstanceFeatures), and [v1 password API](https://zitadel.com/docs/reference/api/management/zitadel.management.v1.ManagementService.SetHumanPassword).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
