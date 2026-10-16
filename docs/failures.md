<a id="недоступности-отказы-и-восстановление"></a>

# Failures and recovery

Use these guides to distinguish a failed request from an uncertain external effect. Commands and source paths use the repository root unless stated otherwise.

<a id="реализованный-срез"></a>
<a id="базовые-правила"></a>
## Policy

[Failure policy](failures/policy.md) defines safe retry and reconciliation boundaries.

<a id="матрица-зависимостей"></a>
## Dependencies

[Dependency failures](failures/dependencies.md) maps symptoms to checks and recovery actions.

<a id="сообщения-и-подтверждения-при-сбоях"></a>
<a id="retry-policy"></a>
## Inputs and retries

[Conversation retry](failures/conversation-retry.md) covers lost receipts, approvals and interrupted actions. [Hybrid provider retry](hybrid-retry.md) defines the separate provider rejection policy.

<a id="остановка"></a>
<a id="recovery-после-crash"></a>
<a id="backups-и-disaster-recovery"></a>
<a id="критерии-qa-изменений"></a>
## Recovery procedures

[Shutdown, crash recovery and backups](failures/recovery.md) explains the recovery process. Use [restore recovery mode](recovery-mode.md) before starting a restored database. [Operations](operations.md) covers installation checks; [QA](qa.md) covers acceptance.
