# Operations

This guide describes an installed Loom service. Keep hostnames, secrets, ingress, Compose and host-specific commands in the operator's deployment repository.

## Update an installation

1. Select a reviewed source revision and wait for CI. Upstream main publishes API and file-worker images.
2. Pull the selected images with your deployment system. If you use a deployment hook, check the receiver separately. Event delivery does not prove application startup.
3. Check the actual image IDs, readiness, panel access and startup/migration errors. Record resolved digests when using latest.
4. Check schema compatibility before returning to an older image. Startup never runs down migrations. Back up the database and session keys before schema upgrades. Stop incompatible older workers.

[CI](ci.md) defines upstream tags and hook behavior. [Startup](startup.md) defines migrations and key creation. Restart does not overwrite database settings with bootstrap YAML.

## Diagnose a run

Find its request/run/operation ID and reason through [diagnostics](diagnostics.md). Check PostgreSQL, sandbox/file workers and discovery freshness. A provider outage must not hide a persisted run status. Readiness is not a SQL write or free-space probe.

Use [logs/metrics](observability.md) and [OTel](opentelemetry.md) to locate a failure. Verify backend delivery, not only exporter configuration. Enable payload logs temporarily and account for user data. Do not use user/run IDs as metric labels.

| State | Next action |
| --- | --- |
| waiting_auth | Obtain fresh authorization for the same party and use reauthorize. Do not create a replacement run to bypass denial. |
| paused/shutdown | Read current status, then resume the same run with a stable idempotency key. |
| paused/queue_attempts_exhausted | Restore the dependency, then explicitly resume safe work. |
| reconciling | Verify effect or usage first. Use the same operation ID and service status or privileged reconcile API. Do not repeat an unknown write. |
| waiting_input/waiting_approval | Follow the selected reply mode, expiry and current action binding. |

Cancel does not roll back an external effect. An input receipt proves command persistence, not write completion.

## Authorization and files

The [permission cache](identity-cache.md) has a bounded TTL. Sensitive boundaries require online authority. Use an authorized purge for urgent revocation and verify its result. JWT/JWKS and discovery use separate caches.

Temporary files expire independently of runs, after 24 hours by default. Read a long-lived source again by its service-owned external ID and [file helper](file-tools.md). Do not use the artifact volume as a durable user-file archive.

## Backup and restore

Back up the database and required session-key versions. Rehearse your RPO/RTO on a separate stand. Start a restored database behind the [recovery barrier](recovery-mode.md). Verify external effects since the backup before allowing dispatch. Do not generate a new key to replace a lost key while stored sessions still need it.

## Verify changes

Run the affected [QA scenarios](qa.md). For each client, check authorization, the broker, result delivery and source files. A manual provider proves protocol/runtime behavior, not live-model interpretation.
