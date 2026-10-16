# Development track

This track separates implemented capabilities, missing acceptance and new work. Use [startup](startup.md), [operations](operations.md), [QA](qa.md) and [limitations](mvp-limitations.md) for the current contracts. Existing task IDs remain stable. A completed item covers only its stated scope, not every installation.

## Implemented foundation

- [x] **A03/A04 — Publication and startup.** Upstream main publishes images. Startup applies migrations and preserves session keys. The panel supports proxy SSO or direct PKCE. Each installation accepts its own deployment.
- [x] **B01 — Real-service module, bounded local scope.** Native discovery, JS broker calls and permission-filtered administrative methods work with a real adapter.
- [x] **B02 — Confirmed write, bounded local scope.** Prepare, restart before approval, real execution and repeated approval/status preserve one effect. A new business method still needs its own checks.
- [x] **B03, basic client slice.** Delegated run, subsequent history, message delivery and recovery of the same run were checked on an isolated client stand. The new conversational protocol remains below.
- [x] **D07/D11 — Live limits and user memory.** Safe limit reductions apply at the next boundary; micro-memory is reloaded before inference. Local behavior is checked.
- [x] **F01/F07/F08/F09 — Discovery, files and telemetry.** Polling is intentional. Temporary references, range/handle streams, media processing and OTel exist. Native file checks do not accept paid transcription or unattended model quality.

## Next acceptance work

- [ ] **A01/E03 — Installed issuer and cache.** Check role removal, expiry, JWKS rotation and purge with warm/cold permission entries. Done when sensitive checks stay online, ordinary checks respect TTL and old cache generations cannot reappear.
- [ ] **A02/D06/D09 — Expanded live model.** Check tools/questions, vision, continuation, native compaction, cached usage and summary quality. Done when usage/settlement agree and restart does not repeat saved effects. Paid runs need an approved budget.
- [ ] **B03/D10 — Conversational client.** Check RU/EN consent, refusal, amendments, late input, delivery ordering and mixed callbacks. A replacement prepare needs a new message; pre-dispatch corrections block old consent. Older protocol-mode checks do not close this item.
- [ ] **C01 — End-to-end telemetry.** Check ingestion, backend reads, rule evaluation and notification delivery separately. Exporter or collector configuration alone is insufficient.
- [ ] **C02/D02 — Backup, restore and keys.** Agree RPO/RTO and off-host storage. Restore ledger/operations and keys behind the recovery barrier without repeated writes. Check historical backups during key rotation. Temporary blobs are not an archive.
- [ ] **C03/E01/E02 — Faults and operating envelope.** On a disposable stand, test storage failures, dependency outages, signals, contention and OOM. Measure load, SSE, queue and shutdown behavior. Do not use a production disk for destructive QA.
- [ ] **C04 — Controlled pilot and rollback.** Check actual client read/write/reauthorize with small quotas. An older image requires compatible schema; startup has no automatic down migration.

## Later changes

- [ ] **D01 — Refreshable delegation.** First prove a supported issuer flow. Loom currently requires reauthorization after access-token expiry.
- [ ] **D03 — Prefix reuse economics.** Measure provider cache hits and cost. Local compatible prefix reuse exists; provider previous_response_id and complete transcript reuse do not.
- [ ] **D04 — Model token deltas.** Add them above durable lifecycle events without duplicate final output or weaker billing/cancel guarantees.
- [ ] **D05 — Dependency recovery.** Define common breaker/retry behavior beyond the existing provider policy. Never repeat an unknown write blindly.
- [ ] **D08 — Data lifecycle.** Set audit/credential/recovery/ledger policy and a separate audited retention migration for existing data. Diagnostics APIs and agent functions already exist.
- [ ] **E04 — Multiple replicas.** Shared temporary files/keys, disconnect coordination and rolling-restart acceptance are needed. Current delivery does not promise HA.
- [ ] **E01, queue-test diagnosis.** An intermittent recovered-row shortfall was previously observed in TestExhaustedQueueRecoveryPreservesUncertaintyAndLiveWork. Isolated repeats did not prove the cause. On recurrence, capture states, leases and locks; a green CI run does not explain it.
- [ ] **E05 — Developer-tool supply chain.** Review native/tool licenses periodically and preserve notices. Go modules need no extra lock file.

## Add only when needed

**F02–F06:** remote cancel, opaque service credentials, WebSocket, private inference/object storage/new native formats, subagents and proactive runs. Each needs a separate contract. Passive events must remain passive.

Close work against its criteria and tested build. Do not infer production properties from a local PASS. Store reports/receipts/dumps outside source control; retain reproducible tests and QA procedures here.
