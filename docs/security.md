# Авторизация и границы доверия

Этот документ описывает действующие границы и отдельно целевой refreshable flow. Текущий MVP использует access-token sessions с explicit reauthorization; автоматический Token Exchange/refresh и deployment-bound secret authentication не реализованы. [PKCE panel login](operator-login.md) — отдельный действующий механизм.

Для интеграции начните с [identity и permissions](security/identity-permissions.md), затем прочитайте [approval и границы данных](security/actions-data.md). [Refresh plan](security/refresh-plan.md) описывает будущий протокол и не является инструкцией для действующей интеграции. Ключи сохранённых сессий описаны в [key rotation](session-key-rotation.md).

## Identity и доступ

Read [Identity permissions](security/identity-permissions.md).

## Права harness

Read [Identity permissions](security/identity-permissions.md).

## Целевой refreshable flow — не реализован

Read [Refresh plan](security/refresh-plan.md).

## Поручение и подтверждение

Read [Actions data](security/actions-data.md).

## Скрипты, данные и внешняя передача

Read [Actions data](security/actions-data.md).

## Источники

Read [Actions data](security/actions-data.md).
