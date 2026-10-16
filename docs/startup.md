# Startup and operator configuration

`loom serve` applies application and River migrations before opening the API or
starting workers. YAML `database.migrate_on_start` defaults to `true`. Set it to
`false` when a separate migration job owns upgrades. `loom migrate` remains
available and needs only `CLX_DATABASE_DSN`.

The database and a dedicated database owner must already exist. Automatic
migrations require that owner's DDL privileges, confined to the Loom database;
they do not require a PostgreSQL superuser. Stop older Loom workers before an
upgrade. A PostgreSQL advisory lock serializes Loom migrators, with a five-minute
deadline. Migration errors stop startup. Completed migration steps remain in
their framework histories and can be retried. A newer application or River schema
is rejected; startup never runs down migrations. Back up before schema upgrades.

Dynamic settings are seeded only when absent. Restarting with changed bootstrap
YAML does not overwrite administrator changes in PostgreSQL.

## Persistent data and session keys

Create a persistent data mount for keys and temporary artifacts. The unprivileged
runtime creates missing key parent directories. On a fresh database without any
stored auth sessions it atomically creates a private 32-byte session encryption
key at `identity.encryption_key_file`. Existing keys are validated and preserved.
A key file can contain 32 raw bytes or their standard base64 encoding. The
development Compose profile instead requires a pre-created read-only key file;
follow [local setup](running/setup.md).
An invalid key, or a missing key with stored sessions, stops startup. Restore the
original key; do not delete sessions to bypass this check.

Docker bind mounts may initially belong to root. A short-lived init container can
run `loom prepare-data --root /data --uid 10001 --gid 10001` with only the data
mount and no network or secrets. This prepares `/data`, `/data/keys` and
`/data/artifacts`; it does not recursively change existing artifact ownership.
Run the API as UID/GID 10001 with a read-only root filesystem. Keep host Compose
configuration and env files outside this API-owned private data directory.

Retain and back up encryption keys across container replacement. Multiple API
instances must share the same key material. See [key rotation](session-key-rotation.md)
and [restore recovery](recovery-mode.md). Recovery mode performs neither automatic
migrations nor key generation and requires an existing restored installation.

## Zitadel application credentials

An API application can authenticate introspection using its downloaded RSA JSON
key. Supply exactly one source: `client_secret`, `client_key_file`, or
`client_key_base64`. For a CI/host environment secret:

```yaml
identity:
  issuer: "${CLX_ZITADEL_ISSUER}"
  audience: "${CLX_ZITADEL_AUDIENCE}"
  client_key_base64: "${CLX_ZITADEL_CLIENT_KEY_B64}"
  encryption_key_file: /data/keys/session-v1
  encryption_key_id: key-v1
```

Encode the complete downloaded JSON as standard base64. Whitespace (including
spaces, tabs, LF and CRLF) is removed before decoding; other invalid characters
are rejected. The size limit also bounds the input before cleanup. `clientId`
and `keyId` come from that JSON. An explicitly configured `client_id` must match.
Service-account JSON is rejected. The decoded key is bounded to 64 KiB and stays
in process memory. Introspection uses fresh short-lived RS256 client assertions,
as specified by [Zitadel](https://zitadel.com/docs/guides/integrate/token-introspection/private-key-jwt).
The application credential is separate from Loom's session encryption key.
Base64 is an encoding, not encryption. Docker administrators can inspect container
environment variables. Never print secrets in workflow logs or store filled env
files in Git. Changing a credential requires restarting the process.

## Container updates

CI publishes `complynx/loom:latest` and `complynx/loom:file-worker-latest` before
sending its deployment event. Commit tags remain available for operator rollback.
Your deployment system pulls and starts the selected images. Container startup owns
configuration validation, migrations and session-key checks. For a deployment with
an init service, that service owns data directory preparation. The development
Compose profile uses a pre-created key file and its image-prepared artifact
directory. Configure an API healthcheck with
`["CMD", "/usr/local/bin/loom", "healthcheck"]`; it requests the local `/readyz`
endpoint with a three-second timeout and fails on non-200 responses. Use
`--address 127.0.0.1:PORT` when the API listens on another port. Allow five minutes
of healthcheck start period for migrations. Readiness checks PostgreSQL, discovery
trust, the sandbox and the enabled file processor without paid provider calls.

Compose marks a failed readiness check unhealthy; it does not restart that
container or guarantee that a generic rollout script waits for readiness. Check
`/readyz` after rollout. The container checks dependency availability, not equality
of the API and file-worker image revision labels. An unsuccessful migration must
not trigger an automatic schema downgrade.

Hostnames, Traefik labels, server workflows and filled server configuration belong
to the operator's deployment repository or local private files. This repository
contains reusable examples and the application contract only. See
[CI](ci.md), [running locally](running.md), and [observability](observability.md).

## Browser panel behind OIDC ingress

The panel can reuse an existing OIDC reverse proxy by setting `operator.proxy_origin`.
See [operator login](operator-login.md) for cookie-based browser diagnostics,
required access-token claims, read-only routing and the direct PKCE alternative.
