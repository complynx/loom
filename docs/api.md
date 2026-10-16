# HTTP API v1

API paths start with `/v1`. JSON requests require `Content-Type: application/json`.
Unknown object fields are rejected. Timestamps use RFC 3339 UTC. Treat resource IDs
as opaque values. Credit amounts are integers in microcredits. Incompatible
protocol changes require a new base path.

Start with [authentication and routes](api/authentication-routes.md), then
[create a run and submit input](api/runs-input.md). The server exposes wire schemas
at `/openapi.json`. See [Contributing](../CONTRIBUTING.md) for the code map.

Persist an idempotency key before sending a command that requires one. Reuse that
key and the same body after a lost response. Keys contain 1–128 bytes, no leading
or trailing whitespace, NUL, CR or LF. A command receipt confirms durable
acceptance; it does not prove an external effect has finished.

<a id="аутентификация"></a>
## Authentication

Read [Authentication](api/authentication-routes.md#authentication).

<a id="поверхность-api"></a>
## Routes

Read the [route table](api/authentication-routes.md#routes).

<a id="создание-run"></a>
## Create a run

Read [Run creation](api/runs-input.md#create-a-run).

<a id="входы-в-run"></a>
## Run input

Read [Run input](api/runs-input.md#run-input).

<a id="пассивные-события"></a>
## Passive events

Read [Passive events](api/runs-input.md#passive-events).

<a id="история-и-поиск"></a>
## History and search

Read [History and search](api/history-events.md#history-and-search).

<a id="сессии-и-версии"></a>
## Sessions and versions

Read [Sessions and versions](api/history-events.md#sessions-and-versions).

## SSE

Read [SSE](api/history-events.md#sse).

<a id="частичное-чтение-blobs"></a>
## Partial blob reads

Read [File content](api/files-recovery.md#partial-blob-reads).

<a id="ошибки"></a>
## Errors

Read [Errors and retries](api/files-recovery.md#errors).

<a id="подготовка-восстановленного-запуска"></a>
## Prepare a restored run

Read [Recovery preparation](api/files-recovery.md#prepare-a-restored-run).
