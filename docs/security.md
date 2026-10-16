# Authorization and trust boundaries

Этот документ описывает действующие границы и отдельно целевой refreshable flow. Текущий MVP использует access-token sessions с explicit reauthorization; автоматический Token Exchange/refresh и deployment-bound secret authentication не реализованы. [PKCE panel login](operator-login.md) — отдельный действующий механизм.

Для интеграции начните с [identity и permissions](security/identity-permissions.md), затем прочитайте [approval и границы данных](security/actions-data.md). [Refresh plan](security/refresh-plan.md) описывает будущий протокол и не является инструкцией для действующей интеграции. Ключи сохранённых сессий описаны в [key rotation](session-key-rotation.md).

## Identity and access

Read [Identity permissions](security/identity-permissions.md).

## Harness permissions

Read [Identity permissions](security/identity-permissions.md).

## Planned refresh flow (not implemented)

Read [Refresh plan](security/refresh-plan.md).

## Requests and confirmation

Read [Actions data](security/actions-data.md).

## Scripts, data and external disclosure

Read [Actions data](security/actions-data.md).

## Sources

Read [Actions data](security/actions-data.md).
