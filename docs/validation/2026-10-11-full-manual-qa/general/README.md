# General manual acceptance — 2026-10-11

Follow-up 2026-10-12: [fixture browser acceptance passed](../../2026-10-12-browser-qa/README.md). The original 40-scenario aggregate and historical TLS BLOCKED entries below are unchanged; the new browser result is recorded separately.

The corrected aggregate contains **40 unique manual scenarios: 40 PASS, 0 unresolved FAIL**. See [FINAL-RESULTS.json](FINAL-RESULTS.json). This result applies to the reviewed Linux runtime with real PostgreSQL/pgvector, River workers, isolated JS supervisor, Docker discovery and the acceptance model service. Identity and model Responses were controlled HTTPS fixtures. No paid model calls or production changes were made.

API and sandbox both ran image `sha256:cef24f773800be422bcbeed05deb4456afe95edc865a416affbf48947f8ad1c4`; [image proof](image-id.json). Requests went through public HTTP endpoints and actual model-selected JS execution. These are runtime scenarios, not unit-test results. The fixture selected deterministic Responses and usage receipts; it does not prove real model quality or real provider billing.

## Coverage and evidence

| Feature | Manual result and scope | Evidence |
| --- | --- | --- |
| Authentication and authorization | Invalid/inactive bearer denied; direct and trusted delegated callers; bare service denied; user and organization boundaries; automatic personal-memory access without special roles | [Core](core-results.json), [HTTP transcript](http.jsonl) |
| Sessions | Create, versioned reauthorization, revoke; CLI encryption-key rotation retained access for existing sessions | [Core](core-results.json), [Rotation](session-rotation.txt) |
| Chats and history | Stable default chat; owner/org isolation; passive append deduplication and conflict; no proactive inference; fulltext, literal, regex, invalid pattern, cursor and partial bounded reads; soft delete | [Core](core-results.json), [Agents](agent-results.json), [Partial read](history-partial.json) |
| Semantic vectors | Supplied-vector indexing/search and isolation; disabled endpoint returns plain HTTP 404 and tool is absent from the model catalog; restored afterward | [Core](core-results.json), [Corrected toggle](semantic-toggle-recovery.json), [Disabled catalog](semantic-disabled-provider.json) |
| Personal memory | Micro/index/note/source CRUD; stale-version conflict; 500-character micro limit; bounded source read and regex; owner/org isolation; fresh micro injected on the next model step; delete/clear | [Core](core-results.json), [Corrected cases](recovery-results.json), [Fresh prompt](memory-fresh-provider.json) |
| Policy and preferences | Available models/effort and preference CAS; admin configuration permissions; own policy/preferences; changing another user's policy/preferences allowed to admin and denied to ordinary caller; foreign-only target returns 404 | [Core](core-results.json), [Other-user result](admin-other-user.json), [HTTP transcript](http.jsonl) |
| Runs and interaction | Idempotency/body conflict/chat busy; choices and text; text-only; interaction disabled; steering; stable waiting checkpoint pause/resume; answer replay conflict; one-shot wait; SSE replay and Last-Event-ID reconnect | [Agents](agent-results.json), [Corrected cases](recovery-results.json), [Initial SSE](sse-initial.txt), [Reconnect](sse-reconnected.txt) |
| Unknown in-flight inference | Pause during dispatch preserves uncertain usage/reservation; audited receipt reconciliation releases reserve; explicit resume completes | [Uncertain](pause-inference-uncertain.json), [Reconciled](pause-reconciled.json), [Resumed](pause-receipt-resumed.json) |
| JS runtime | Actual isolated supervisor; parallel calls, built-ins and module calls; infinite script canceled through client API in approximately 165 ms | [Agents](agent-results.json), [Parallel request](parallel-script-provider.json), [Cancel timing](js-cancel-timing.json) |
| Artifacts | Raw upload/download and exact original bytes; import/temporary handles; cross-owner denial; tested bulk-delivery path passes JS metadata/handle rather than blob bytes; bounded `artifacts.read` remains available; direct PNG model attachment; images-disabled policy stops admission safely | [Agents](agent-results.json), [Corrected cases](recovery-results.json), [Image request](image-attachment-provider.json), [Original bytes](blob-original.txt) |
| Discovery | Actual Docker metadata polling, static and dynamic manifests, dynamic calls; stopped registration disappears after poll and restored registration returns; read-only catalog excludes write call | [Agents](agent-results.json), [Toggles](toggle-results.json), [Removed catalog](module-removed-provider.json), [Restored catalog](module-restored-provider.json), [Read-only catalog](read-catalog-provider.json) |
| Write approval and uncertain effect | Explicit approve/reject; replay-safe approval; disabled approval never implies consent; lost response persisted as reconciling; retained-volume service restart and explicit resume reconcile effect via service status | [Agents](agent-results.json), [Corrected cases](recovery-results.json), [Waiting](write-waiting.json), [Uncertain](write-reconciling.json), [Recovered](write-recovered.json) |
| Retry and accounting | Fixture 429/503 retry through River; unknown transport drop sponsored recovery; final user usage charged once and reserve released; audited credit-adjustment deduplication and permissions | [Agents](agent-results.json), [429](retry-429.json), [503](retry-503.json), [Sponsored recovery](sponsored-recovery.json) |
| Previous run and compaction | Previous run ID retained within chat; current personal micro retained; native active compaction called through fixture and opaque compaction item persisted/used in the next Responses request | [Agents](agent-results.json), [Corrected cases](recovery-results.json), [Previous run](previous-run-provider.json), [Compact request](native-compact-final-provider.json), [Diagnostics](native-compact-diagnostics.json) |
| Diagnostics and observability | Permission-protected HTTP lists/details/pagination; actual agent diagnostic functions; metrics exposed; debug-log check did not find full fixture credentials or secrets | [Agents](agent-results.json), [Toggles](toggle-results.json), [Agent request](diagnostics-script-provider.json), [Metrics](metrics.txt), [Logs](api.log) |
| Panel authorization protocol | Product login generated S256 PKCE and binding cookie; fixture authorization code exchanged; fresh diagnostics authorized; replay denied. Explicit fixture CA trust with normal TLS/hostname validation | [PKCE protocol](pkce-protocol.txt) |

