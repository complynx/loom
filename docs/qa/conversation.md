# API and conversation QA

Use the [HTTP contract](../api.md) and [reply-mode contract](../conversational-input.md).

## Repeatable fixture checks

Prepare the disposable database in [setup](setup.md), then run from the repository root:

```sh
go test -v -race -parallel=4 -tags=integration -count=1 ./integration \
  -run 'Test(DefaultChatAndDurableRunContract|HTTPMutationRequiresApprovalAndExecutesStableOperationOnce|ConversationClarifiesAcrossMessagesThenResolvesExactAction|CriticalActionUsesBinaryOrExactLocalizedText)$'
go test -v -race -count=1 ./internal/identity
```

The [conversation fixtures](../../integration/conversation_test.go) and
[mutation fixture](../../integration/mutation_test.go) show exact request bodies,
receipts and expected transitions. The identity tests use HTTP authority fixtures;
they do not prove configuration against a real Zitadel instance. The integration
authenticator accepts test tokens, so it cannot verify token protocol behavior.

For installation checks below, use the deployed `/openapi.json` for request schemas
and record the run ID, input/operation IDs and expected result for each scenario.
Use separate chats for independent cases. Do not continue an uncertain write until
its service status or receipt establishes the effect.

## Basic run

1. Obtain a user access token and create an authorization session. Read the default chat. Create a run with its `auth_session_id` and repeat its idempotency key. Expect the same run; a changed body with that key must conflict.
2. Complete a read-only request. Check history, events, usage and balance/spent/reserved. Restart after completion: no second inference or charge.
3. Run work in two chats. A second nonterminal run in the same chat must fail.
4. Append a passive action: history changes, but no inference starts. Submit an active request and check passive-history compression without deleting full history.
5. Disconnect SSE and reconnect with Last-Event-ID. Read persisted events and deduplicate by seq. Do not expect provider token deltas.

## Identity

| Scenario | Expected result |
| --- | --- |
| Invalid signature/issuer/audience or expired token | Deny protected reads and dispatch. |
| Foreign user/org/chat ID | Deny access. Cross-org delegation is not supported. |
| Caller plus delegated user | Both authorities apply; a manifest cannot grant user permission. |
| Role removal | Sensitive operations use fresh authority. Ordinary cache use stays within TTL/exp. |
| IAM outage | Warm positive entries work only within validity. Cold/expired entries fail closed; no stale-on-error. |
| Cache purge | Old generations cannot be reused or repopulated. Check all API instances. |
| Signing-key rotation | Load the new kid through trusted JWKS. Test JWKS and introspection failure separately. |
| Queue restart and reauthorize | Preserve run/operation identity; fresh credentials represent the same party. Test revocation and TTL expiry separately. |
| Panel login | Proxy SSO and direct PKCE are separate configurations. Deny missing diagnostics.read, foreign-org roles, and service/delegation headers on proxy routes. |

## Replies and consent

Test protocol and agent reply modes separately. Agent-mode typed text and ordinary button captions use one steering stream. Close the form, ask a clarification, send other messages, then return to the action. Accepted input must wake the same run and return a stable receipt on retry.

An amendment rejects the old prepare. A new prepare needs fresh consent. Input accepted before the dispatch boundary must block superseded consent. After dispatch, verify the actual or unknown effect; do not promise rollback.

For critical actions with native callbacks, approve/reject binds the exact action without model classification. Without callbacks, require the agent's exact localized phrase. No trimming, case folding or codes. Any other nonempty message rejects; empty input is invalid. Test expiry and stale presentation.

Clients must serialize input and display one pending action. Without a reply reference, identical delayed text and fresh text are indistinguishable.

Test live-model interpretation separately: RU/EN approval, refusal, conditional consent, amendments, unrelated questions and late replies. Scripted providers prove runtime gates, not language understanding.

## Modules and writes

1. Register a real module by URI label. Check schema, help, categories, dynamic listing and cache expiry. Forbidden methods must be hidden and denied on direct invocation.
2. Prepare a write: no business effect yet. Restart while waiting: operation ID, arguments and expiry survive.
3. Approve once. Replay input and query status: one effect and one durable receipt. Include nested JSON across PostgreSQL JSONB persistence.
4. Change rights, contract or preconditions after prepare: deny the old dispatch.
5. Lose the response after the service commits: reconcile the same operation ID, without blind execution.
6. Remove and restore a caller registration. Distinguish temporary discovery unavailability from credential denial. The client retains run/session/deadline and creates no replacement task.
