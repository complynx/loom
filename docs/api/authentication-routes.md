# Authentication and routes

[API index](../api.md)

<a id="аутентификация"></a>
## Authentication

Configure the issuer, API audience, introspection credentials and role-to-permission
mapping before connecting a client. Normal API authorization requires the user's
resource-owner organization claim and roles granted in that organization. The
browser proxy has a separate [operator organization rule](../operator-login.md).
Positive token claims use a [bounded permissions cache](../identity-cache.md);
sensitive writes require fresh online authorization.

| Caller | Headers |
| --- | --- |
| User | `Authorization: Bearer <user-access-token>` |
| Service bootstrapping delegation | `Authorization: Bearer <service-access-token>` and `X-Subject-Token: <user-access-token>` |
| Service using saved delegation | `Authorization: Bearer <service-access-token>` and `X-Auth-Session-ID: <session-id>` |

A service also needs a current trusted deployment binding. User and actor must
belong to the same organization. A saved session is bound to user, actor,
organization and allowed audiences; its ID is not a bearer credential. Do not
send both delegation headers.

For run creation, body `auth_session_id` selects a session but does not authenticate
a service request: the service must still send `X-Auth-Session-ID`. Header and body
IDs must match when both are set. Do not combine a body session ID with a subject
token. If no session is selected, run creation saves one from the verified credentials.

Loom accepts verified access tokens, not arbitrary service secrets. Automatic OAuth
token exchange and refresh are not implemented. Expiry can put work in `waiting_auth`;
[reauthorize the existing session](history-events.md#sessions-and-versions).
Use TLS at the external boundary. Normal logs exclude credentials; explicit debug
payload logging can retain claims and JWTs with masked signatures. See
[logging policy](../observability.md).

<a id="поверхность-api"></a>
## Routes

Paths below are relative to `/v1`. Listed permissions are route gates; ownership,
organization, lifecycle and additional handler checks still apply.

| Method | Path | Purpose / route permission |
| --- | --- | --- |
| POST | `/auth/sessions` | Save delegation; `chat.read` |
| POST | `/auth/sessions/{id}/reauthorize` | Replace delegation with fresh same-user/actor authorization; `chat.read` |
| DELETE | `/auth/sessions/{id}` | Revoke local session; `chat.read` |
| POST | `/chats` | Create separate chat; `chat.read` |
| GET | `/chats/default` | Get or create default chat; `chat.read` |
| GET | `/chats/{id}/history` | Read event pages; `chat.read` |
| POST | `/chats/{id}/events` | Append passive action; `chat.events.append` |
| POST | `/chats/{id}/history/search` | Search history; `chat.read` |
| POST | `/chats/{id}/history/read` | Read bounded events or event text; `chat.read` |
| POST | `/chats/{id}/history/embeddings` | Index supplied vector when enabled; `chat.embeddings.write` and `chat.read` |
| DELETE | `/chats/{id}` | Delete chat and derived data; `chat.delete`; explicit ID required |
| POST | `/runs` | Create work; `runs.create` and `chat.read` |
| GET | `/runs/{id}` | Read state, question, approval and result; `chat.read` |
| GET | `/runs/{id}/events` | Replayable SSE; `chat.read` |
| POST | `/runs/{id}/input` | Submit steering, answer or approval; `runs.control` |
| POST | `/runs/{id}/pause`, `/runs/{id}/resume`, `/runs/{id}/cancel` | Control same run; `runs.control` |
| GET | `/models` | List permitted models; `chat.read` |
| GET | `/users/me/preferences` | Read model/effort; `chat.read` |
| PATCH | `/users/me/preferences` | Set model/effort; `preferences.self.write` |
| GET | `/users/me/credits` | Read balance and reservation; `chat.read` |
| GET/PATCH | `/admin/settings` | Read/update typed settings; `settings.read` / `settings.write` |
| POST | `/admin/identity/cache/clear` | Purge shared API/worker claims cache; `settings.write`, admin organization, fresh authorization |
| GET/PATCH | `/admin/users/{id}/policy` | Read/update user limits; `users.policy.write` |
| GET/PATCH | `/admin/users/{id}/preferences` | Read/update another user's model; `preferences.other.write` |
| POST | `/admin/billing/{id}/adjustments` | Audited credit adjustment; `billing.adjust` |
| POST | `/admin/runs/{id}/recovery/prepare` | Prepare uncertain restored work; `operations.reconcile`, recovery mode only |
| POST | `/admin/runs/{id}/steps/{step}/reconcile` | Reconcile unknown inference billing; `operations.reconcile` |
| POST | `/artifacts` | Upload raw file bytes; `artifacts.write` |
| GET | `/artifacts/{id}` | Read metadata; `artifacts.read` |
| GET/HEAD | `/artifacts/{id}/content` | Read bytes or headers; `artifacts.read` |
| POST | `/runs/{id}/artifacts/handles` | Create handle from `{artifact_id}`; `artifacts.read` |
| GET/HEAD | `/artifact-handles/{id}/content` | Read authorized file content; `artifacts.read` |

Handles expire within 15 minutes and no later than the file. A handle ID alone
does not grant access. See [Files and recovery](files-recovery.md).
[User memory](../user-memory.md) and [diagnostics](../diagnostics.md) describe their
additional endpoints. Consult `/openapi.json` for request and response schemas.

Outside `/v1`, `/healthz` checks the process and `/readyz` checks whether durable
requests can safely be accepted. Readiness returns 503 during recovery mode.
Metrics use a separate private listener.
