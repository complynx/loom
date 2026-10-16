# Schema invariants

[Guide index](../data.md)

## PostgreSQL entities

Схема задаётся [SQL-миграциями](../../migrations), HTTP DTO — генерируемым `/openapi.json`. JSONB хранит типизированные domain documents и checkpoints; не каждое поле соответствует отдельной таблице.

| Таблицы | Назначение |
| --- | --- |
| users, memberships, user_preferences, user_policies | Identity, организация, выбор модели и доступные лимиты. |
| chats, chat_events, passive_event_receipts | Чаты, полная история и дедупликация пассивных действий. |
| runs, run_events, run_inputs, steps | Состояние исполнения, события, входящие сообщения и checkpoints. Вопросы/approvals сохраняются в domain documents. |
| auth_sessions, service_identities, identity_cache_state | Зашифрованная авторизация, учёт сервисных identities и поколение permission cache. Автоматического OAuth refresh нет. |
| write_operations | Prepare/approval/dispatch/status и устойчивое состояние внешних эффектов. |
| billing_accounts, credit_ledger, platform_provider_attempts, provider_retries | Balance/reserve/settlement, exposure платформы и расписание retry. |
| context_summaries, run_contexts, event_embeddings | Summary, prefix контекста и optional vector search. |
| artifacts, chat_artifacts, artifact_handles, artifact_file_refs | Временные файлы, связи, handles и stable external references. |
| user_memory, user_memory_entries | Micro-память и иерархические личные записи. |
| settings, idempotency_records, audit_events | Версии настроек, квитанции команд и аудит. |

River, библиотека очереди заданий PostgreSQL, имеет собственные таблицы и migration history. Discovery registry хранится в памяти процесса; таблицы `service_registry` нет. Поля и ограничения таблиц определяются SQL-миграциями.

Сервисные субъекты и deployment bindings — отдельные identities, не обычные пользовательские аккаунты. DB JSONB используется для контрактных payload/schema, а не вместо всех связей и ограничений.
## Persisted conversation context

Разговорный механизм использует существующие таблицы и JSONB, без отдельной таблицы для каждой формы:

- `runs.doc`: `interaction.reply_mode`, `conversation_revision`, последний `conversation_input`, текущие `question` и `approval`. Approval сохраняет подготовленный call, способ ответа, параметры/hash, срок, решение и ссылки `decision_input_id`/`resolution_call_id`.
- `run_inputs`: точный вход и его применение. Для разговорного steering `doc.context` содержит серверный `revision`, `source_actor` и известные при приёме IDs вопроса/действия; `doc.presentation` отдельно хранит необязательные сведения клиента. Клиент не назначает `id`, `context` или `applied`.
- `idempotency_records`: hash исходного запроса и ID принятого input. Повтор возвращает исходный input, даже если run уже получил новые сообщения. Legacy-записи могут ссылаться только на run и не дают input receipt.
- `write_operations.doc`: подготовка, binding и `consent_revision`; перед dispatch они проверяются под run lock. Отдельного model-text parser или второго классификатора нет.
- `steps`: сохранённые model/input/tool checkpoints. Внутренний шаг `interaction` сохраняет результат исполнения прямого бинарного/строгого ответа, не создавая фиктивный вызов модели.
- `chat_events` и `run_events`: принятые входы, вопросы/предложения и связи решений. `interaction.resolved` записывается agent resolver и exact-text fallback и не обещает бизнес-успех; native input сохраняется в chat history.

`conversation_revision` задаёт порядок входов внутри run; это не chat history seq и не SSE cursor. Последний input, текущие question и approval передаются модели отдельно от сжатой истории. Summary не может заменить эти записи или перенести согласие на новую операцию. Схемы и границы проверок — в [контракте диалога](../conversational-input.md).
## Required invariants

- Partial UNIQUE `chats(owner_id) WHERE is_default`: один default-чат на пользователя, не по одному на организацию. Удаление снимает `is_default`. Его org фиксируется при создании; обращение из другой org требует явного чата.
- Partial UNIQUE на runs(chat_id), если состояние нетерминальное. Даже paused/waiting run удерживает слот.
- Primary keys `(chat_id, seq)` и `(run_id, seq)` для событий, UNIQUE `(run_id, ordinal)` для steps.
- UNIQUE `(source_actor, source_event_id)` для внешних UI-событий с непустым ID. `passive_event_receipts` сохраняет этот ключ после удаления истории; request hash сравнивается при повторе.
- Primary key `idempotency_records(scope, key)`. Приложение строит `scope` из issuer, subject, actor, org и route; это не отдельные колонки. В hash входят поля команды, а не credentials.
- Operation ID и settlement key уникальны. Одно исполнение не может повторно списаться из-за retry.
- Смена состояния, durable event и изменение бюджета выполняются одной короткой транзакцией, если относятся к одному шагу.
- Аренда и fencing token меняются атомарно; результат старого владельца не становится новым агентским шагом. Полученный late result всё равно может уточнить статус исходной operation.
- Баланс и резерв обновляются атомарно с ledger. CHECK требует `balance >= 0`, `spent >= 0`, `reserved >= 0` и `balance >= reserved`. Отрицательный баланс и административный долг не поддерживаются.

Нельзя держать транзакцию открытой во время сети или inference. Доставку PG jobs выполняет River core. Вставка job и переход run выполняются в одной короткой транзакции через riverpgxv5. Compare-and-set и fencing состояния run принадлежат приложению: повторная доставка job не должна запускать run конкурирующим worker или повторять внешний write. Механизмы wakeup/recovery очереди используем из River, отдельную самописную очередь не вводим.
