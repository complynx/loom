# Real Zitadel, River restart and reauthorization acceptance

Result: **PASS**, 2026-10-11. Full API, PostgreSQL, River workers, separate Linux sandbox supervisor and real Zitadel 4.16.3 were used. Running image was verified before and after the restart:

`clx-scripter:full-review-qa-20261011`, `sha256:cef24f773800be422bcbeed05deb4456afe95edc865a416affbf48947f8ad1c4`.

Run `91e25c39-d42b-43db-b1b6-945e04d072dc` completed through the following actual HTTP and issuer actions:

1. An explicit saved authorization session and question-producing run were created. The run reached `waiting_input`; its first model step cost 110 microcredits.
2. The owner was deactivated through the real Zitadel management API. The previous issued JWT returned HTTP 401. The owner was reactivated; a new access token was issued by Zitadel.
3. An answer submitted with fresh credentials queued the run. Its old sealed credentials were rejected by live issuer verification. The run reached `waiting_auth` with reason `authorization_required`, spent 110 and reserved zero.
4. `docker compose restart api` stopped and restarted the actual API process and its River worker pool. The saved session, question, input and same-run `waiting_auth` survived. The running image remained the reviewed image.
5. A same-organization alternate user with the same harness role received HTTP 409 on reauthorization and HTTP 404 on the run. The initial foreign organization token lacked the harness role and received HTTP 403.
6. Fresh owner credentials and the current session version reauthorized the same saved session. The session version increased. Repeating the stale version returned HTTP 409 `session_version_conflict`.
7. River completed the same run. Exactly two model steps settled, 110 each, and one answer tool step settled zero. Total spent was 220; current reserved balance was zero. Chat history contained one user message, one question, one answer and one assistant message. The answer was applied once.
8. A stronger foreign-organization test then granted the same `qa-user` role through a real project grant and issued a fresh JWT. The foreign user could access their own default chat (200), but received 409 on the owner's saved session and 404 on the owner's run. See `foreign-org-same-role.json`.

Evidence: `question.json`, `waiting-auth-before-restart.json`, `waiting-auth-after-restart.json`, `reauthorized.json`, `completed.json`, `assertions.json`, `run-durable-history.txt`, `foreign-org-same-role.json`, `reviewed-runtime-image.txt`, and `api.log`.

The first preparation-derived run paused before inference because the inherited dynamic acceptance module accepted literal fixture tokens, while this lane uses real JWTs. It spent zero. Removing `qa.access`/`qa.read` from this lane's role corrected the fixture. No product code changed. The extra paused run remains visible in aggregate database evidence; run-scoped history and settlements identify the accepted run precisely. Aggregate credit ledger totals include reserve audit records and account bootstrap and are not billable spend.

## Boundaries

This proves live revocation and replacement of real issued credentials. **TTL expiration was not used in this lane.** The queue restart is the real API/River process restart with persistent PostgreSQL; PostgreSQL itself was not restarted. Provider inference is deterministic local HTTPS, not a paid OpenAI request. There were exactly two local model requests and **zero paid OpenAI calls**. Separate sandbox process was running, but this question lane did not execute a JavaScript script or an external module mutation. No claims about duplicate external mutation effects are made here; those belong to module/reconciliation QA.

The fixture obtains real human JWTs through an explicitly authorized fixture-only impersonation token exchange. This is not production login guidance and does not replace the separate browser PKCE acceptance. Secrets and tokens remain in ignored `management.local/full-qa-reauthorize/`. Public config values are redacted. Existing Zitadel/OpenAI stands and production were not modified.

The isolated stand remains running for inspection: API `http://127.0.0.1:58480`, issuer/proxy port 58490, fixture module port 58491. Root decides cleanup. This report does not claim production acceptance.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
