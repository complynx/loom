# Conversation retry

[Guide index](../failures.md)

<a id="сообщения-и-подтверждения-при-сбоях"></a>
## Lost receipts and interrupted actions

| Situation | Client action and runtime boundary |
| --- | --- |
| Input HTTP receipt lost | Repeat the same idempotency key and body. Do not create a new run. The original input ID is returned where the receipt contract provides it. |
| Queue insertion fails | Admission rolls back with its decision. Retry the original request. |
| Steering arrives while a question is prepared | Unprocessed input prevents publishing a stale wait; the run remains queued. |
| Clarification during ordinary approval | Continue the same run. The prepared action remains blocked. |
| Reply differs from `exact_text` | The runtime records rejection, including when the reply was a clarification. An expired phrase or conflicting presentation cannot approve the action. |
| Late callback or reply to an old proposal | Check the action ID/binding and presentation. Consent does not transfer to a replacement action. Ambiguous ordinary text requires clarification. |
| Correction arrives before dispatch intent | Unprocessed steering or a new revision blocks execution under old consent. |
| Message arrives after dispatch intent | Do not promise rollback. Obtain or reconcile the original operation's actual outcome first. |
| Restart after a decision | Recover the stored input, decision and operation ID. Query unknown effects rather than creating a replacement action. |

Loom has no general client message-delivery acknowledgment. A snapshot describes server state at admission; `presentation` is context reported by the client. In text channels without reply references, identical late and current replies cannot be distinguished. Present pending actions sequentially. Use binary callbacks for strict binding. See the [full input contract](../conversational-input.md).

## Retry policy

Provider calls use the [hybrid policy](../hybrid-retry.md). Authentication failures, invalid schema, context overflow and expired approvals are not transient retry signals. Preserve operation identity; account for new provider attempts under the applicable financial policy.

A general dependency circuit breaker is not implemented. If you add one, separate provider/service calls from recovery, status and authorization traffic, and bound half-open probes. A breaker cannot make an unknown write safe to repeat.

Manual reconciliation requires the operation ID, backend evidence, author and reason. An administrator must not change `unknown` to `not_started` without evidence to permit another attempt. If evidence is unavailable, leave the operation unresolved. Further work must avoid a conflicting action and explain the uncertainty to the user.
