# Deploy loom on complynx.net

Repository rename: use the coordinated [Loom receiver/Alloy patch](validation/2026-10-12-loom-rename-server.patch). Historical QA patches preserve their original names. Deploy the updated receiver before enabling `updated-loom` dispatch.

Update 2026-10-12: the [fixture browser follow-up](validation/2026-10-12-browser-qa/README.md) passed login, filter, detail, refresh and disconnect after Daniel installed the test CA and restarted Codex. The earlier TLS blocker below is historical. Production identity and pilot acceptance remain separate.

This runbook prepares a deployment. It does not record a production deployment. Commands below change the server only when the operator executes them. Use one API replica for the first release. The API contains the River worker; there is no separate queue process.

Local pre-rollout evidence: [whole-code review](validation/2026-10-11-full-review/README.md) and [current manual QA](validation/2026-10-11-full-manual-qa/README.md). Current fixture browser login remains blocked by TLS trust; local acceptance does not replace the production pilot. Readiness tests connectivity, not guaranteed write capacity under ENOSPC/read-only failures.

## 1. Confirm the target and release

Read-only SSH inspection on 2026-10-11 found:

- Host `complynx.net`, login `complynx`, Ubuntu 24.04.4 LTS, kernel `6.8.0-110-generic`, x86_64.
- Docker 29.4.0 and Docker Compose 5.1.3.
- Server configuration checkout `/home/complynx/server_configs`, branch `master`, HEAD `20e9be3ece7b953e5e96563213d13708e76ee864`.
- `traefik-prod` runs Traefik v3.6. Public HTTPS uses entrypoint `web` and resolver `letsencrypt`; the Docker network is `traefik-net`.
- `zitadel` runs v4.16.3, public issuer `https://id.complynx.net`.
- `postgres-postgres-1` runs PostgreSQL 17.9 in `pgvector/pgvector:pg17`. Available vector extension is 0.8.2. Its data bind mount is `/home/complynx/server_configs/data/postgres`.
- Alloy is already running and ingests Docker logs. Its configured destination is Grafana Cloud. The `clx-observability` network is absent on the live host.
- `configs/loom` is absent. The live checkout has no deployed Loom receiver/profile. The prepared local sibling receiver and Alloy patch use `updated-loom` and `loom:9090`; deploy them together with the source workflow rename. They have not been deployed.
- Root filesystem had approximately 90 GiB available. This is a snapshot, not a capacity guarantee.

Recheck before making changes:

```powershell
ssh -i C:/Users/ddriz/.ssh/id_ed25519 complynx@complynx.net
```

Then on the server:

```sh
cd /home/complynx/server_configs
git status --short
git rev-parse HEAD
docker compose version
docker ps --format '{{.Names}} {{.Image}}'
docker network ls
df -h data/postgres
```

Do not overwrite a dirty server checkout. Prepare and review the server profile in `complynx/server_configs`, then update the live checkout to its approved revision. Do not run the global `init.sh` to deploy only this application.

Select an approved loom release after code review, documentation review and QA. Record the source SHA and the published Docker digest. Pin every application container to `complynx/loom@sha256:<digest>`. Do not use `latest` for the first rollout or rollback. The application image includes the API, sandbox, Docker proxy and migrations.

## 2. Select DNS and create the profile

`agent.complynx.net` below is a proposed hostname. It was not registered or approved during this work. Replace it consistently if another hostname is chosen. Create its A/AAAA records for the existing server ingress. Do not add an IPv6 record unless the server accepts HTTPS on that address.

The production callback is `https://agent.complynx.net/admin`. There is no new public port: Traefik terminates TLS and routes to API port 8080. Keep metrics 9090 and Docker proxy 2375 unpublished. The illustrative profile publishes API port 58280 only on loopback for SSH acceptance.

Copy [the profile](../deploy/server/docker-compose.prod.yml) into `/home/complynx/server_configs/configs/loom/docker-compose.prod.yml`. This is a reviewed starting template; it does not create secrets, PostgreSQL roles or a release hook. The profile uses the existing shared database, not the development Compose PostgreSQL.

```sh
cd /home/complynx/server_configs
install -d -m 0750 configs/loom
install -d -m 0700 data/loom
install -d -m 0700 data/loom/keys
install -d -m 0750 data/loom/artifacts
sudo chown 10001:10001 data/loom/artifacts
```

In `configs/loom/.env`, set non-secret values:

