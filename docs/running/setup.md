# Setup

[Guide index](../running.md)

## Setup

Before starting, prepare Docker with Compose, a reachable Zitadel issuer, an API audience, introspection credentials, and a model-provider credential. Configure Zitadel roles and their explicit role-to-permission mapping. The example maps `harness-user` and `harness-admin`; these roles must exist in your identity setup. See [identity and permissions](../security/identity-permissions.md).

Copy the development templates:

```sh
cp deploy/config.example.yaml deploy/config.yaml
cp deploy/api.env.example deploy/api.env
```

Fill `deploy/api.env` with the issuer, audience, introspection client, administrator organization and provider key. Review `deploy/config.yaml`. Its omitted model catalog seeds the reviewed OpenAI preset; review the models, capabilities and tariffs before use. To choose another model policy, use [OpenAI configuration](../openai.md) and [supported configuration](../../internal/config/SUPPORT.md).

Set `bootstrap_settings.external_disclosure_default: true` before the first startup to permit provider calls. All provider endpoints count as external, including local endpoints. When disclosure is denied, Loom does not forward requests, history or saved tool results to the provider. After the first startup, change this policy through the authorized settings API; changing bootstrap YAML does not update existing database settings.

Create a separate AES-256 session key and make it readable by the container's UID 10001:

```sh
openssl rand -base64 32 > deploy/auth.key
chmod 600 deploy/auth.key
sudo chown 10001:10001 deploy/auth.key
```

The ownership command applies to a Linux host bind mount. On hosts with different bind-mount permission handling, use the equivalent ACL. The default Compose profile mounts this pre-created file read-only; it cannot generate the key at that path. Keep a separate backup. Without the original key, saved sessions cannot be decrypted. Do not commit filled configuration, env files or keys.

Set the database password in the shell that starts Compose. Use a URL-safe password because Compose inserts it into the database DSN:

```sh
export CLX_DEV_DB_PASSWORD="$(openssl rand -hex 24)"
docker compose --profile app up --build -d
```

Keep that password in private local storage and reuse it on later starts. Changing it does not change the password of an existing PostgreSQL volume. `deploy/api.env` supplies container variables; it does not supply Compose's `CLX_DEV_DB_PASSWORD` interpolation. Alternatively, put the password in the ignored root `.env` file.

Compose runs application and River migrations before the API. Check readiness and inspect startup failures:

```sh
curl --fail http://127.0.0.1:8080/readyz
docker compose --profile app ps -a
docker compose --profile app logs --tail=100 migrate api sandbox docker-api-proxy
```

`/healthz` checks the API process. `/readyz` checks PostgreSQL, the separate sandbox, trusted discovery freshness and the file processor when enabled. `/openapi.json` describes the implemented HTTP DTOs. Readiness does not prove issuer authentication or a successful model call.

Before creating a run, obtain a user access token for the configured issuer/audience and mapped permissions. The example starts users with zero credits. An administrator must grant credits through the audited [billing API](../api.md); a run budget is a spending cap, not a balance grant. Continue with [Client](client.md).

## Boundaries

PostgreSQL and the API bind to loopback. A server installation needs its own TLS and routing. The Docker proxy permits limited discovery reads and removes container environment and mount details. Only the proxy receives the Docker socket; a read-only socket mount alone does not restrict Docker API commands.

The sandbox has no network, secrets, Linux capabilities or writable root filesystem. Compose limits the supervisor and all child processes together to 128 MiB and 128 PIDs. Keep supervisor flags, YAML ceilings and container limits aligned.

The base profile supports HTTP requests and SSE. It does not include WebSocket transport, automatic OAuth token exchange/refresh, or inbound opaque service secrets. Enable the [file worker](../file-worker.md) separately for media tools. [Metrics and logs](../observability.md) can use an operator-selected private collector. Runtime model settings can use provider aliases configured at startup; a new provider binding requires a restart.