## Original failures and corrections

Original failed evidence remains available in [first-pass results](first-pass-results.json), [second-pass results](second-pass-results.json), [agent results](agent-results.json), [toggle results](toggle-results.json), and [other-user first result](admin-other-user-first.json). The aggregate counts each final scenario once. It does not erase earlier failures.

The driver initially used a budget below the conservative prompt/tool reserve, reused passive-event deduplication IDs, expected a revoked session to return 401 instead of 404, and used invalid version zero rather than a positive stale version. These assertions were corrected and rerun through HTTP. Early budget-limited runs were canceled through the public API.

An in-flight pause correctly entered `reconciling/inference_uncertain`, not a free paused state. The original reservation was preserved, then reconciled with an audited operator receipt and explicitly resumed. No direct SQL reset was used. Image and compaction checks initially changed only the user policy; the model catalog is also authoritative, so both settings were changed through admin APIs and restored after acceptance.

One approval scenario ran immediately after acceptance-service restart, before discovery refreshed; its tool call failed without a durable effect. Rerunning after the configured poll interval passed. Discovery is deliberately eventual. The disabled semantic endpoint correctly returned plain 404; the original helper incorrectly tried to decode it as JSON. The corrected helper verified both 404 and the missing tool advertisement.

The first admin organization assertion reused the same issuer/subject in two organizations. That owner already had legitimate membership in the admin's organization, so reading its local policy returned 200. The corrected fixture enrolled a separate foreign-only subject; requesting that owner's policy through the original organization's admin returned 404. Other-user policy/preferences changes and ordinary-caller denial also passed. No product defect was established by these driver failures.

## Boundaries and remaining acceptance

- **Current browser login remains blocked:** the root agent rendered the logged-out panel, but navigation to the private HTTPS issuer produced `ERR_CERT_AUTHORITY_INVALID`. Browser warnings were not bypassed, host trust was not modified, and TLS validation was not weakened. The PKCE result above is a protocol-client PASS, not a current browser-login PASS. Real Zitadel browser evidence belongs to its separate QA lane.
- Disk-full/ENOSPC and real Zitadel revocation/queue restart/reauthorization belong to independent QA lanes and are combined in the parent report. They are not counted as this lane's passes.
- Long retention aging, cache/stale TTL expiry boundaries, deadline expiry, maximum-size/load saturation, malformed-manifest variants and replica races were not separately exercised manually here. Ordinary paging/partial results, cancellation, polling removal/restore and the tested retry paths passed; this does not imply all timing/load edges passed.
- A live Codex adapter was not exercised with a dedicated authenticated local profile. Fixture results do not prove real model interpretation, cache pricing, native summarization quality, or opaque encryption correctness. Historical minimal real OpenAI evidence is reported separately; no new paid requests were made.
- Text-to-vector embedding generation is outside the supplied-vector API. These scenarios do not establish an external embedding service, a universal dependency circuit breaker, or universal retry coverage.

## Retained stand and replay

The isolated stand remains available for inspection by the parent agent: API `http://127.0.0.1:58680`, metrics `http://127.0.0.1:58681/metrics`, acceptance service `http://127.0.0.1:58690`. Project prefix is `clx-fullqa-general-20261011`; its network is `10.254.244.0/24`.

Private replay drivers, peer source, configuration, generated credentials and certificates are under ignored `management.local/full-qa-general/`. Replay uses `run.ps1`, `semantic-recovery.ps1` and the focused cases in `agents.ps1`; the PKCE client is `pkce-client.go.txt`. Docker commands must include both `compose.yaml` and `rotation.compose.yaml`, because the key-rotation check changed the active session key. Public transcripts omit credential headers and requests. Private files were not copied into this evidence directory. An automatic approval check rejected the proposed private-source export; a sanitized report was produced instead.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
