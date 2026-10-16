# Capabilities and limitations

This page separates implemented behavior, verification scope and missing features. Operators accept their own installation. The legacy filename remains stable for links.

## Implemented and checked

| Area | Verified scope |
| --- | --- |
| API, River and PostgreSQL | Behavioral tests for transactional input/jobs, idempotency, one run per chat, history, SSE and billing. CI includes Linux race tests, Golden lint and Docker builds. |
| Conversation | Deterministic steering, clarification with a blocked action, replacement prepare, native callbacks, exact text, stale consent and restart. Live interpretation is separate. |
| Zitadel | Real isolated issuer checks for JWT/introspection, cross-org denial, role removal, signing-key rotation and API/River restart followed by reauthorization after credential revocation. Proxy SSO and direct PKCE are separate modes. |
| OpenAI | One minimal live Responses call and settlement. This does not accept expanded tools, vision or compaction quality. |
| External client and writes | Bounded real-service checks: delegated run, subsequent history, permission-filtered discovery, result delivery and same-run recovery. Protocol-mode prepare/restart/approval/replay/status preserves one effect. The provider was manual. |
| Files | Native service import, readable PDF text/rendering, video frame/audio extraction, audio metadata and explicit source refetch after artifact expiry. Automatic reference refresh has separate local tests. |
| Memory, search and context | Owner isolation, micro-memory, literal/fulltext/RE2, partial reads, vector API, compatible prefix reuse and durable compaction use fixtures/PostgreSQL. |
| Failures and diagnostics | Isolated disk-full/read-only, cancellation, lost responses, worker kill/OOM, bounded restore barrier, diagnostics, logs/metrics and bounded OTel export. This does not establish host load limits. |

Checks apply to their recorded builds and boundaries. They do not automatically accept each later release or installation. Keep run evidence outside the repository; use the [QA guides](qa.md) for repeatable criteria.

## Implemented, with acceptance still open

| Scenario | Existing implementation | Remaining check |
| --- | --- | --- |
| PKCE | Direct authorization-code/PKCE tested with a real isolated Zitadel. | Accept the selected installation mode. Proxy SSO does not require a direct PKCE client. |
| Role removal | Online authority, bounded positive cache, sensitive-operation bypass and shared purge generation. | Verify revocation and purge in the installation's issuer/cache profile. Warm ordinary checks can delay removal up to TTL; there is no stale-on-error. |
| Signing-key rotation | Signature/JWKS refresh and isolated real rotation. | Operational rotation and independent JWKS failure while introspection remains available. |
| Queue restart → reauthorize | Same run, CAS and a real local credential-revocation chain. | The complete chain for token TTL expiry and the deployed client. |
| Conversational consent | Resolution binds the latest accepted input and exact prepared action. | Live RU/EN, conditional/late answers and client UI ordering. Older binary callbacks do not accept this mode. |
| Models and media | Image attachment, native compaction and paid transcription. | Live vision, compaction quality/cost, paid speech transcription and unattended tool selection. |
| Operations | Startup, telemetry, keyring and recovery guards. | End-to-end backend/alerts, target load/host OOM, off-host/PITR restore and historical-backup key rotation. |

## Not implemented

- Cross-org delegation is intentionally denied. Supporting it needs a separate trust contract.
- Automatic token exchange, offline refresh and refresh rotation inside Loom. Expiry needs explicit reauthorization; a run can enter waiting_auth.
- Incoming opaque service secrets. Services use verified Zitadel tokens and trusted caller bindings.
- Provider token deltas and WebSocket. SSE contains durable run events; steering applies at step boundaries.
- A general parallel scheduler for model tool calls. Scripts can use parallel host RPC.
- An embedding generator, automatic vector indexing or ANN. The optional vector API ranks supplied vectors exactly and is hidden when disabled.
- Provider previous_response_id or complete transcript reuse. previous_run_id is a compatible local history-prefix hint, not resume or a guaranteed provider cache hit.
- A common dependency circuit breaker, automatic late-usage retrieval or a global platform spend cap. Bounded provider retry and sponsored recovery already exist.
- Remote cancellation through manifest cancel_path. Cancel is not rollback.
- Object storage, chunk-upload sessions, cross-host blob transfer or native PDF/audio/video model input. The worker processes these formats; supported images can attach directly.
- A proven private-inference boundary, automatic memory extraction, memory administration UI, subagents or proactive runs.
- Privileged retention changes for all existing resources or secure erasure of every checkpoint/audit/backup trace.

## Operating limits

One nonterminal run owns a chat, including pauses and waits. Passive events do not start inference. Clients serialize pending actions. Identical delayed text cannot be distinguished from fresh text without a reply reference. Replacement prepares need fresh consent.

Files have finite expiry, 24 hours by default and at most seven days. Active runs do not extend it. Uploads and derived outputs without a source-refetch contract cannot recover automatically. External services own long-lived originals.

Readiness is not a write-capacity probe. A read-only or full database can still accept connections. Monitor storage and write errors separately. Sandbox limits cover the shared container, not each worker; they do not establish HA.

Default OpenAI hard/trigger/target values are 272000/244800/204000 per model. Byte bounds can compact earlier. The provider controls native compacted output size. Compaction is billed; its reservation can require the full possible output window.

All model endpoints require external-disclosure permission. Secrets do not enter JS. Debug payload logs can contain user text and JWT claims; credential masking cannot recognize every secret written in free text.

Startup applies pending migrations and does not downgrade schemas. Stop incompatible older workers and preserve database/session keys between container replacements. Host profiles belong outside this repository.
