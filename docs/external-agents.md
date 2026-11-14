# External agents

A user can connect their own agent to Loom through the Model Context Protocol
(MCP). The agent uses the same command interpreter that the internal agent
uses: one `script` tool with permission-filtered modules. Loom does not run
inference for these calls. The user's agent supplies its own model.

This page is the contract for operators, chat clients, module authors and
clients of the MCP endpoint. It also records the agreed design for the parts
that are not implemented.

## Status

| Part | State |
| --- | --- |
| MCP endpoint, OAuth resource metadata, `script` tool with help, history and memory reads | Implemented. |
| Service reads through marked methods | Implemented with a separate token from a [token service](#token-services). Off until a discovered service declares a token endpoint and the operator approves it. |
| User profile written by a confirming chat client | Implemented. |
| `external_agent` method mark in manifests | Implemented for read methods. |
| History record for each external action | Implemented. |
| Acceptance with a real MCP client and the installed issuer | Open. See [Acceptance](#acceptance). |
| External service writes and personal-memory writes | Not implemented. See [Planned writes](#planned-writes). |
| Files, skills, paid transcription, diagnostics and administration tools | Not available to external agents. |

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
| `token_services` | Optional. Approves [token services](#token-services): each key is a service ID, each value is the secret that authenticates Loom to that service. A secret has 16 to 512 printable ASCII characters without spaces. At most 32 entries. Without an entry only help, history and memory are available. |
| `calls_per_minute` | Tool calls and tool listings per user in one fixed minute, 1..600. Zero or omission uses 30. The limit also counts calls of a user without access. It is local to one API process. |

When `enabled` is false, the other fields must be absent. In recovery mode the
endpoint and the profile routes are not served and return `404`.

`GET /v1/capabilities` reports the feature to registered clients:

```json
{"external_agent": {"version": 1, "resource": "https://loom.example.com/mcp", "access": ["history", "memory"], "token_services": ["replace-with-service-id"]}}
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
Metadata Document or dynamic registration) is the operator's decision. Loom
does not proxy registration.

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
catalog of the session is empty.

### Token services

By default external agents cannot call service methods. The catalog, the
bindings and dispatch contain only help, history and memory.

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
`remote_catalog_unavailable` warning; history and memory stay available. The
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
| PUT | `/v1/users/me/external-agent` | `{enabled, history, memory, version}` | Updated profile. |

Both routes require `external_agent.profile.write`. PUT uses fresh
authorization and bypasses the positive claims cache. PUT also requires a
delegated request: Loom refuses a user's direct token, also when a role grants
the permission.

```json
{"enabled": true, "history": true, "memory": false, "version": 0}
```

| Field | Meaning |
| --- | --- |
| `enabled` | Master switch. False denies every tool call. |
| `history` | Allows `modules.history.search` and `modules.history.read` in the default chat. The history can contain results of methods that are not marked for external agents. |
| `memory` | Allows `modules.memory.state`, `list` and `read`. Memory writes stay unavailable. |
| `version` | Current stored version. Zero creates the profile. A stale value returns `409 external_agent_profile_version_conflict`; read again and resolve. |

The response also contains `confirmed_by` (the actor that wrote the profile)
and `updated_at`. A user without a stored profile gets
`{"enabled":false,"history":false,"memory":false,"version":0}`. Each change
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
- Only read methods are served now. A marked write or delete method stays
  unavailable until [writes](#planned-writes) exist.
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
`Content-Type: application/json` and returns one JSON response. The endpoint is
stateless: it issues no session ID and opens no server stream. `GET` and
`DELETE` return `405`.

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
`initialize` parameters return `-32602`.

## The script tool

`script` takes `{"code": "<JavaScript>", "token_service": "<service ID>"}`.
`token_service` is optional; see [selection](#selection). The code runs in the
sandbox worker with the [script contract](modules/scripts-files.md#js-api) of
internal runs: `await` calls, return a JSON value, no network, filesystem or
credentials.

Available bindings:

| Module | Methods | Condition |
| --- | --- | --- |
| `help` | `list`, `read` | Always. The catalog contains only what this session can call. |
| `history` | `search`, `read` | Profile `history`. Default chat only. |
| `memory` | `state`, `list`, `read` | Profile `memory`. |
| Service modules | Marked read methods | The call has a [token service](#token-services), and the user has the module and method permissions. |

The tool description contains the same environment overview and navigation
guidance that the internal agent receives. Start with
`return await modules.help.list({})` and read a method card before an
unfamiliar call.

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
| `external_agent_disabled` | No enabled profile. `tools/list` then describes the tool as disabled. |
| `disclosure_denied` | The operator disabled external disclosure. `tools/list` then describes that reason. |
| `rate_limited` | The per-user limit for this minute is used. |
| `busy` | The bound on concurrent external scripts is reached. Retry later. |
| `history_unavailable` | Loom could not store the history record and withheld the result. |
| `invalid_arguments`, `unknown_tool` | Wrong tool name or arguments. |
| `unknown_token_service` | `token_service` names no available [token service](#token-services). |
| Script codes | The classified codes of the script contract, for example `method_unavailable` and `script_timeout`. |

Private error text, backend bodies and credentials are not returned.

## History record

Loom writes the record. The agent cannot omit or change it.

One script that started at least one service, history or memory call creates
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
  reduced to at most 128 printable ASCII characters. A dynamically registered
  client chooses its own name; treat the value as a claim, not as a verified
  identity. An absent value is recorded as `unknown`.
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

Migration `00010_external_agent_profiles.sql` adds one table. It does not
rewrite existing rows. Older binaries ignore the table. Before a rollback,
disable the endpoint and remove `external_agent`, `external_agent_tokens` and
the new caller permission from published manifests. Events of type
`external_agent.action` remain in chat history; an older binary gives them to
the internal agent as plain text.

## Planned writes

The design below is agreed and not implemented. It needs an accepted
[token service](#token-services) in the installation first.

- **Operations without a run.** A write keeps the prepare, consent, dispatch
  and status-recovery contract of [durable operations](modules/calls-confirmation.md).
  Its owner becomes the external session instead of a run. Dispatch does not
  use the run engine.
- **Standalone confirmations.** Every confirmed external write uses a separate
  chat message with inline approve and cancel controls bound to its approval
  ID. Free text never approves an external action. Approvals of the internal
  agent keep their existing reply modes.
- **Source.** The approval carries its source, and the message shows it.
- **System text.** The message text comes from Loom and from the service's
  prepare description. Text from the external agent is not shown.
- **Resolution.** After the decision is accepted, the client removes the
  controls and changes the message to confirmed or cancelled. An expired
  preparation changes the message to expired.
- **Limit.** The number of unresolved confirmations per profile is bounded.
- **Delivery.** The confirming client of the profile receives its approvals.
- **Profile.** Service writes and memory writes get their own profile
  switches. A memory write also needs a standalone confirmation that shows
  the exact text.
- **Result.** A write call returns `waiting_approval` with its operation ID.
  The agent reads the outcome by that ID. The outcome is also written to
  history.

## Acceptance

Fixture tests cover the endpoint, token rules, profile, method mark and history
record. They do not prove these installation properties:

- A real MCP client completes authorization against the installed issuer,
  including client registration.
- The issued token contains the MCP audience, the organization and the user's
  module roles, and no downstream service accepts it.
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
| Token service request: Loom's secret and the agent's token, only to a service that declared the endpoint | `TestExternalTokenServiceGetsSecretAndSubjectToken` in `internal/modules` |
| Service token checks: issuer signature, same user and organization, never a Loom audience, expiry, reuse for one token service | `TestServiceTokenBindsUserAndNeverOpensLoom` in `internal/identity` |
| Method mark, refused unmarked dispatch, no artifact import | `TestExternalAgentReadsOnlyMarkedMethodsWithoutArtifacts` in `internal/modules` |
| Profile switches, token service selection, service token sent instead of the agent's token, direct profile write refused, zero credits, limit for a caller without access, history record, no run, withheld result, concurrency bound, token and profile revocation inside a script | `TestExternalAgentReadsRecordHistoryWithoutRunOrCredits` in `internal/app` |
| Profile right comes from the caller binding, not from the user's role | `TestProfilePermissionComesFromCallerBinding` in `internal/identity` |
| Compression in run context, refused event type on the public route | `TestPassiveContextKeepsHistoryAndLatestActions` in `internal/store` |
| Upgrade from the previous schema with existing data | `TestExternalAgentProfileMigrationKeepsExistingData` in `internal/store` |

The tests in `internal/app` and `internal/store` need `CLX_TEST_DATABASE_DSN`. See [QA setup](qa/setup.md).