```dotenv
CLX_IMAGE=complynx/loom@sha256:<approved-digest>
CLX_PUBLIC_HOST=agent.complynx.net
```

Record the previous digest before every update. Keep configuration paths stable; releases must not replace persistent artifacts or keys.

Create the metrics network before starting either profile:

```sh
docker network inspect clx-observability >/dev/null 2>&1 || docker network create clx-observability
```

The API joins `traefik-net` for PostgreSQL, public ingress and trusted service calls. The proxy uses a separate internal network shared only with the API. The sandbox uses `network_mode: none`, no secrets and a read-only filesystem. Its only API connection is the shared Unix socket. The four-worker sandbox profile needs the configured 128-PID ceiling: Go runtime threads count against it, and the former 32-PID limit caused concurrent script failures in local acceptance. Keep the one-CPU and shared 128-MiB limits separate from this PID budget; shared memory does not guarantee per-worker OOM isolation. See [resource acceptance](validation/2026-10-12-sandbox-resource-qa/README.md). Do not attach the proxy to Traefik or publish its port. Mount the Docker socket only in the limited proxy. A read-only Docker socket mount alone does not limit Docker API commands.

Port 9090 has no public route or host mapping. Because the application binds it on all container interfaces, other containers on the API's shared networks can reach it. Treat membership of those networks as an operator trust boundary. This is not an authenticated metrics endpoint.

The template sets Docker stop grace to six seconds, leaving margin for the application's five-second deadline. It deliberately uses `recreate`: overlapping sandbox supervisors cannot own the same Unix socket, and pending browser login transactions are process-local. A short rollout outage is acceptable for the initial deployment. Keep API/sandbox/proxy image digests identical.

## 3. Create a dedicated database and two roles

Use the existing local database administrator connection. Do not reuse `complynx` or Zitadel credentials as the API database user. Open an interactive session:

```sh
docker exec -it postgres-postgres-1 psql -U complynx -d postgres
```

Create names once; if they exist, inspect ownership rather than dropping or recreating them:

```sql
CREATE ROLE clx_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE ROLE clx_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
\password clx_migrator
\password clx_runtime
CREATE DATABASE loom OWNER clx_migrator;
REVOKE ALL ON DATABASE loom FROM PUBLIC;
GRANT CONNECT ON DATABASE loom TO clx_runtime;
\connect loom
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO clx_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE clx_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO clx_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE clx_migrator IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO clx_runtime;
```

Use `\password` so passwords are not included in shell history or process arguments. Runtime does not need ownership, CREATE, superuser or migration credentials. The migrator owns application and River objects. Default privileges cover future migrations by this same owner. After migration, grant existing tables/sequences as shown in phase 6 and prove runtime can start.

Use the `postgres` alias directly on `traefik-net`, port 5432; avoid the public Traefik PostgreSQL route for application traffic. DSN passwords must be URL-encoded. `sslmode=disable` matches the existing local bridge deployment; this is not encrypted transport across hosts.

Semantic search starts disabled. Current vector search accepts supplied vectors and stores them in JSONB; it requires an installed pgvector extension for distance calculation. There is no Go fallback when that extension is absent. There is no embeddings service. pgvector availability does not require enabling this feature. If enabled later, an administrator may execute `CREATE EXTENSION IF NOT EXISTS vector` **in `loom`**, then verify the enabled API/tool behavior; an extension in another database does not apply here.

## 4. Install private configuration and encryption keys

Copy the release's [config example](../deploy/config.example.yaml) to `data/loom/config.yaml`. Set:

- `discovery.allowed_networks: [traefik-net]`, `poll_seconds: 300`, `freshness_ttl_seconds: 3600`.
- `discovery.docker_api: http://docker-api-proxy:2375`.
- Existing supervisor/socket, artifact and HTTP paths from the template.
- `observability.metrics_listen: :9090`, `log_level: info`, `log_payloads: false`.
- `search.semantic_enabled: false` initially.
- `identity.issuer: https://id.complynx.net`, project audience, API introspection client and trusted admin organization.
- `identity.encryption_key_id: key-v1`, `encryption_key_file: /run/secrets/clx-auth-key`.
- Keep `refreshable_exchange_enabled: false` until the installed-version exchange gate has passed. Normal access-token sessions and explicit reauthorization work without refresh.
- Set roles explicitly. Give a pilot user `harness-user`; grant a separate operator `diagnostics.read`, `settings.read`, `settings.write`, `billing.adjust` and `operations.reconcile` as required. Do not grant destructive `chat.delete` implicitly. Personal memory does not require separate memory roles.
- Keep the reviewed OpenAI default catalog (`gpt-6-luna`, medium) unless the pilot needs a change. Confirm tariff/capability data before enabling paid calls. Internal microcredits are accounting estimates, not an OpenAI invoice.

