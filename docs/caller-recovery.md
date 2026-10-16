# Caller restart and delegated authorization

A delegated service needs a current trusted caller registration as well as valid
tokens or a saved authorization session. Discovery removes a caller that is no
longer registered. Loom does not borrow permissions from its old registration.

## Distinguish absence from denial

For otherwise valid actor/user credentials, a missing current caller binding
returns HTTP `503` with `error.code: caller_binding_unavailable` and
`error.retryable: true`. Conflicting or ambiguous actor bindings remain denied.
Other identity/discovery outages can return retryable `dependency_unavailable`.

Actor/user token verification, session lookup and subject/organization/actor checks
precede the temporary binding error. Invalid or inactive tokens return 401. A
missing, expired or revoked saved session returns a definitive lookup denial
(currently 404). With a current binding, missing caller permissions return 403.
Do not treat those denials as temporary caller absence.

Harness operation rights belong to the service; business/module categories belong
to the delegated user. Removing a user's module role removes that category, not
the service's independent chat-read permission.

## Retry the existing work

Persist run/session IDs, original request bodies, idempotency keys and records of
uncertain effects. Retry the 503 with bounded backoff and a persisted availability
deadline sized for the deployment's discovery polling interval. Do not reset the
wait budget on every restart or create a replacement run. Missing registration
alone cannot distinguish temporary absence from intentional removal; 503 does not
promise future recovery.

Once discovery recovers, the same valid session can read the existing run. Already
authorized in-flight work may finish; new execution needs a current binding. If the
worker put the run in `waiting_auth`, reauthorize its existing session with fresh
same-user/same-actor credentials and the current session version. This schedules
retained work. See [Session requests](api/history-events.md#sessions-and-versions)
for the exact headers and body.

Unknown paid operations and writes still need their existing reconciliation
procedure. Do not replay them blindly after availability recovers. See
[Failure behavior](failures.md) and [Recovery mode](recovery-mode.md).

## Registration contract

Dynamic discovery accepts the strict `{"methods":[...]}` envelope. Do not add a
`$schema` field. Module administrative categories require explicit deployment and
user-role mappings; Loom does not grant administrative rights automatically.
See [Module discovery](modules/discovery.md).
