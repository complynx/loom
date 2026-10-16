# Configuration and runtime boundaries

This page explains where configuration lives and when it takes effect. Use [Startup](startup.md) for migrations and keys, [Local setup](running.md) to start development, and [Operations](operations.md) to maintain an installation.

## YAML and environment

YAML configures connections, issuer/audience trust, provider credentials, Docker discovery, encryption keys, file processing and hard resource ceilings. See the [supported fields](../internal/config/SUPPORT.md) and [deployment template](../deploy/config.example.yaml).

The loader expands `${NAME}` in YAML string values. Missing variables, unknown YAML fields and invalid values stop startup. Environment variables do not override arbitrary YAML fields: a field must reference the variable. `CLX_CONFIG` selects the file; `--config` overrides that path. The `migrate` command can use `CLX_DATABASE_DSN` without loading a complete configuration file. Secrets are not returned by the administrator API and do not enter JavaScript.

PostgreSQL holds the model catalog and capabilities, tariffs, defaults, quotas, context budgets, history retention and disclosure policy. Bootstrap settings seed the database only when settings are absent. Restarting with changed bootstrap YAML does not overwrite the current administrator revision.

`PATCH /v1/admin/settings` accepts a complete typed object with the current `version` in its body. It is not JSON Merge Patch or an ETag contract. A version mismatch returns 409. The update and audit commit together. Changing another user's model or effort requires a separate permission.

A run stores its settings and tariff snapshot. Admission uses current policy. Later script/model boundaries apply the stricter admitted, current and deployment limits. An ordinary settings update does not interrupt a call already in progress. Changing credentials, provider bindings or hard ceilings requires a restart.

History retention is fixed for a resource when it is created; a settings patch does not shorten existing retention. Temporary file expiry uses deployment `artifacts.ttl_seconds` (default 86400 seconds), independently of active runs. `bootstrap_settings.artifact_retention` is legacy compatibility metadata and does not set that expiry. See [file lifetime](file-tools.md).

## Runtime

The supported runtime is Linux/amd64 with PostgreSQL, River and separate sandbox/file workers. File processing is optional. Docker discovery uses a restricted proxy; the API and JavaScript do not receive the Docker socket. Workers run without root privileges, network access or secrets, within container limits. Goja alone is not a security boundary.

Startup serializes migrators and checks the schema and session keys before workers/API start. Migrations need DDL permissions in a dedicated database, not PostgreSQL superuser permissions. A separate migration job can replace `database.migrate_on_start`. Instances must use compatible schema versions and agreed key material. Stop old workers before an incompatible upgrade.

Files are temporary. An external service remains the authoritative source for a long-lived file. Back up the database and session keys; do not treat temporary artifacts as permanent storage. Restore through the [recovery barrier](recovery-mode.md).

## Operating contracts

- Settings and permissions are checked again at effect boundaries. Consent does not replace authorization.
- Reconcile an unknown write with the service using the original operation ID. A new run does not recover that operation.
- SIGINT/SIGTERM starts draining with a configured deadline of at most five seconds. Measure process exit separately from Docker notification latency.
- Logs, metrics and OTel are diagnostic channels. They do not replace durable audit or the credit ledger.
- The [development track](development-track.md) lists remaining work and acceptance limits. QA documents define procedures; run reports belong outside source control.
