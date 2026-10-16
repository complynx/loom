# Данные, контекст и бюджет

## Сущности PostgreSQL

Таблица ниже — концептуальная схема первоначального плана; её названия и поля не являются текущей SQL-схемой. Исполняемая схема находится в [migrations](../migrations), точные HTTP DTO — в `/openapi.json`. Например, refresh leases и persisted service registry из плана не означают реализованный refresh или registry в БД.

| Сущность | Основные поля и связи |
|---|---|
| `users` | id, issuer, subject, status, created_at; UNIQUE(issuer, subject) |
| `memberships` | user_id, org_id, status; UNIQUE(user_id, org_id) |
| `user_preferences` | user_id, model_id, effort, version |
| `user_policies` | user/org, model entitlements, quotas, version |
| `chats` | id, user_id, org_id, is_default, next_seq, deletion_state |
| `chat_events` | chat_id, seq, kind, source_actor, source_event_id, run_id, text, data, occurred_at, received_at |
| `runs` | chat/subject/actor/billing IDs, state/reason, input, settings snapshot, context boundary, lease owner/expiry, fencing token, deadlines, request flags |
| `run_events` | run_id, seq, type, sanitized payload, created_at |
| `run_inputs` | run_id, kind, payload, application status, idempotency reference |
| `steps` | run_id, ordinal, kind, state, model/manifest identity, input/result references, generation_id |
| `tool_operations` | operation_id, step/run, service/module/method, argument hash, idempotency key, state, dispatch intent, status proof, reconcile deadline |
| `questions` | run, schema, pending/answered/expired, answer input, expires_at |
| `approvals` | run/subject/actor, method/argument hash, prepared parameters/precondition, decision, expiry, operation_id |
| `context_summaries` | chat_id, through_seq, source revision/hash, summary text, model ID, version, validity |
| `artifacts` | owner/org/chat/run, blob key, MIME, size, hash, disclosure policy, expiry/deletion state |
| `auth_sessions` | subject/actor/org, OAuth client/scope/audience, encrypted token fields, key ID, version, refresh lease, state/expiry |
| `service_registry` | Docker identity, service ID, manifest/revision/hash, freshness, endpoints, registration status |
| `billing_accounts` | owner/org, currency=internal_microcredits, posted_balance, reserved_balance, version |
| `credit_ledger` | account, entry kind/amount, run/step, rate revision, external usage reference, unique settlement key |
| `reservations` | account/run/step, amount, state, uncertainty, expiry policy |
| `settings` | scope, typed value, revision, author/time |
| `idempotency_records` | actor/subject/route/key, request hash, result reference, expiry |
| `audit_events` | actor/subject/org, action, target, before/after, reason, request ID |

Сервисные субъекты и deployment bindings — отдельные identities, не обычные пользовательские аккаунты. DB JSONB используется для контрактных payload/schema, а не вместо всех связей и ограничений.

## Сохранённый контекст диалога

Разговорный механизм использует существующие таблицы и JSONB, без отдельной таблицы для каждой формы:

- `runs.doc`: `interaction.reply_mode`, `conversation_revision`, последний `conversation_input`, текущие `question` и `approval`. Approval сохраняет подготовленный call, способ ответа, параметры/hash, срок, решение и ссылки `decision_input_id`/`resolution_call_id`.
- `run_inputs`: точный вход и его применение. Для разговорного steering `doc.context` содержит серверный `revision`, `source_actor` и известные при приёме IDs вопроса/действия; `doc.presentation` отдельно хранит необязательные сведения клиента. Клиент не назначает `id`, `context` или `applied`.
- `idempotency_records`: hash исходного запроса и ID принятого input. Повтор возвращает исходный input, даже если run уже получил новые сообщения. Legacy-записи могут ссылаться только на run и не дают input receipt.
- `write_operations.doc`: подготовка, binding и `consent_revision`; перед dispatch они проверяются под run lock. Отдельного model-text parser или второго классификатора нет.
- `steps`: сохранённые model/input/tool checkpoints. Внутренний шаг `interaction` сохраняет результат исполнения прямого бинарного/строгого ответа, не создавая фиктивный вызов модели.
- `chat_events` и `run_events`: принятые входы, вопросы/предложения и связи решений. `interaction.resolved` записывается agent resolver и exact-text fallback и не обещает бизнес-успех; native input сохраняется в chat history.

