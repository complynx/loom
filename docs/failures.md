

# Failures and recovery

Use these guides to distinguish a failed request from an uncertain external effect. Commands and source paths use the repository root unless stated otherwise.

## Policy

[Failure policy](failures/policy.md) defines safe retry and reconciliation boundaries.

## Dependencies

[Dependency failures](failures/dependencies.md) maps symptoms to checks and recovery actions.

<a id="retry-policy"></a>
## Inputs and retries

[Conversation retry](failures/conversation-retry.md) covers lost receipts, approvals and interrupted actions. [Hybrid provider retry](hybrid-retry.md) defines the separate provider rejection policy.

## Recovery procedures

[Shutdown, crash recovery and backups](failures/recovery.md) explains the recovery process. Use [restore recovery mode](recovery-mode.md) before starting a restored database. [Operations](operations.md) covers installation checks; [QA](qa.md) covers acceptance.
