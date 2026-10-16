<a id="dependencies"></a>

# Dependency failures

[Guide index](../failures.md)

<a id="матрица-зависимостей"></a>
## Symptoms and recovery

Start with the request, run and operation IDs in [diagnostics](../diagnostics.md). Read the persisted state before sending another command. The table describes recovery boundaries; it is not a deployment acceptance report.

| Symptom | Check and recovery action |
| --- | --- |
| API, ingress, DNS or TLS unavailable | Restore connectivity. Retry the original request with the same key/body, or read the known run. Resume SSE from its cursor. Do not create a replacement task because its receipt was lost. |
| Database unavailable before admission | The API cannot accept durable work. Restore PostgreSQL and retry with the same key. |
| Database commit succeeded but response was lost | Repeat the original idempotent command; read the resulting run. |
| Database lost during inference or after an external write | The result or usage can be unknown. Restore the database and inspect persisted intent and operation status before retrying. |
| Database storage full or read-only | Readiness can still return 200. Check storage and write errors separately, restore writes, then inspect affected intents. |
| Complete database loss | Restore behind the [recovery barrier](../recovery-mode.md). Reconcile external effects after the backup point before allowing dispatch. |
| Provider 429 or 503 | Only recognized output-free rejections use [hybrid retry](../hybrid-retry.md). Exhaustion pauses with `provider_retry_limit`. Other 5xx responses are not a generic safe retry signal. |
| Provider stream lost, timeout, or usage missing | Preserve any partial output as incomplete. Check unknown usage and [sponsored recovery limits](../active-compaction.md); do not release uncertain reservations without evidence. |
| Provider credentials, model, billing or quota rejected | Correct the configuration or budget. Do not retry a known invalid request indefinitely or silently change the paid model. |
| Compaction unavailable | Inspect context limits and the saved summary. Do not remove constraints or truncate history silently to force execution. |
| Authorization expired or revoked | Inspect `waiting_auth` and denial reason. Use explicit reauthorization for the same party. Loom does not automatically refresh tokens. |
| Identity authority or JWKS unavailable | Cache behavior depends on the check and TTL. Sensitive checks require online authority. Follow [identity cache](../identity-cache.md); do not bypass denial with stale credentials. |
| Docker discovery or manifests unavailable, stale or conflicting | Check discovery diagnostics and trusted registrations. Cached trust is bounded by freshness TTL; correct the registration or rollout before new calls. |
| Read RPC timed out | Check the method contract and budget. A read can still have a charge; network failure alone does not justify another paid call. |
| Write RPC timed out, returned 502, or lost its response | Treat the outcome as unknown. Query the same operation ID. An ambiguous `not_found` does not prove that execution never occurred. |
| Status endpoint unavailable | Leave the operation unresolved. Restore status access or obtain external evidence for privileged reconciliation. |
| Approval or precondition expired | Prepare again and obtain fresh approval bound to the new action. |
| Script deadline, worker crash or OOM | Inspect persisted operation outcomes. Fence stale dispatch; do not replay a script that may have written externally. See [crash recovery](recovery.md#crash-recovery). |
| Artifact storage unavailable or bytes corrupt | Do not deliver incomplete/corrupt data. Restore the bytes or import again from the trusted source. Temporary artifacts are not durable originals. |
| Client disconnect or slow SSE consumer | Reconnect and replay durable events. Follow the run's disconnect policy; an SSE connection is not the run itself. |
| Duplicate input or competing run in one chat | Reuse the original key/body. Inspect a conflict rather than issuing a new key; one nonterminal run occupies a chat. |
| Budget exhausted or reserve uncertain | Inspect credit and unresolved usage records. Restore budget or reconcile, then explicitly resume eligible work. |
| Worker loses its lease | Old ownership must not dispatch new calls. Recovery uses a new fence and reconciles operations already sent. |
| Host overload or clock drift | Inspect container/host resources and clock synchronization. Admission can fail while accepted work remains durable. Do not widen token leeway to conceal clock drift. |

For controls not supplied by Loom, configure database/storage alerts, container resource monitoring and backup retention in your deployment system. Use [observability](../observability.md) and the affected [QA scenarios](../qa.md) to verify recovery.
