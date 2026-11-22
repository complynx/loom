# External agents

A user can connect their own agent to Loom through the Model Context Protocol
(MCP). The agent reads permission-filtered modules through `script`. If the
user enables writes, the agent uses the separate write tools. The user's
agent supplies its own model for both paths.

This page is the contract for operators, chat clients, module authors and
clients of the MCP endpoint. It also records the agreed design for the parts
that are not implemented.

## Status

| Part | State |
| --- | --- |
| MCP endpoint, OAuth resource metadata, `script` tool with help, skill, history and memory reads | Implemented. |
| Service reads through marked methods | Implemented with a separate token from a [token service](#token-services). Off until a discovered service declares a token endpoint and the operator approves it. |
| User profile written by a confirming chat client | Implemented. |
| MCP sessions and revoke controls in the internal agent | Implemented. |
| `external_agent` method mark in manifests | Implemented for eligible read and write methods. |
| History record for each external action | Implemented. |
| Acceptance with a real MCP client and the installed issuer | Open. See [Acceptance](#acceptance). |
| External service writes | Implemented for marked idempotent service methods with status. See [Service writes](#service-writes). |
| External personal-memory writes | Planned. |
| Files, paid transcription, Loom diagnostics and administration tools | Not available to external agents. |

## Execution model

An external action runs outside any run.

- It does not create a run, queue a job or wake a waiting run. It cannot start
  the internal agent.
- It does not occupy the chat. A paused or waiting run of the same user does
  not block it.
- It reserves no credits and reads no balance. A user who has no credits can
  continue to work through their own agent. A per-user rate limit replaces
  the credit check.
- External scripts share the sandbox workers with internal runs. At most half
  of `sandbox.max_workers` (minimum one) can run external scripts at the same
  time. A call above that bound fails with `busy`. With one sandbox worker,
  that worker serves both internal runs and external scripts.
- Loom writes each action to the user's default chat as a
  [passive event](api/runs-input.md#passive-events). The next internal run
  reads that event as context. A current run reads it between steps.

The global `external_disclosure` setting must be true. The agent's model and
host are outside Loom, so every result is an external disclosure.

## Enable the endpoint

Add this block to the deployment configuration. The endpoint is disabled by
default.

```yaml
external_agents:
  enabled: true
  resource: "https://loom.example.com/mcp"
  audience: "replace-with-mcp-audience"
  scopes: ["openid"]
  client_id: "replace-with-mcp-introspection-client"
  client_secret: "${CLX_MCP_INTROSPECTION_SECRET}"
  token_services:
    replace-with-service-id: "${CLX_MCP_TOKEN_SERVICE_SECRET}"
  calls_per_minute: 30
```

| Field | Meaning |
| --- | --- |
| `enabled` | Registers `/mcp`, the metadata route and the profile routes. |
| `resource` | Public URL of the MCP endpoint. HTTPS except loopback. It must contain a path without a trailing slash. Loom serves the endpoint at `/mcp` and the metadata at `/.well-known/oauth-protected-resource`; a proxy must map both public paths. |
| `audience` | Required. Audience that a token for the MCP endpoint must contain. It must differ from `identity.audience`. See [token audience](#token-audience). |
| `scopes` | Scopes that Loom advertises to clients. List every scope that the issuer needs to put the MCP audience, the organization and the roles into the access token. Do not add scopes for the audiences of downstream services. At most 32 entries. |
| `client_id`, `client_secret`, `client_key_base64`, `client_key_file` | Optional introspection credentials for MCP tokens, with the meaning of the same `identity` fields. Without them Loom uses the `identity` credentials. See [introspection client](#introspection-client). |
| `token_services` | Optional. Approves [token services](#token-services): each key is a service ID, each value is the secret that authenticates Loom to that service. A secret has 16 to 512 printable ASCII characters without spaces. At most 32 entries. Without an entry the agent can use only builtin modules. |
| `calls_per_minute` | Tool calls and tool listings per user in one fixed minute, 1..600. Zero or omission uses 30. The limit also counts calls of a user without access. It is local to one API process. |

When `enabled` is false, the other fields must be absent. In recovery mode the
endpoint and the profile routes are not served and return `404`.

`GET /v1/capabilities` reports the feature to registered clients:

```json
{"external_agent": {"version": 1, "resource": "https://loom.example.com/mcp", "access": ["history", "memory", "skills", "writes"], "token_services": ["replace-with-service-id"]}}
```

The key is absent when the endpoint is disabled. A chat client must check it
before it offers the feature, and can show `resource` as the URL that the user
adds to their agent. `access` lists the profile switches. `token_services`
lists the IDs of the [token services](#token-services) that are approved and
discovered now. With an empty list external agents cannot call service methods.

## Authorization

Loom is an OAuth resource server. The configured `identity.issuer` is the only
authorization server. Loom issues no credentials of its own.

1. A request without a valid token returns `401` with
   `WWW-Authenticate: Bearer resource_metadata="<url>", scope="<scopes>"`.
2. The metadata URL is the resource URL with
   `/.well-known/oauth-protected-resource` inserted before its path. For the
   example above it is
   `https://loom.example.com/.well-known/oauth-protected-resource/mcp`. Loom
   serves the document at that path and at
   `/.well-known/oauth-protected-resource`.
3. The document contains `resource`, `authorization_servers`,
   `bearer_methods_supported` and, when configured, `scopes_supported`.
4. The client gets an access token from the issuer and sends
   `Authorization: Bearer <token>` with every request.

Loom verifies the token with the same rules as the `/v1` API: signature,
issuer, [audience](#token-audience), expiry and introspection. It then applies
these rules:

- The token must belong to a human user. Service accounts and registered
  service callers are rejected.
- Delegation headers (`X-Subject-Token`, `X-Auth-Session-ID`) are rejected.
- The user keeps only their module permissions. Every Loom operation right
  from the token (`runs.*`, `chat.*`, `artifacts.*`, administration rights) is
  dropped. The [profile](#user-profile) supplies the rights of the session.

The client refreshes its own token. Loom stores no credential for an external
agent. Loom checks the token at each request and again before each module call
of a running script. These checks can use the
[positive claims cache](identity-cache.md), so a revoked token or removed role
can stay effective for the cache TTL. A profile change is not cached.

Registration of the client at the issuer (pre-registered client, Client ID
Metadata Document or dynamic registration) is the operator's decision when
the broker is disabled.

### OAuth broker for local clients

Set `external_agents.oauth_broker` to offer one ZITADEL callback for all local MCP clients.
The broker publishes OAuth metadata at `/.well-known/oauth-authorization-server` and serves
`/oauth/mcp/*` on the same public origin as `/mcp`. Route these paths to Loom.

In the MCP audience project in ZITADEL, create a confidential Web OIDC application
with Authorization Code and Basic authentication. Register
`https://<loom-host>/oauth/mcp/callback` as its exact redirect. Use the existing MCP
API application's credentials for introspection.

Add this section under `external_agents`:

```yaml
oauth_broker:
  client_id: "<confidential ZITADEL application client ID>"
  client_secret: "${LOOM_MCP_BROKER_CLIENT_SECRET}"
  key_base64: "${LOOM_MCP_BROKER_KEY}"
  confirmation_service: "zinusya"
```

Supply an independent base64-encoded random 32-byte key. In `external_agents.scopes`,
include `openid`, the MCP project audience scope, and `urn:zitadel:iam:user:resourceowner`.
The broker uses these scopes for ZITADEL. MCP clients request the broker scope `mcp`.

Clients register an HTTP loopback IP or `localhost` callback with a path.
Authorization requires an explicit loopback port. The host and path must match
the registration, and the token exchange must use the exact authorized callback.
Registration does not create an application record in Loom or ZITADEL. The user
signs in at ZITADEL and approves the named client in the configured Telegram bot
before Loom issues a code. Zinusya must support connection approvals on its
internal token-service endpoint. The owner must enable read access in `/mcp`
for tools. Write consent remains independent.

Deploy the confirming client before enabling the broker. Broker mode requires a
fresh login for existing MCP clients. The browser holds the encrypted login
request until ZITADEL verifies the identity. Use one Loom API replica for the
Telegram decisions and one-time codes. Pending decisions and codes are lost on
restart. The login request expires after five minutes.
Tokens expire with the upstream token or after one hour. Clients sign in again
when their token expires. Keep the broker key to preserve registrations on restart.
Removing the broker block restores direct ZITADEL authorization.

The OAuth broker keeps up to 16 active authorizations per user and organization.
At the limit, a new login revokes the oldest active authorization and closes its sessions.
The confirming Telegram bot can list and revoke selected authorizations. A
revoked token cannot open another session. Revoking all authorizations leaves
the user's profile settings in place. Disabling the profile also revokes its
current broker authorizations. Telegram approval creates the authorization
before the broker issues a code. Revoking it also cancels any pending exchange.

Loom advertises management as `external_agent.oauth.manage_connections` in
`GET /v1/capabilities`. Both management routes require a delegated actor with
`external_agent.profile.write`, acting for the user in the current organization.

| Route | Contract |
| --- | --- |
| `GET /v1/users/me/external-agent/connections` | Returns `200` with a `connections` array of active authorizations, newest first. Each entry has `id`, `name`, `created_at`, and `expires_at`. |
| `POST /v1/users/me/external-agent/connections/revoke` | Takes `{ "ids": [...] }` with 1 to 16 canonical authorization UUIDs. Returns `422` for invalid input and `200 {}` otherwise. IDs outside the user's organization or ownership, and already revoked IDs, have no effect. |

Revocation uses the normal delegated identity cache. It can reduce access
while the issuer is unavailable and the cached credentials are still valid.
If Loom cannot record the Telegram approval or token exchange returns
`temporarily_unavailable`, start a new login when Loom is available again.

Apply migration 15 before starting a Loom binary with authorization management.
Previously issued broker tokens require a fresh login after the upgrade. On
rollback, the old binary also requires a fresh login for tokens issued by the
new broker. Registrations are preserved when the broker key stays the same.
Before updating the broker, finish pending writes and reconcile unknown outcomes.
A fresh broker login starts a new session and cannot continue those operations.

### Token audience

Loom does not require the resource URL in the token audience: the issuer
decides which audience values it writes. Set `external_agents.audience` to the
value that the issuer writes for this endpoint. It must differ from
`identity.audience`; Loom refuses to start with a shared value.

Loom checks MCP tokens with a separate authenticator and claims cache. A token
for the MCP audience is not accepted at `/v1`, and a `/v1` token is not
accepted at `/mcp`. A token that contains both audiences is refused at both
endpoints.

Loom does not know the audiences of downstream services. The issuer must not
give an MCP client a token that a service accepts. With such a token the client
can call the service directly, outside the method mark, the profile and the
history record.

The separation is necessary. A `/v1` token of a user opens routes that need no
operation right, for example the personal memory routes, which read, change
and delete memory. A token that is valid at both endpoints would let the
agent's client use those routes outside the profile, outside the read-only
limit and outside the history record.

The role claims of the token must contain the user's module roles, or the
session has only the modules and methods that need no permission.

### Introspection client

By default Loom introspects MCP tokens with the `identity` credentials. Some
issuers answer an introspection request only when the token's audience contains
the introspecting client or its project. ZITADEL is such an issuer. An MCP
token must not contain `identity.audience`, so on such an issuer the `identity`
client cannot introspect it and every request to `/mcp` gets `401`.

For such an issuer, register a second introspection client that the MCP token
names, for example an API application in the project of the MCP clients, and
set its credentials in `external_agents`. Use `client_id` with `client_secret`,
or one application key (`client_key_base64` or `client_key_file`). Loom uses
them only for MCP tokens. The `/v1` API keeps the `identity` credentials.

### Token services

By default external agents cannot call service methods. The catalog, the
bindings and dispatch contain only builtin modules.

A **token service** enables marked service reads. It is a discovered service,
for example a chat client, that can issue a token of the user for downstream
services. Loom never sends the agent's token to a downstream service. Before a
service call, Loom asks the selected token service for a separate service token
and sends only that token. The external agent never receives it.

A service is a token service when both of these are true:

1. Its manifest declares the endpoint. This is a manifest fragment:

   ```json
   {"external_agent_tokens": {"path": "/external-agent/token"}}
   ```

2. The operator approves its service ID in `external_agents.token_services`
   with a secret.

The approval is necessary because Loom sends the user's agent token to that
service. Approve only a service that is trusted to act for every user.

How the service gets the token is its own decision, for example an OAuth 2.0
token exchange with impersonation at the issuer. Loom needs no credential that
can impersonate a user.

#### Selection

Different token services can give different rights and reach different
services. The user decides which one to use. Loom announces the available token
services in the description and in the input schema of the `script` tool, with
the ID and the summary of each service. The agent selects one with the
`token_service` argument of each call.

- With one token service, Loom uses it when the argument is absent.
- With several token services, a call without the argument has no service
  methods, and `help.list` returns a `token_service_not_selected` warning.
- An unknown value fails with `unknown_token_service`.
- A caller without an enabled profile does not see the list.

Loom sends the token of the selected service to every service method that the
script calls. A service that does not accept that token refuses the call.

#### Request

Loom sends `POST <path>` to the registered origin of the token service:

```http
Authorization: Bearer <secret from external_agents.token_services>
Content-Type: application/json

{"protocol_version": 1, "subject_token": "<the agent's access token>"}
```

The service returns `200` with a JSON object:

```json
{"access_token": "<JWT>", "expires_in": 600}
```

`expires_in` is optional. Every other status is a failure. The response is
limited to 64 KiB and to `discovery.request_timeout_ms`.

A token service must do all of these:

- Check the secret first. The agent holds the subject token too. Without the
  secret check, an agent that can reach the endpoint gets a service token and
  calls services outside Loom.
- Verify the subject token with the issuer: issuer, expiry, the MCP audience
  and a human user.
- Issue the token only for the user of the subject token, and only when the
  service accepts that user for external agents.
- Put neither `identity.audience` nor `external_agents.audience` into the token.

#### Checks in Loom

Loom does not trust a token service for the facts of its token. Loom accepts
the returned token only when all of these are true:

- It is a JWT that the issuer signed, and it is not expired.
- Its subject is the user of the agent's token.
- If it contains the organization claim, the organization is that of the
  agent's token. A service must not decide by another organization than Loom.
- It contains neither `identity.audience` nor `external_agents.audience`. A
  token that Loom sends to a service must not open Loom.

Loom accepts an issue time up to 30 seconds ahead of its own clock.

Loom keeps a service token in memory and reuses it for the same token service
and agent token until 30 seconds before it expires, and never longer than the
agent's token. When Loom sees other permissions for that agent token, it asks
again. If the request fails, service calls fail and `help.list` returns a
`remote_catalog_unavailable` warning. Builtin data remains available. The
operation metric `external_service_token` counts the outcomes.

When a token service leaves discovery or the operator removes its approval,
Loom stops using its tokens at the next module call.

Known limit: the issuer decides the lifetime of a service token. It can be
longer than that of the agent's token. Loom stops using the service token with
the agent's token, but a service accepts it until it expires.

## User profile

The profile is the user's consent. Without an enabled profile, a valid token
gets no access. The external agent cannot read or change the profile.

A **confirming client** writes the profile. It is a registered caller, for
example a chat client, that acts for the user with delegation. This restricts
external agents to users who already use that client.

| Method | Path | Body | Result |
| --- | --- | --- | --- |
| GET | `/v1/users/me/external-agent` | None | Profile. |
| PUT | `/v1/users/me/external-agent` | `{enabled, history, memory, skills, writes, version}` | Updated profile. |

Both routes require `external_agent.profile.write`. PUT uses fresh
authorization and bypasses the positive claims cache. PUT also requires a
delegated request: Loom refuses a user's direct token, also when a role grants
the permission.

```json
{"enabled": true, "history": true, "memory": false, "skills": false, "version": 0}
```

| Field | Meaning |
| --- | --- |
| `enabled` | Master switch. False denies every tool call. |
| `history` | Allows `modules.history.search` and `modules.history.read` in the default chat. The history can contain results of methods that are not marked for external agents. |
| `memory` | Allows `modules.memory.state`, `list` and `read`. Memory writes stay unavailable. |
| `skills` | Allows disclosure from the user's organization skill library. Omission is false, including in requests from older confirming clients. |
| `writes` | Enables the write tools of marked services. Omission is false. Turning it off closes connections that have performed or prepared a write. |
| `version` | Current stored version. Zero creates the profile. A stale value returns `409 external_agent_profile_version_conflict`; read again and resolve. |

The response also contains `confirmed_by` (the actor that wrote the profile)
and `updated_at`. A user without a stored profile gets
`{"enabled":false,"history":false,"memory":false,"skills":false,"version":0}`. Each change
is written to the audit log. A change applies to the next module call, also
inside a script that is already running.

To let a caller write profiles:

1. Add the permission to the caller's manifest. This publishes the service as
   a confirming client.

   ```json
   {"caller": {"permissions": ["chat.read", "external_agent.profile.write"], "auth": {"type": "zitadel", "issuer": "https://id.example.com", "subject": "replace-with-service-subject", "audience": "replace-with-harness-audience"}}}
   ```

   This is a manifest fragment. See [caller registration](modules/discovery.md#caller-registration).
2. Map a role of that service to `external_agent.profile.write` in
   `identity.role_permissions`.

In a delegated request the permission comes from the caller, not from the
user.

## Method mark

A module author decides which methods an external agent can call. Each static
or dynamic method can set `external_agent`:

```json
{"id": "list_slots", "effect": "read", "external_agent": true}
```

This is a method fragment, not a complete manifest.

- The default is false. An unmarked method is absent from the catalog, from
  help and from script bindings, and its dispatch is refused.
- There is no module-level default. A new method never becomes available to
  external agents until its author marks it.
- The mark does not grant permissions. The user's module and method permission
  categories still apply.
- Marked mutations use the separate [write catalog and tools](#service-writes).
  Read scripts contain only read methods.
- A method result can include artifact descriptors. Loom does not import them
  for an external agent and returns only `result`.

Mark a method only when its result can leave Loom's disclosure boundary. The
agent's model is unknown, and its context can also contain text from other
tools.

The mark controls direct calls only. The `history` switch of a profile gives
the agent the chat history, which can contain earlier results of unmarked
methods in the internal agent's answers.

Upgrade Loom before a service publishes `external_agent`,
`external_agent_tokens` or the new caller permission. Older strict v1 readers
reject them. Remove them from static and dynamic documents before a rollback.

## MCP endpoint

`POST /mcp` accepts one JSON-RPC 2.0 message with
`Content-Type: application/json` and returns one JSON response. A successful
`initialize` returns a new `Mcp-Session-Id` header. Send that ID and the OAuth
token with each later request. Loom binds the ID to the token's issuer, user,
organization and OAuth client. A refreshed token of the same identity can keep
using the session. The issuer must report a nonempty `client_id` in
introspection. A missing session ID returns `400`. An expired, revoked or
foreign ID returns `404`.

`DELETE /mcp` with the same authorization and session headers ends the session
and returns `204`. The endpoint opens no server stream. `GET` returns `405`.
Sessions last up to 24 hours from initialization. If the user already has
16 active sessions in the organization, opening another closes the oldest.
Initialization counts toward the per-user rate limit.

Browser pages are not supported clients. A request with an `Origin` header
returns `403 forbidden_origin`, and Loom sends no CORS headers. Native and
server-side clients send no `Origin` header.

| Method | Result |
| --- | --- |
| `initialize` | Protocol version, `tools` capability, server information and instructions. Loom answers with the requested revision when it is `2025-11-25` or `2025-06-18`; otherwise with `2025-11-25`. |
| `ping` | Empty result. |
| `tools/list` | The `script` tool. |
| `tools/call` | Tool result as one text content item with `isError`. |
| Notification (method, no ID) or client response (ID with result or error, no method) | `202` without a body. |

An `MCP-Protocol-Version` header with an unsupported revision returns `400`.
A request ID must be a string or a number. Batches, other ID types, another
`jsonrpc` value and every other message shape return JSON-RPC error `-32600`
with a null ID and start no tool. Unknown methods return `-32601`. A `tools/list` request above
the rate limit returns `-32000` with the message `rate_limited`. Malformed
`initialize` parameters return `-32602`. Opening a session requires an enabled
profile and external disclosure. Disabled access returns `403`.

## Manage MCP access from the main bot

In a normal internal chat the bot can call `modules.mcp_access.list({})` to
name current sessions and `modules.mcp_access.revoke({session_id})` to end one.
Use the reference from `list`. It is different from the transport session ID.
The application refuses the next request or module admission from the revoked
session and withholds any result when revocation is observed before return.
The client can initialize a fresh session while the profile is enabled.

`modules.mcp_access.disable({})` disables the user's profile and ends all
sessions in that organization. New sessions require the confirming client
to enable access again. The profile retains its last confirming client in
`confirmed_by`. These methods require current `chat.read` authority for the
user and are unavailable through MCP.

## The script tool

`script` takes `{"code": "<JavaScript>", "token_service": "<service ID>"}`.
`token_service` is optional; see [selection](#selection). The code runs in the
sandbox worker with the [script contract](modules/scripts-files.md#js-api) of
internal runs: `await` calls, return a JSON value, no network, filesystem or
credentials.

Available bindings:

| Module | Methods | Condition |
| --- | --- | --- |
| `help` | `mcp`, `list`, `read` | Always. The catalog contains only what this session can call. |
| `help` | `write_catalog`, `write_status` | Profile `writes` and a token service. |
| `history` | `search`, `read` | Profile `history`. Default chat only. |
| `memory` | `state`, `list`, `read` | Profile `memory`. |
| `skills` | `list`, `read` | Skills published by available service methods. Profile `skills` also adds the organization library. |
| Service modules | Marked read methods | The call has a [token service](#token-services), and the user has the module and method permissions. |

Start with `return await modules.help.mcp({})` in `script`. It explains the
available MCP tools and shows how to discover reads, inspect method cards,
and continue approved writes. Use `modules.help.list({})` for the authorized
method catalog and `modules.help.read({module, method})` for a method card.

Use `modules.skills.list({})` for names, sources, and use cases. Read a body
with `modules.skills.read({name, source})`, copying the source from the list.
Loom checks every `require` against the current MCP method catalog for both
operations. Select the same token service when reading a skill and calling
its methods. Builtin and run-specific skills and library edits stay inside
the main bot. Skill text cannot grant authority or consent. See the
[skill contract](skills.md#model-interface) for list filters and pagination.

If the service catalog is unavailable, a script still runs with the builtin
modules, and `help.list` returns a `remote_catalog_unavailable` warning.

Script time and parallel calls use the current `script_timeout_ms` and
`parallel_rpc_limit` settings and their deployment ceilings. The other limits
of the [script contract](modules/scripts-files.md#script-failures) apply.

A failed call returns `isError: true` with model-safe JSON:

```json
{"error": "external_agent_disabled", "recovery": "Ask the user to enable external agent access in the chat client that confirmed their Loom account."}
```

| Code | Cause |
| --- | --- |
| `external_agent_disabled` | The profile is absent or disabled. A new connection receives HTTP `403`. |
| `disclosure_denied` | The operator disabled external disclosure. `tools/list` then describes that reason. |
| `rate_limited` | The per-user limit for this minute is used. |
| `busy` | The bound on concurrent external scripts is reached. Retry later. |
| `history_unavailable` | Loom could not store the history record and withheld the result. |
| `invalid_arguments`, `unknown_tool` | Wrong tool name or arguments. |
| `unknown_token_service` | `token_service` names no available [token service](#token-services). |
| `external_session_closed` | The connection ended during execution. |
| Script codes | The classified codes of the script contract, for example `method_unavailable` and `script_timeout`. |

Private error text, backend bodies and credentials are not returned.

## History record

Loom writes the record. The agent cannot omit or change it.

One script that started at least one service, skill, history or memory call creates
one event in the user's default chat. A script that only read help creates no
event. Loom stores the event before it returns the result. If the event cannot
be stored, the agent receives `history_unavailable` instead of the result.

A call is counted when it starts. When the script ends, Loom refuses new calls,
takes the count and then cancels the calls that are still in flight. A call
that the script did not wait for is therefore in the record as `unfinished`.

```json
{
  "type": "external_agent.action",
  "action": "script",
  "source_actor": "external_agent:example-client",
  "text": "External agent example-client ran a read script. Calls: massage.list_slots x2. Outcome: succeeded.",
  "data": {
    "execution_id": "opaque-id",
    "client_id": "example-client",
    "tool": "script",
    "outcome": "succeeded",
    "calls": [{"module": "massage", "method": "list_slots", "count": 2}]
  }
}
```

- `text` is written by Loom from method IDs and counts. It contains no text
  from the agent, no arguments and no results.
- `client_id` is the OAuth client that the issuer reported for the token,
  reduced to at most 128 printable ASCII characters for display.
- `outcome` is `succeeded` or `failed`. A failed script adds `error` with its
  classified code. Each call entry can add `failed` with the number of failed
  calls and `unfinished` with the number of calls that were in flight when the
  script ended.
- Only Loom writes this event type. `POST /v1/chats/{id}/events` refuses it
  with `422 invalid_event`.
- `execution_id` is also the `run_id` value that services receive in the read
  envelope of this script.

Clients that read chat history must ignore or render the new event type. The
internal agent receives it as activity data, not as instructions. In the model
view of a later internal run, these events are compressed together with passive
user actions: the latest three stay, and one summary replaces the older ones.
Stored history and search remain complete.

## Persistence and compatibility

Migrations `00011_external_agent_profiles.sql` and
`00012_external_agent_sessions.sql` add tables. Migration
`00013_external_agent_skills.sql` adds the organization-library switch with a
false default for existing profiles. Before a rollback,
disable the endpoint and remove `external_agent`, `external_agent_tokens` and
the new caller permission from published manifests. Events of type
`external_agent.action` remain in chat history; an older binary gives them to
the internal agent as plain text.

## Service writes

An enabled `writes` profile switch adds the `write` MCP tool alongside
`script`. The confirming chat client sets this switch with the existing
profile route. In `script`, call `modules.help.write_catalog({})` for write
methods and input schemas or `modules.help.write_status({operation_id})`
for a receipt. Select the same token service for creation, receipt lookup
and continuation.
After upgrading, refresh the client's MCP tool list. If a direct-issuer session
is still active, use the original operation ID to check or continue its write.

Only marked, idempotent actions with a status endpoint and no artifact
imports are available. A `write` uses a UUID request ID. Retry with the
same request ID, service, method and arguments to recover its receipt.
The retry never dispatches an existing operation. Keep its operation ID
for status or continuation. The operation belongs to the MCP session.
A method marked `external_agent_protected_arguments` uses a sealed
argument envelope bound to its operation ID. It must have no confirmation
step and must return a redacted receipt. Loom uses its storage keyring
and does not send these arguments to the confirming service. Keep the
previous read key available until sessions with affected operations expire. Loom sends neither the
agent token nor any credentials to the approving user.

The confirming service advertises `external_agent_tokens.approval_path`.
Loom sends the signed-in subject, client label, sealed operation ID,
prepared description and expiry there, using the configured token-service
secret. If the confirming service also prepared the operation, Loom sends
its normalized arguments so the client can verify the saved terms. The
client presents a private Telegram decision. It submits
`POST /v1/users/me/external-agent/approvals/{id}/decision` with delegated
authorization and a body `{"choice":"once"}`. Other choices are `reject`,
`method` and `all`. The deciding actor must match the confirming profile.
The decision route changes consent only. The external client calls
`write` with the original `operation_id` and its current MCP credential
to execute.

`external_agent_remember: true` enables method and all-eligible buttons.
A remembered choice applies only to new writes in that MCP session, up
to 24 hours after initialization.
Per-operation approval remains mandatory on other methods. Revocation and
session expiry remove the remembered choices. The write transport uses the
selected token service's secret in `X-Loom-External-Write-Key` for prepare,
execute and status. This header is sent only to that service.

A write that needs Telegram returns `waiting_approval`. Receipt lookup
alone does not confirm delivery. When delivery is `unconfirmed` and the
prompt did not arrive, call `write` with the original `operation_id`.
It resumes approval delivery without executing an unapproved write.
The profile can have eight pending operations at a time. The confirming
client uses the service's prepared description and shows the MCP client
label. Free text cannot decide an operation. On dispatch, Loom records
the result as a passive event in the user's chat.

The coordinator preserves the run-owned path for internal agents. MCP
operations instead lock the active session and confirming profile before
recording dispatch intent. After a proven pre-dispatch failure, retry
continuation on the original operation. An uncertain dispatch is reconciled
through the status RPC for the same operation while the owning connection
is active. Reading a stored result requires the service to authorize the
operation through its current status RPC. Continuation still requires the
original sealed write contract. Replay with the
same request ID returns a failed receipt with `preparation_interrupted`
after an abandoned preparation.

Migration 00014 preserves old operation receipts and adds an opt-in
switch to existing profiles. Disable writes before rolling back the
binary. The down migration refuses to discard receipts from MCP sessions.

## Acceptance

Fixture tests cover the endpoint, token rules, profile, method mark and history
record. They do not prove these installation properties:

- A real MCP client completes authorization against the installed issuer,
  including client registration.
- The issued token contains the MCP audience, the organization and the user's
  module roles, and no downstream service accepts it.
- The issuer answers the introspection of an MCP token for the configured
  [introspection client](#introspection-client), and the answer contains the
  user's module roles. A client in another project can get other role claims;
  the session then has only the methods that need no permission.
- A [token service](#token-services) checks the secret and the agent's token.
  Its service token contains the roles that the services need and no Loom
  audience.
- A downstream service accepts the service token and refuses the agent's token.
- A chat client reads the capability, writes the profile and handles the new
  history event type.

## Verification

| Behavior | Test |
| --- | --- |
| JSON-RPC exchange, `401` challenge, metadata, disabled routes | `TestMCPEndpointAuthorizesAndServesTools`, `TestMCPEndpointRejectsInvalidExchanges` in `internal/api` |
| Token rules and dropped operation rights | `TestExternalAuthenticationKeepsOnlyModulePermissions` in `internal/identity` |
| A token for one endpoint, or for both, does not open the other | `TestEndpointAudiencesAreMutuallyExclusive` in `internal/identity` |
| Own introspection credentials for MCP tokens; the `/v1` credentials do not change | `TestExternalIdentityUsesOwnIntrospectionClient` in `internal/app` |
| Token service request: Loom's secret and the agent's token, only to a service that declared the endpoint | `TestExternalTokenServiceGetsSecretAndSubjectToken` in `internal/modules` |
| Service token checks: issuer signature, same user and organization, never a Loom audience, expiry, reuse for one token service | `TestServiceTokenBindsUserAndNeverOpensLoom` in `internal/identity` |
| Method mark, refused unmarked dispatch, no artifact import | `TestExternalAgentReadsOnlyMarkedMethodsWithoutArtifacts` in `internal/modules` |
| Profile switches, token service selection, service token sent instead of the agent's token, direct profile write refused, zero credits, limit for a caller without access, history record, no run, withheld result, concurrency bound, token and profile revocation inside a script | `TestExternalAgentReadsRecordHistoryWithoutRunOrCredits` in `internal/app` |
| Profile right comes from the caller binding, not from the user's role | `TestProfilePermissionComesFromCallerBinding` in `internal/identity` |
| Session ownership, revocation, profile disable and replacement of the oldest session | `TestExternalAgentSessionsKeepOwnershipAndRevokeIndependently` and `TestExternalAgentSessionOverflowClosesOldestConnection` in `internal/store` |
| Compression in run context, refused event type on the public route | `TestPassiveContextKeepsHistoryAndLatestActions` in `internal/store` |
| Upgrade from the previous schema with existing data | `TestExternalAgentProfileMigrationKeepsExistingData` in `internal/store` |

The tests in `internal/app` and `internal/store` need `CLX_TEST_DATABASE_DSN`. See [QA setup](qa/setup.md).
