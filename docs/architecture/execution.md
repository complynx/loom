# Execution

[Guide index](../architecture.md)

## Run states

| Состояние | Значение | Допустимый выход |
|---|---|---|
| `queued` | Принят и сохранён, ждёт worker | `running`, `paused`, `cancelled`, `failed` |
| `running` | Выполняет агентские шаги | Все состояния ожидания, `paused`, терминальные |
| `waiting_input` | Ожидает ответ на вопрос | `queued`, `paused`, `cancelled`, `failed` |
| `waiting_approval` | Ожидает согласие на действие | `queued`, `paused`, `cancelled`, `failed` |
| `waiting_auth` | Нужна новая авторизация | `queued`, `paused`, `cancelled`, `failed` |
| `reconciling` | Есть операция с неизвестным результатом | `queued`, `paused`, `cancelled`, `failed` |
| `paused` | Исполнение остановлено и допускает продолжение | `queued`, `cancelled`, `failed` |
| `completed` | Поручение завершено | Нет |
| `cancelled` | Поручение отменено | Нет |
| `failed` | Продолжение этого run невозможно | Нет |

`pause_requested` и `cancel_requested` — durable флаги, а не мгновенное обещание остановки. Причина состояния хранится отдельно: `shutdown`, `budget_exhausted`, `dependency_unavailable`, `wait_expired` и другие коды.

Истечение срока вопроса или паузы завершает run с `failed / wait_expired` согласно политике. Новое поручение после этого может использовать прежний чат. Операции `unknown` продолжают учитываться и сверяться даже после отмены или завершения run.
## Conversation and pending action

`question` и `approval` — независимые части сохранённого run. Пока действие заблокировано, агент может задать уточняющий вопрос; тогда run находится в `waiting_input` и всё ещё содержит pending approval. Закрытие клиентской формы не меняет состояние операции.

В режиме `agent` любой непустой steering сохраняется с контекстом и будит этот run из обоих состояний ожидания. Модель читает сообщение и точное ожидающее предложение. Уточнение не даёт согласия; `interaction_resolve` фиксирует интерпретированное решение с input ID и binding. Новые параметры требуют нового prepare и нового сообщения с согласием. Более свежий принятый вход блокирует dispatch по прежнему согласию.

Для критического метода `confirmation.reply_mode=protocol` runtime принимает отдельный бинарный callback, если клиент его поддерживает. Иначе он проверяет точную локализованную фразу модели; любой другой непустой текст отклоняет действие. Эти решения не требуют классификации моделью и проходят обычную очередь, fresh authorization и operation recovery. Агент может затем объяснить фактический результат.

Модель не владеет окончательным разрешением dispatch. Решение сохраняется атомарно с operation approval; отправка эффекта имеет отдельную транзакционную границу. Подробные схемы, таблица выбора режима, ограничения доставки и сценарии — в [контракте диалога](../conversational-input.md).
## Execution step

1. Получить аренду run и актуальный fencing token.
2. Проверить блокировки, авторизацию и ожидающие управляющие входы.
3. Собрать разрешённый контекст и инструменты; зафиксировать использованный диапазон событий.
4. Зарезервировать бюджет оплачиваемого шага.
5. Сохранить намерение шага, затем выполнить модель или инструмент.
6. Сохранить результат и расходы, добавить события в той же транзакции.
7. Применить входы, появившиеся во время шага; выбрать следующий шаг или ожидание.

Скрипт может выполнять независимые RPC параллельно в пределах `parallel_rpc_limit`. Внешние JS-методы доступны только для чтения; внешние write/delete используют структурированный `module_call`. Runtime не предоставляет общий lock на предметный ресурс: backend должен проверять preconditions и дедупликацию. `Promise.all` не отменяет уже выполненный RPC при ошибке соседнего promise; для встроенной личной памяти действуют version checks.
## Clocks and deadlines

### Orphan delivery recovery

Outside recovery mode, queue maintenance runs at startup and every ten seconds.
It selects at most 100 `running` runs with an expired or missing lease, no
pause/cancel request and no queued delivery. In one transaction it fences the
old executor, changes the run to `queued / orphaned_delivery` and inserts a new
River job for the same run. A failed insert rolls back the entire checkpoint.
Concurrent sweepers lock and skip selected rows. Existing active leases remain
authoritative; the old River job is retained for audit.

Each new delivery records the run fence at insertion. A stale `running` River
row does not suppress recovery or hide an exhausted replacement. Available,
scheduled and retryable deliveries still prevent duplicate insertion. Queue
exhaustion is checked first and retains its existing pause/reconciliation policy.
Older jobs without a delivery fence can be recovered after lease expiry.

The engine rechecks caller and user authorization before sponsored inference
recovery and before execution. This sweep does not settle reservations, repeat
unknown writes or reset the sponsored attempt limit. Pending non-model work
remains subject to reconciliation. Unknown provider exposure keeps its durable
platform accounting; run, chat and authorization-session IDs do not change.

With a healthy database, available workers and no batch backlog, replacement
delivery is scheduled by the next ten-second sweep after lease expiry, plus
transaction and queue latency. This is a scheduling bound, not a final-response
SLA. Outages and batches over 100 can delay recovery. River's separate stuck-job
rescue timeout is not the bound for this path.

`TestOrphanQueueSurvivesSIGKILL` kills a Linux worker during inference and starts
a replacement without repair SQL. PostgreSQL, River and the process boundary
are real; identity and provider HTTP peers are fixtures. Deployment acceptance
with the actual IAM, caller and provider remains a separate check.

### Checkpoint deadlines

Lease-releasing checkpoints, including questions and approval requests, have a
separate one-second persistence deadline. Work cancellation does not interrupt
that checkpoint: the control watcher can observe the committed lease release
before the database acknowledges COMMIT. Store checks still enforce the owner,
fence, lease expiry and durable pause/cancel requests. Model and tool execution
continue to use the cancellable work context.

- Общая длительность run, включая ожидания, ограничивает срок поручения.
- Активное время ограничивает вычисления, не включает ожидание пользователя.
- Deadline скрипта включает ожидание всех его RPC.
- Метод имеет собственный deadline, не превышающий оставшееся время скрипта.
- Shutdown имеет отдельные `execution.shutdown_deadline_ms` и `shutdown_target_ms`. Предельный срок допускает не более 5 секунд; примеры используют цель 1 секунда. Цель — измеряемый operational target, а не гарантия завершения.

Durable lifecycle checks используют время PostgreSQL; runtime дополнительно применяет process deadlines и проверяет expiry токенов и аренд. Временные метки передаются в UTC. `occurred_at` от сервиса не определяет авторизацию, порядок исполнения или сроки подтверждения.