Create the active encryption key without displaying it:

```sh
umask 077
openssl rand -base64 32 > data/loom/keys/key-v1
sudo chown 10001:10001 data/loom/keys/key-v1
sudo chmod 0400 data/loom/keys/key-v1
sudo chown 10001:10001 data/loom/config.yaml
sudo chmod 0400 data/loom/config.yaml
```

Generate once. Do not regenerate a key on each deployment. Keep a protected, separately stored backup of all key versions. The API runs as UID/GID 10001; that UID must read mounted config and keys and write the artifact directory. The Docker proxy alone runs as root to read its socket.

Create `data/loom/api.env` using an editor or secret delivery channel:

```dotenv
CLX_DATABASE_DSN=postgres://clx_runtime:<URL-encoded-password>@postgres:5432/loom?sslmode=disable
CLX_ZITADEL_ISSUER=https://id.complynx.net
CLX_ZITADEL_AUDIENCE=<project-id>
CLX_ZITADEL_CLIENT_ID=<API-introspection-client-id>
CLX_ZITADEL_CLIENT_SECRET=<API-introspection-client-secret>
CLX_ADMIN_ORGANIZATION_ID=<approved-admin-org-id>
CLX_OPENAI_API_KEY=<project-scoped-provider-key>
```

Create `data/loom/migration.env` with **only** `CLX_DATABASE_DSN` for `clx_migrator`. The `migrate` CLI uses that DSN directly and does not need OpenAI, Zitadel or encryption credentials. Both env files are read by host Compose; keep them owned by the deploy login and mode 0600. Container environment can be inspected by Docker administrators; Docker administration is a trusted role.

Do not put filled configs, env files, auth keys or database dumps in Git. Do not reuse the local real-model QA token without an operator decision. Bootstrap settings seed the database once; subsequent YAML edits do not overwrite stored settings. Change existing dynamic settings through the versioned settings API.

For key rotation, follow [the keyring barrier and batch command](session-key-rotation.md). Every writer must read both keys before switching the active key. Use the runtime role or a restricted operator role with SELECT/UPDATE on `auth_sessions`; migration ownership is unnecessary. Retain old keys for historical backups.

## 5. Register Zitadel applications

Create the harness project/audience and roles in the existing Zitadel organization. Configure an API application/client for the authenticator's introspection credentials. Separately create a public User Agent OIDC application with Authorization Code, S256 PKCE and authentication method `none`.

Register the exact callback `https://agent.complynx.net/admin`, then set `operator.client_id` and `operator.redirect_uri` in the private YAML. No public-client secret is needed. The existing Traefik dashboard OIDC client is not a substitute.

The panel requests these scopes:

```text
openid
urn:zitadel:iam:user:resourceowner
urn:zitadel:iam:org:project:id:<project-id>:aud
urn:zitadel:iam:org:project:roles
```

Enable project/application role assertion as appropriate. Verify the access token/introspection has the API audience, resource-owner organization and project roles. The resource-owner scope is required; a role alone does not establish the organization. Assign the operator's role with `diagnostics.read`. Do not add Traefik's dashboard OIDC middleware to this application: its own code validates bearer tokens and handles panel PKCE.

Ensure Traefik/access logs omit authorization-code query strings on `/admin`, authorization headers and exchange bodies. Verify TLS certificate issuance before sign-in. With one replica the process-local five-minute login transaction needs no shared session store. Restart invalidates pending sign-in. Disconnect clears the panel token but does not log out of Zitadel. See [operator login](operator-login.md) for acceptance cases.

## 6. Migrate before starting execution

From `configs/loom`, validate shape without printing expanded secrets:

```sh
docker compose -f docker-compose.prod.yml config --quiet
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml --profile migration run --rm --no-deps migrate
```

Migration first runs the application Goose migrations, then River migrations. Stop on any failure; do not start API against partially migrated schema. There is no separate River CLI installation. Loom consolidates its pre-release application schema into migration 00001_loom.sql, including personal memory. Use a new application database for this baseline; do not apply it over legacy QA databases with application versions 00001–00012. River keeps its own migration history. Use the embedded release migration rather than copying selected SQL fragments.

Grant existing objects after migration through the administrator connection:

