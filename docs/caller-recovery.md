# Caller restart and delegated authorization

[GitHub issue #2](https://github.com/complynx/loom/issues/2) is an error-classification defect. Removing a caller that disappears from fresh Docker discovery is intentional fail-closed behavior. Reporting that absence as an opaque credential rejection was the bug.

For valid authenticated actor/user credentials, a missing current caller binding returns HTTP 503 with `error.code: caller_binding_unavailable` and `error.retryable: true`. No current permissions are borrowed from an old registration. A conflicting/ambiguous actor binding remains unauthorized. Other identity/discovery outages can still return retryable dependency_unavailable.

Live actor/user token verification, stored session lookup and subject/org/actor checks precede the temporary binding error. Invalid/inactive tokens remain 401; a missing/revoked stored session remains a definitive non-retryable lookup denial (currently 404). With a current binding, missing caller harness permissions deny the route with 403. Harness operation rights belong to the service; business/module categories belong to the delegated user. Removing a user module role removes that category, not the service's independent chat-read right.

Clients should preserve run/session IDs, immutable request and idempotency keys, and earlier uncertain effects. Retry 503 with bounded backoff and a persisted availability deadline appropriate to the configured discovery polling interval (production default 300 seconds). Do not retry 401/403/404 as if they were this availability condition. Do not create a replacement run or renew a wait budget on each caller restart. Intentional removal and temporary absence cannot be inferred from the missing registration alone; 503 does not promise future recovery.

On discovery recovery, the same valid session can read its existing run. Already authorized in-flight work may finish; new execution requires a current binding. If a worker authorization check put the run into waiting_auth, explicitly reauthorize the existing session using fresh same-user/same-actor credentials and its current version. That schedules retained work; no new run is needed. Unknown paid operations/writes still require their existing reconciliation rules, never blind replay.

Verify same-run recovery, combined revocation/absence and real caller discovery using [QA](qa.md). A client mitigation for an older opaque 401 does not change the current 503 contract. No database migration or new setting is required for this classification.

Other findings in the original file are confirmed integration contracts: dynamic discovery accepts only the strict `{"methods":[...]}` envelope, and module administrative categories require an explicit deployment/user-role mapping. Loom does not accept an adapter-added `$schema` field or grant administrative rights automatically.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