`conversation_revision` задаёт порядок входов внутри run; это не chat history seq и не SSE cursor. Последний input, текущие question и approval передаются модели отдельно от сжатой истории. Summary не может заменить эти записи или перенести согласие на новую операцию. Схемы и границы проверок — в [контракте диалога](conversational-input.md).

## Обязательные инварианты

- Partial UNIQUE на живой дефолтный чат по user_id. Его org фиксируется при создании; обращение из другой org требует явного чата.
- Partial UNIQUE на runs(chat_id), если состояние нетерминальное. Даже paused/waiting run удерживает слот.
- UNIQUE(chat_id, seq), UNIQUE(run_id, seq), UNIQUE(run_id, ordinal).
- UNIQUE(actor_id, source_event_id) для внешних UI-событий; hash сравнивается при повторе.
- UNIQUE(actor_id, subject_id, route_scope, idempotency_key). Identity scope включает org; payload не содержит токены в hash/log.
- Operation ID и settlement key уникальны. Одно исполнение не может повторно списаться из-за retry.
- Смена состояния, durable event и изменение бюджета выполняются одной короткой транзакцией, если относятся к одному шагу.
- Аренда и fencing token меняются атомарно; результат старого владельца не становится новым агентским шагом. Полученный late result всё равно может уточнить статус исходной operation.
- Баланс и резерв обновляются атомарно с ledger. CHECK запрещает отрицательный свободный баланс при штатном резервировании; административный долг только отдельной явной политикой, выключенной в v1.

Нельзя держать транзакцию открытой во время сети или inference. Доставку PG jobs выполняет River core. Вставка job и переход run выполняются в одной короткой транзакции через riverpgxv5. Compare-and-set и fencing состояния run принадлежат приложению: повторная доставка job не должна запускать run конкурирующим worker или повторять внешний write. Механизмы wakeup/recovery очереди используем из River, отдельную самописную очередь не вводим.

## История и контекст

Последовательность чата объединяет сообщения, UI-события и отображаемые факты о run. Большие tool results и raw provider details хранятся отдельно и добавляются в контекст по необходимости. Оригиналы сохраняются согласно retention; выжимка не заменяет их.

Начальный контекст: системная политика, разрешённые инструменты, валидная summary до N, полные необходимые события после N, текущее поручение. Граница N выбирается по токен-бюджету конкретной модели. Резервируется место для output, инструментов и управляющих входов. События вопрос/ответ и tool call/result не разрываются в недопустимую для provider структуру.

Сохраняются цель, актуальные ограничения, принятые решения, открытые вопросы и references на исходные IDs. Approvals, права и бюджет берутся из структурированных записей, никогда из summary. Summary не запускает действий и не расширяет пользовательское поручение.

Summary обновляется при превышении бюджета свежей части, а не каждый run. Она включает through_seq и ссылки на исходные данные; правка/удаление источника инвалидирует её. Если summarization недоступна и прежней валидной выжимки недостаточно, run приостанавливается с `context_unavailable`, а важные ограничения не обрезаются молча. Огромный одиночный input отклоняется либо оформляется как артефакт до принятия run.

Активный run применяет новые события между шагами. Snapshot сборки контекста имеет верхнюю seq; более поздние события остаются для следующей границы. UI-событие показывает прошлое: перед изменением backend состояние читается заново.

