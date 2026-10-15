# Конфигурация, эксплуатация и реализация

Этот документ сохраняет целевой контракт v1 и первоначальный план. Текущее состояние описано в [ограничениях MVP](mvp-limitations.md) и [поддержке настроек](../internal/config/SUPPORT.md). После реализации MVP дальнейшая работа ведётся по [треку разработки](development-track.md); начальные этапы ниже не означают, что весь код ещё предстоит написать.

## Разделение конфигурации

`config.yaml` плюс env содержат инфраструктурные подключения, доверенные issuer/audiences, provider credentials, Docker proxy endpoint, секретные bindings, encryption key paths, blob root и жёсткие resource ceilings. Значения env имеют приоритет; неизвестные ключи/неверные значения не допускают startup. Секреты не возвращаются административным API. [Пример](../examples/config.yaml) содержит только placeholders.

БД содержит typed динамические настройки: model catalog/capabilities, defaults, tariff revisions, run/user quotas, context budgets, ожидания, retention и политику внешней передачи. Пользовательские preferences выбирают только разрешённую модель/effort. Изменение entitlements — отдельное admin право.

В config задаются bootstrap defaults. При первом запуске они создают DB revision; на следующих стартах не перезаписывают admin settings. Effective settings: hard ceiling из config, системная revision из БД, user policy, более узкий request budget. Совместимость версии settings проверяется до readiness.

Реализованный `PATCH /v1/admin/settings` принимает полный типизированный объект настроек с обязательной актуальной `version` в JSON body; это не JSON Merge Patch и не `If-Match`/ETag. Несовпадение — 409; валидация целого patch перед commit; audit в одной транзакции. API выдаёт effective values и revision без секретов. Модель/effort другого пользователя меняются отдельной ручкой и правом.

Новые run фиксируют policy/settings/tariff snapshot. Safety revocation и блокировки применяются немедленно на следующей проверке. Deployment ceilings фиксируются при startup; отдельного live emergency-ceiling API нет. Обычный settings update влияет на retention новых ресурсов и не сокращает сохранённые сроки существующих данных. Privileged операция `future_only|all_existing` остаётся планом и не реализована.

## Deployment

- Linux/Docker, linux/amd64 (complynx.net подтвердил Linux x86_64). Go service за Traefik; внутренние service RPC по разрешённой доверенной Docker-сети. Server template использует существующую `traefik-net`; proxy и metrics имеют отдельные сети. Base images фиксируются digest sha256; сборка и обязательные integration gates выполняются в Linux-контейнере.
- Отдельный PostgreSQL database/user, migrations под отдельной ролью. Runtime не использует PostgreSQL superuser.
- Persistent volumes для artifacts, encrypted credentials metadata в БД; encryption keys вне БД, включены в отдельный защищённый backup.
- Docker discovery через ограниченный API proxy, без прямого полного Docker socket в API контейнере.
- Sandbox workers non-root, без network, с CPU/memory/PID limits и ограниченным IPC. OS изоляцию надо подтвердить Linux runtime spike: rlimits для процесса сами по себе не изолируют файловую систему.
- `/readyz` выключается при DB failure/drain. Provider failure не делает весь HTTP API недоступным: статусы и ожидания остаются читаемыми.
- Сервис начинает graceful shutdown на SIGINT/SIGTERM; container grace > 5 секунд. Rollout не должен одновременно удалять все persistent state.

## Наблюдаемость

Structured logs: request/run/step/operation IDs, actor/subject identifiers, статусы и sanitized error codes. По умолчанию raw payloads не логируются. Явный `debug` + `log_payloads: true` включает ограниченные санитизированные payloads; JWT claims и личный текст могут быть видны. См. [точную logging policy](observability.md). Не использовать user IDs как metric labels.

Целевой перечень metrics: admission/queue depth, активные и waiting run, latency шагов, dependency errors, stale registry, unknown operations/usage, reserve age, authorization refresh failures, context sizes, worker kills/OOM, shutdown duration, blob space, reconciliation lag. Admin diagnostics показывают точные blockers и correlation IDs без секретов.

Целевой перечень alerts: DB unavailable/storage, неизвестный write сверх окна сверки, auth refresh uncertainty, stale discovery, превышение shutdown bound, высокий error rate, backup/restore failure, storage forecast. Алерты должны быть actionable, не на каждый transient retry. Реальные exported series и поставляемые rules описаны в [observability](observability.md); публикация rules и доставка notifications не доказаны наличием этого списка.

## Исторические этапы реализации

1. **Интеграционные spikes.** Проверить installed Zitadel: claims/audiences, exchange+offline_access, rotation/revocation; проверить JS engine, async host RPC и OS kill/isolation. Зафиксировать конкретные зависимости и версии. Непрерывная делегация и sandbox security — release gates, не предположения.
2. **Durable API skeleton.** Go routing, DB migrations, identities/default chats, run state machine, idempotency, leases, SSE replay. Fake provider/module только для проверок контракта.
3. **Один end-to-end read-only run.** Реальный provider adapter, модели/preferences, history/context/search, один discovered сервис с read методом, artifacts, JS worker и бюджет.
4. **Управление и изменения.** Questions, steering, два approval протокола, write idempotency/status/cancel, reconciliation, refreshable auth, administrative settings.
5. **Отказы и эксплуатация.** Fault scenarios из failures.md, shutdown SLO, deployment integration, backup/restore, нагрузочные пределы. Затем приёмка первой версии.

Начальные packages: `api`, `identity`, `policy`, `runs`, `history`, `billing`, `modules`, `sandbox`, `artifacts`, `store`, `config`. Не вводить абстрактные frameworks сверх реальных границ. Выбраны pgx + sqlc, SQL migrations Goose, HTTP/OpenAPI Huma + ServeMux и PG jobs River core. Goja с eventloop — выбранный кандидат для изолированного worker; окончательное принятие после spike. [Полный набор, лицензии и коммиты](dependencies.md).

## Приёмка v1

Рабочая версия должна доказать: два чата работают параллельно, один чат защищён от конкурирующих run; UI append пассивен; права на чтение/запись независимы; regex и fulltext ограничены; summary не выдаёт approval; модуль подключается одним label; токены не доступны JS; кредиты учитываются без дублей; write recovery не делает blind retry; вопросы/steering и артефакты работают через реальный API; SIGTERM и recovery проверены. Пройти fault gates и restore rehearsal на изолированном стенде.

Документация не является доказательством реализации, QA или production readiness. Commit, push, deployment и изменение соседних сервисов требуют отдельного поручения.

## Исторические решения первоначального плана

Этот список сохраняет исходный план. Принятые решения, реализованные возможности и оставшиеся deployment inputs описаны в треке и server rollout; пункты ниже не являются актуальным перечнем незавершённого кода.

- Подтвердить refreshable flow на реальном Zitadel и bootstrap client registration/scopes.
- Выбрать первый provider/model и первый сервис для end-to-end интеграции; включить его права и endpoint contract.
- Утвердить стоимость microcredit и стартовые тарифы/квоты; defaults из примера не финансовая политика.
- Подтвердить OS worker isolation и конкретный engine.
- Утвердить retention defaults, backup destination и измеримые RPO/RTO.
- Принять следствие общего чата: сервис с chat.read и делегацией видит его общий контекст. Если появятся закрытые области, нужны отдельные чаты/ACL, а не фильтрация только ответа.
