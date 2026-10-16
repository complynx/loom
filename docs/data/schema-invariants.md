# Schema invariants

[Guide index](../data.md)

## Сущности PostgreSQL

Схема задаётся [SQL-миграциями](../../migrations), HTTP DTO — /openapi.json. JSONB хранит типизированные domain documents и checkpoints; не считайте каждое поле отдельной таблицей.

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

River имеет собственные таблицы и migration history. Registry discovery не хранится как вымышленная service_registry таблица. Число/поля таблиц могут расширяться миграциями; при изменении кода сверяйтесь с SQL.

Сервисные субъекты и deployment bindings — отдельные identities, не обычные пользовательские аккаунты. DB JSONB используется для контрактных payload/schema, а не вместо всех связей и ограничений.
## Сохранённый контекст диалога

Разговорный механизм использует существующие таблицы и JSONB, без отдельной таблицы для каждой формы:

- `runs.doc`: `interaction.reply_mode`, `conversation_revision`, последний `conversation_input`, текущие `question` и `approval`. Approval сохраняет подготовленный call, способ ответа, параметры/hash, срок, решение и ссылки `decision_input_id`/`resolution_call_id`.
- `run_inputs`: точный вход и его применение. Для разговорного steering `doc.context` содержит серверный `revision`, `source_actor` и известные при приёме IDs вопроса/действия; `doc.presentation` отдельно хранит необязательные сведения клиента. Клиент не назначает `id`, `context` или `applied`.
- `idempotency_records`: hash исходного запроса и ID принятого input. Повтор возвращает исходный input, даже если run уже получил новые сообщения. Legacy-записи могут ссылаться только на run и не дают input receipt.
- `write_operations.doc`: подготовка, binding и `consent_revision`; перед dispatch они проверяются под run lock. Отдельного model-text parser или второго классификатора нет.
- `steps`: сохранённые model/input/tool checkpoints. Внутренний шаг `interaction` сохраняет результат исполнения прямого бинарного/строгого ответа, не создавая фиктивный вызов модели.
- `chat_events` и `run_events`: принятые входы, вопросы/предложения и связи решений. `interaction.resolved` записывается agent resolver и exact-text fallback и не обещает бизнес-успех; native input сохраняется в chat history.

`conversation_revision` задаёт порядок входов внутри run; это не chat history seq и не SSE cursor. Последний input, текущие question и approval передаются модели отдельно от сжатой истории. Summary не может заменить эти записи или перенести согласие на новую операцию. Схемы и границы проверок — в [контракте диалога](../conversational-input.md).
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