История доступна через literal (default), PostgreSQL fulltext с конфигурацией `simple` и RE2 regex над ограниченным авторизованным диапазоном. Bounded snippets и paged read сохраняют доступ к полным событиям; ответ явно указывает truncation, skipped sequences и продолжение. Опциональный vector API принимает внешние embeddings и выполняет exact cosine ranking через pgvector. `search.semantic_enabled: false` скрывает инструмент и отключает indexing route. Embeddings-сервис и автоматическая индексация не реализованы.

## Кредиты

Единица — integer microcredit, без floating point. Тарифы версионируются; provider currency usage и внутреннее списание сохраняются отдельно. Для model шага тариф включает input, output, cached input и другие тарифицируемые provider категории. Effort сам по себе не заменяет учёт фактических токенов.

Перед шагом резервируется верхняя оценка стоимости в рамках max output и provider limits. Если provider не позволяет ограничить оплачиваемый объём достаточно для hard budget, такой режим не включается для жёстко ограниченных аккаунтов до отдельной политики.

После известного usage происходит settlement: reserved освобождается, фактическая стоимость списывается ровно один раз. Неудачная или отменённая генерация тоже может быть платной. При неизвестном usage резерв остаётся uncertain, выполняется provider lookup при наличии. Отсутствие usage не означает нулевую стоимость. Manual adjustment требует права, причины и доказательства; история ledger не переписывается.

При ожидании пользователя освобождаются неиспользованные резервы будущих шагов. Резерв неизвестной оплаченной операции сохраняется. Resume требует новый доступный бюджет. Стоимость summary учитывается как отдельный chat-maintenance шаг текущего плательщика, согласованный в тарифной политике; скрытых бесплатных inference нет.

## Retention и удаление

Для истории и idempotency metadata задаётся duration либо `forever`. Deployment-пример: история 365 дней. Файлы временные: deployment `artifacts.ttl_seconds`, default 86400 (24 часа), finite ceiling 7 дней. Legacy `artifact_retention` сохраняется в settings для совместимости, но не управляет файлами. Их `expires_at` не продлевается активным run; сборщик удаляет байты после истечения. Отдельного persisted trace stream нет; duration для trace отклоняется, поддерживается forever/unspecified. Recovery и финансовые records сохраняются; operational Docker/Loki logs имеют отдельную deployment retention policy. `forever` всё равно подчиняется storage quota и явному удалению.

Idempotency записи живут минимум максимального окна повторов/восстановления; после него сохраняются tombstones для запрета повторного исполнения использованных ключей. Принятые операции не исчезают раньше завершения reconciliation. Срок хранения backend deduplication проверяется отдельно от срока чата.

Удаление чата сначала блокирует новые run и доступ, затем отменяет существующий, ставит очистку событий, summary, индексов и blobs. Financial ledger сохраняет минимальные ссылки без содержимого сообщений. Неопределённые внешние операции не уничтожаются до сверки. Удаление default-чата позволяет атомарно создать новый при следующем запросе.

После восстановления старого backup требуется отдельная сверка удалений, отзывов и внешних эффектов до исполнения. Автоматический post-PITR barrier/replay tombstones не реализован; процедура и ограничения описаны в [restore recovery](recovery-mode.md). Пользовательское удаление не обещает мгновенного удаления из исторических backups; срок их истечения документируется.

## Реализованные дополнительные слои

`previous_run_id` — необязательный cache hint для terminal run того же чата, владельца и организации. Только совместимый completed run позволяет повторно использовать сохранённый history prefix; model/effort/settings, actor, artifacts, retention и source count проверяются. Текущие права и micro-memory не берутся из этого кеша. TTL prefix — 15 минут; экономия provider cache не гарантируется. Исполнительные tool checkpoints предыдущего run не копируются.

[Active-run compaction](active-compaction.md) сохраняет новую input window отдельно от истории и durable эффектов. Нативный Responses compact и fallback summary учитываются как отдельные платные шаги.

[Личная память](user-memory.md) имеет scope issuer/subject/org, micro до 500 Unicode code points и три уровня on-demand данных. Она доступна без отдельных ролей, не удаляется вместе с чатом и очищается собственным API.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
