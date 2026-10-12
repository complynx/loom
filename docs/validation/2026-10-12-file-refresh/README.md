# File reference refresh acceptance

Local acceptance: PASS on 2026-10-12. Source: the file-refresh working tree after 41af0ad; committed with this report. No real OpenAI requests or production mutations.

Two container scenarios passed with a five-second TTL:

1. A service-backed JS file helper read text, serialized its reference, waited for expiry and API restart, then resumed the same run. Parallel reads returned the original text through a new physical artifact ID. The stable reference did not change. Old HTTP content returned 404, current content streamed successfully, and another user received 404. Final credit reserve was zero.
2. An initial service-backed image attachment entered waiting_input, expired, and survived API restart. Answering the question completed the same run; both model requests contained the image. Final credit reserve was zero.

The isolated stand used PostgreSQL, River, Docker discovery/proxy, sandbox supervisor, private file worker and deterministic local provider. Test TTL was restored to 86400 seconds afterwards. Results contain only scenario assertions and run IDs; credentials, raw requests and configuration are excluded.

Additional PostgreSQL behavioral tests cover concurrent refresh, immutable version rejection before download/quota, current permission and disclosure removal, deleted chat references, foreign users, abandoned lease recovery, heartbeat during a slow download, cancellation/retry, retained expired handles for nonterminal runs, shared references across chats, and unknown transcription settlement without automatic paid replay.

Verification: full Linux integration with race detector PASS; full Windows integration PASS; Golden lint PASS (zero issues). Independent review loop: two passes. First-pass findings fixed: the abandoned refresh lease exceeded the resolver timeout, and shared references depended on the original chat remaining live. Second pass found no actionable findings. Staged diff remained empty through review.

The first manual QA helper attempted to consume SSE as one JSON response; corrected the QA script before successful acceptance. Earlier test fixture setup/timeout failures and a missing lease-query parameter were corrected before the passing gates. These failures were not marked PASS.

Boundaries: raw uploads, derived outputs without a refetch contract and legacy source imports cannot automatically recover. Source availability, unchanged version and current authorization remain required. Public HTTP expiry is not relaxed. No automatic replay of paid transcription or uncertain writes. No target-server or real-service acceptance in this stage.

Images:
- API: loom:file-refresh-20261012 (sha256:c8882836d91a7904eac7675a4ef59c3aa77dcaa1d0caaad47d867dc7d68ddd4e)
- Source fixture: loom:file-refresh-acceptance-20261012 (sha256:56c9e27d71c669d2db906b67ae4d644bc0875a4ec080ef024b5452008f47eb50)

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk