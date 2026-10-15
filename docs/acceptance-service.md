# Module acceptance service

`clx-acceptance` is an isolated fixture for the module protocol. It is not a production adapter. It uses two fixed bearer tokens; it does not validate Zitadel signatures, expiry, organizations, or roles. The harness must still authorize the initiating user and filter module permissions.

## Start the fixture

Set distinct local QA values for `CLX_QA_READ_TOKEN` and `CLX_QA_WRITE_TOKEN`. Set `CLX_DEV_DB_PASSWORD` because the base Compose file requires it. Do not use production secrets.

```sh
docker compose -f compose.yaml -f examples/acceptance.compose.yaml --profile acceptance up --build -d acceptance-service
```

Run this command from the repository root. The override uses the root build context. It creates an `acceptance-state` volume and exposes port 8090 on localhost only. The fixture shares the base Compose network, `loom-dev_default`.

For a harness in this Compose project, include `loom-dev_default` in `discovery.allowed_networks`. The registration label is one line:

```yaml
clx.harness.service: http://acceptance-service:8090/manifest
```

The Docker metadata source must see this container. Polling discovers the registration; no event stream is needed. Example polling and freshness settings are 300 and 3600 seconds. For a different project/network, use its actual allowed network name.

A harness request must delegate one of the fixture's configured token strings for module calls. The read token allows reads, discovery, and blob downloads. The write token also allows preparation, writes, and fault controls. A fixed fixture token does not replace the harness's own authentication. Tests use a trusted registry source and an explicit principal to isolate the module contract from Zitadel.

## Contract and acceptance scenarios

`GET /manifest` publishes a static `fixture` module and a dynamic `fixture_dynamic` module. `qa.access` enables the module category. `qa.read` enables static `read` and dynamically discovered `lookup`; `qa.write` enables the confirmed `write` action.

Reads take `{"query":"value"}` and return a result plus a text blob descriptor. The descriptor includes MIME type, byte length, and SHA-256. `GET /blob` returns the original bytes under the delegated token. A normal harness import returns an artifact handle to the script.

Writes use `service_id` confirmation. `POST /prepare` returns a stable confirmation ID bound to the token, run ID, operation ID, and normalized arguments. `POST /write` requires the matching confirmation and uses the operation ID as its idempotency key. A conflicting argument, run, or token is rejected. `POST /status` reports the committed result without applying a write.

To lose the next write reply after the state has been saved:

```sh
curl -X POST http://127.0.0.1:8090/qa/faults \
  -H "Authorization: Bearer $CLX_QA_WRITE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"drop_next_write_reply":true}'
```

The next successful write closes its connection after commit. The harness should reconcile through status. Restart the service without deleting its volume to test persisted deduplication:

```sh
docker compose -f compose.yaml -f examples/acceptance.compose.yaml restart acceptance-service
```

Focused HTTP tests cover manifest registration, permission filtering, dynamic discovery, schema rejection, blob bytes/hash, confirmation rejection, duplicate preparation and execution, lost-reply reconciliation, restart recovery, and read-token write denial:

```sh
go test ./internal/acceptance ./cmd/clx-acceptance
```

## Independent local QA

The HTTP API run path was checked against the Docker fixture with real PostgreSQL, production registry/ModuleBridge/coordinator and artifact import, actual Inline JavaScript, and a deterministic Responses HTTP provider. Dynamic lookup and imported blob passed. The write path passed approval, duplicate approval, lost reply, retained-volume Docker restart and explicit resume/status recovery. Counters showed one write RPC and one status RPC; completed-run execution caused no additional inference or effect.

See [QA report and replay sources](validation/2026-10-11-acceptance-qa/REPORT.md). This uses test authentication and an explicit trusted registry endpoint. Live Zitadel, Docker metadata discovery, River scheduling, supervisor IPC and complete application startup remain outside this scoped QA.

## Limits

The fixture performs no business operation. Its committed value is test state. Preparation expires after five minutes. Applied operation records remain in the volume; capacity is bounded to 1000 records and 1 MiB. This exceeds the manifest's declared 24-hour deduplication window only while the volume is retained. Clearing the volume removes this guarantee. There is no automatic cleanup or multi-process writer support.

State writes synchronize a temporary file and rename it before acknowledging a write. Parent-directory synchronization is not implemented; power-loss durability has not been proven. Restart tests cover ordinary process restart. There is no cancellation endpoint or arbitrary format/media support; the exported artifact is a fixed text blob.

The fixture's HTTP contract is tested through the real module registry and write backend. Focused tests also pass with the race detector on Linux. The merged Compose configuration validates; the pinned Linux image builds and starts with a read-only root filesystem, and preparation writes successfully into an anonymous state volume. The temporary container was stopped and removed after this check. Running it on Daniel's server, connecting it through server Docker discovery, and exercising the full live Zitadel/provider path are separate acceptance steps.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