```sh
docker exec -i postgres-postgres-1 psql -v ON_ERROR_STOP=1 -U complynx -d loom <<'SQL'
GRANT USAGE ON SCHEMA public TO clx_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO clx_runtime;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO clx_runtime;
SQL
```

Migrate without the API executing concurrently for this first rollout. For later changes, establish schema compatibility before starting overlapping versions; the default recreate plan avoids overlap. Use a `pre-rollout-hook.sh` in the server profile to run the migration and these grants before each automated release. The hook must honor `--dry-run`, stop on errors and avoid printing secrets. This hook is **a required server configuration task**; the illustrative Compose profile alone does not implement it. The migration service is profile-gated so normal rollout enumeration does not start it as a long-lived service.

Start the support services and then API:

```sh
docker compose -f docker-compose.prod.yml up -d docker-api-proxy sandbox
docker compose -f docker-compose.prod.yml up -d --no-deps api
curl --fail --silent http://127.0.0.1:58280/healthz
curl --fail --silent http://127.0.0.1:58280/readyz
```

The scratch image has no shell/curl. Probe from the host; do not add an invalid `CMD-SHELL` healthcheck. `service_started` does not prove sandbox readiness. Wait for `/readyz` and inspect redacted startup logs. Runtime must succeed with the restricted role. Discovery initially polls and then uses the 300/3600 schedule; registration changes are not instantaneous.

## 7. Connect modules and telemetry

For the first real module, add one opt-in label to that service's approved Compose profile:

```yaml
labels:
  - "clx.harness.service=http://<actual-service-alias>:<port>/<manifest-path>"
```

The alias must belong to the container on `traefik-net`; a label pointing at an arbitrary external URL is rejected. Publish a valid v1 manifest and implement the described authorization, operation IDs, idempotency and status semantics before allowing writes. Check the model sees only methods allowed for the pilot user. Passive events must not create a run. The model acceptance service is a QA fixture; it does not establish acceptance of zns-chatbot or another production service.

Deploy the reviewed sibling Alloy changes after creating `clx-observability`: Alloy must join it and scrape `loom:9090`. Keep the existing Docker log pipeline; a second source duplicates ingestion. Use the existing Grafana Cloud credentials, not new application push secrets. Restart only the Alloy profile when its approved configuration is ready:

```sh
cd /home/complynx/server_configs
bash scripts/rollout.sh --config-name alloy --profile prod
```

Confirm Prometheus `up{job="integrations/loom"}=1`, current snapshot timestamps, dependency gauges and parsed JSON logs in Loki. Import [alert rules](../deploy/observability/alerts.yaml), wire a notification route and send a controlled test. Rule validation does not prove delivery. Keep metrics identifiers bounded; search user/run/operation IDs as log fields. Ensure port 9090 has no external Traefik route.

## 8. Enable CI delivery after the manual pilot

The source workflow publishes `linux/amd64` on approved `main` pushes after quality gates. Configure source repository secrets `DOCKER_HUB_ACCESS_TOKEN` and `SERVER_REPO_TOKEN`; the latter must dispatch to `complynx/server_configs`. Update the server receiver's default branch with `updated-loom` mapping, then deploy its approved checkout/profile/hook.

The receiver currently calls `rollout_all.sh loom`. It uses the image in the profile, **not** the dispatch payload digest. With the immutable pin above, a dispatch alone will redeploy the currently pinned release. For each new release, update the reviewed pin before the rollout, or implement an explicit receiver pin-selection step before enabling unattended updates. Do not claim the existing payload automatically selects the new image. Avoid `latest` if exact rollback matters.

The existing server SSH workflow injects its own secrets. The profile uses host-private env files, so new CLX secrets need not be added to that workflow. Its deployment login needs permission to read these files and the approved profile. The release hook must make migration failure block API rollout. Confirm the sender accepted dispatch, the receiver completed, container image IDs match the approved digest, and `/readyz` is healthy; sender success alone is not deployment proof. See [CI contract](ci.md).

## 9. Back up and rehearse recovery before opening use

Back up database, artifact directory and all encryption keys as one recovery set. Encrypt off-host backups and record release digest, schema version, settings revision and key IDs. Protect restored identity sessions; expiry and live permission checks still apply.

For an initial consistent backup, block new ingress and stop the API (including its worker), leave PostgreSQL running, then take a logical dump and artifact snapshot:

```sh
cd /home/complynx/server_configs/configs/loom
docker compose -f docker-compose.prod.yml stop api
umask 077
docker exec postgres-postgres-1 pg_dump -U complynx -Fc loom > /secure-backup/loom.dump
```

`/secure-backup` is an operator-created protected destination outside the repository. Copy the artifact tree while API is stopped, and store keys separately under equivalent protection. Check command exit status, nonempty dump, checksums and off-host upload. Restart API only after backup completes successfully. Stopping the worker does not roll back an external request already sent; preserve and inspect uncertainty records.

Rehearse `pg_restore` into a separate isolated database with the recorded owner roles, restore artifacts/keys and run read-only database checks before any API worker starts. Never test restore over the shared production PostgreSQL database. Never run destructive disk-full QA on the server's root or PostgreSQL volume.

There is no implemented global post-PITR recovery mode that starts HTTP while withholding all queue dispatch. The `serve` process starts workers. A database rolled back behind an external effect can lack its operation/receipt records. Before starting a restored `serve`, compare provider receipts and downstream operation ledgers with the backup, reconcile externally completed effects and stop uncertain jobs from automatic dispatch through a reviewed recovery procedure. Do not promise exactly-once recovery after PITR. A safe production recovery rehearsal and an operator-approved reconciliation procedure remain a deployment gate; do not improvise SQL that marks uncertain effects successful.

For ordinary retained-state restart, use existing run IDs. `waiting_auth` requires fresh authorization for the same user and actor through `/v1/auth/sessions/{id}/reauthorize`; reauthorization schedules the blocked work according to the saved state. Uncertain inference uses the audited reconciliation endpoint; uncertain writes use their previous operation ID/status endpoint. Do not create replacement runs to bypass uncertainty.

## 10. Pilot, widen access and rollback

Begin with one pilot user, minimal credits, low max steps/output and explicit external-disclosure consent. Initial credits are zero. Grant credits through the audited billing adjustment API, not direct balance SQL. The model runs only after policy allows disclosure. Do not turn on provider calls solely for readiness checks.

Prove these observable paths against the approved production configuration:

1. HTTPS and PKCE login succeed for the operator; unprivileged login is denied. Role revocation denies subsequent access after online authorization rechecks.
2. One minimal real-model request completes, charge/reservation settle once and the saved run is readable. Agree the paid-call ceiling before this pilot; extensive mechanics use fixture/manual providers.
3. Chat/default resolution, passive action ingestion, search, personal micro-memory and full memory are user/org isolated. Micro-memory appears in the next model request without separate roles.
4. Questions, approvals, steering, pause/resume, cancellation, previous-run continuation and SSE reconnect work through the actual initiating client.
5. One read module and one approved write/status lookup work. Duplicate client requests return the same operation; no duplicate downstream effect occurs.
6. API restart retains state. Expired or revoked sessions reach `waiting_auth`; queue restart does not bypass authorization, and fresh same-actor reauthorization resumes safely.
7. Healthy shutdown meets the one-second target; failure paths stay within five seconds. Disk-full handling is proven on an isolated bounded QA filesystem and evidence is linked to the release; production receives monitoring and capacity checks, not intentional exhaustion.
8. Artifact ownership, direct model attachment and client streaming enforce MIME, limits and disclosure policy. Bulk blob delivery bypasses JS; bounded `artifacts.read` remains available for script processing.
9. Metrics/logs arrive in Grafana Cloud and at least one alert notification arrives. Backup/isolated restore and external-effect reconciliation procedure are accepted.

Record actual evidence and failures. Local QA reports are inputs to this gate, not proof of the production client, network or notification setup. Widen grants and budgets only after pilot acceptance.

For binary rollback, block new ingress, stop API, set the previous approved digest in the profile and restart compatible support services/API. Do not downgrade database schema blindly. Confirm the previous release can read the current schema; otherwise restore into an isolated recovery database and apply the reconciliation procedure before execution. Keep artifacts, keys, durable events and financial audit. Record run states, reservations, unresolved writes and unknown inference before and after. Disable panel login by clearing both operator fields if that flow alone needs rollback.

## Operator inputs still required

Approved hostname/DNS, release digest, server-config revision/profile/hook, PostgreSQL role passwords, Zitadel project/audience/API client/public PKCE client/admin organization/role grants, project-scoped OpenAI key and budget, first real module manifest, backup destination/retention, alert recipient/route, and the approved recovery procedure. No production secrets, OIDC applications, dispatches, DNS records or containers were changed while writing this runbook.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
