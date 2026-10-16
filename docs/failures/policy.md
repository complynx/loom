# Policy

[Guide index](../failures.md)

<a id="реализованный-срез"></a>
## Current behavior

Loom persists run intent, operation identity and reservations before dispatch. A client retry must preserve its idempotency key and request body. A lost HTTP response does not prove that a request failed.

Explicit provider rejections without output use [hybrid retry](../hybrid-retry.md). Uncertain inference can use the bounded [platform-sponsored policy](../active-compaction.md), with separate accounting for possible platform costs. Outside that policy, reconcile the uncertain result. Never repeat an unknown external write blindly.

River limits job attempts. The queue sweep moves exhausted work without a live job or lease to `paused` for explicit resume, or to `reconciling` when a result or charge is uncertain. Reservations remain protected; the sweep does not create an unlimited retry loop.

A general circuit breaker and OAuth refresh/token exchange are not implemented. Discovery uses polling and freshness TTLs. Database transaction retries are limited to transactions known to have rolled back; they do not repeat network actions. See [limitations](../mvp-limitations.md) for remaining boundaries.

<a id="базовые-правила"></a>
## Outcome rules

| Outcome | Required action |
| --- | --- |
| `not_started` | Retry only with evidence that dispatch did not occur, or a backend guarantee that execution cannot occur. Keep retries bounded. |
| `succeeded`, `failed`, `cancelled` | Preserve the final backend result. Do not repeat a completed action. |
| `unknown` | Query status first. Repeat the same operation ID/key only when the backend's idempotency contract permits it. |

Do not dispatch a new action without durable intent, current authorization and a reservation when required. Cancelling a run does not roll back an external effect.

The [dependency matrix](dependencies.md) is a troubleshooting guide, not evidence that every deployment scenario passed. Readiness does not prove database writes or free storage. The [recovery barrier](../recovery-mode.md) supports restore review; it does not provide automatic PITR or a distributed freeze. Loom does not promise exactly-once network execution or recovery after loss of every copy of data and keys.
