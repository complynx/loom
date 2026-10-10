# HTTP API v1

Базовый путь `/v1`. JSON UTF-8; время RFC 3339 UTC; ID непрозрачные UUID; денежные значения в целых microcredits. Неизвестные поля входных объектов отклоняются. Версия протокола несовместимо меняется новым базовым путём.

## Аутентификация

- Прямой запрос: `Authorization: Bearer <user-access-token>`.
- Сервисный запрос: `Authorization: Bearer <service-access-token>` и, при bootstrap делегации, `X-Subject-Token: <user-access-token>`.
- После bootstrap сервис указывает `auth_session_id`; сессия привязана к пользователю, actor, организации и разрешённым аудиториям. ID не является bearer credential.
- Один запрос не может одновременно использовать `X-Subject-Token` и `auth_session_id`.
- MVP принимает проверенные access tokens. Входящие непрозрачные сервисные секреты и автоматический token exchange/refresh не реализованы; deployment binding остаётся требованием для будущего секретного режима.

Все запросы выполняются по TLS на внешней границе. Заголовки с токенами исключаются из логов. Запросы без refreshable сессии допустимы, но могут перейти в `waiting_auth`.

## Поверхность API

| Метод | Путь | Назначение |
|---|---|---|
| POST | `/auth/sessions` | Создать ограниченную авторизационную сессию из проверенного subject token |
| POST | `/auth/sessions/{id}/reauthorize` | Заменить делегацию свежей авторизацией того же пользователя и actor |
| DELETE | `/auth/sessions/{id}` | Отозвать локальную авторизационную сессию |
| POST | `/chats` | Создать отдельный чат |
| GET | `/chats/default` | Получить или создать дефолтный чат |
| GET | `/chats/{id}/history` | Страничная история по durable cursor |
| POST | `/chats/{id}/events` | Добавить пассивное пользовательское действие |
| POST | `/chats/{id}/history/search` | Полнотекстовый или regex-поиск |
| DELETE | `/chats/{id}` | Начать контролируемое удаление чата и производных данных |
| POST | `/runs` | Создать поручение; необязательный `chat_id` |
| GET | `/runs/{id}` | Статус, вопросы, подтверждения, итог и незавершённые операции |
| GET | `/runs/{id}/events` | SSE с возможностью дочитывания |
| POST | `/runs/{id}/input` | Steering, ответ на вопрос или подтверждение |
| POST | `/runs/{id}/pause` | Запросить сохраняемую остановку |
| POST | `/runs/{id}/resume` | Продолжить paused run |
| POST | `/runs/{id}/cancel` | Отменить поручение |
| GET | `/models` | Разрешённые пользователю модели и их capabilities |
| GET/PATCH | `/users/me/preferences` | Своя модель и effort |
| GET/PATCH | `/admin/settings` | Типизированные динамические настройки |
| GET/PATCH | `/admin/users/{id}/policy` | Лимиты и доступ пользователя |
| GET/PATCH | `/admin/users/{id}/preferences` | Выбор модели другого пользователя |
| POST | `/admin/billing/{id}/adjustments` | Аудируемое начисление или корректировка, не прямое изменение balance |
| POST | `/admin/runs/{id}/steps/{step}/reconcile` | Аудируемая сверка неизвестной оплаты inference |
| POST | `/artifacts` | Ограниченная загрузка файла |
| GET | `/artifacts/{id}` | Метаданные |
| GET | `/artifacts/{id}/content` | Авторизованное потоковое чтение |

`/healthz` проверяет процесс; `/readyz` — способность безопасно принимать durable запросы. Metrics доступны только во внутренней сети.

## Создание run

`POST /runs`, обязательный `Idempotency-Key`:

```json
{
  "chat_id": null,
  "auth_session_id": "opaque-id",
  "input": {"text": "Помоги выбрать массаж", "artifact_ids": []},
  "interaction": {"questions": "choices_and_text", "approvals": true},
  "disconnect_policy": "continue",
  "delivery": {"mode": "async", "wait_ms": 0},
  "budget": {"max_microcredits": 1000000},
  "billing_account_id": null
}
```

В v1 модель выбирается по пользовательским настройкам. Произвольного обхода через поле model в запросе нет. Бюджет запроса может только уменьшать разрешённый лимит. Плательщик проверяется отдельно; default — пользовательский аккаунт.

Ответ `202`: `run_id`, разрешённый `chat_id`, статус, URL событий. Ответ выдаётся только после commit run и исходного сообщения. Повтор с тем же ключом и идентичным семантическим body возвращает исходный ресурс; другой body — `409 idempotency_conflict`. Уже сохранённый повтор проверяется до `chat_busy`.

`delivery.mode=wait` ждёт ограниченное время и возвращает `200`, если получен итог, либо `202` с тем же ID. Это способ доставки одного durable run, а не отдельное исполнение. Ответ не удерживается до бесконечности. Поддерживаются `questions=none|text|choices|choices_and_text`.

Если вопрос необходим, но клиент указал `none`, состояние — `waiting_input` с причиной `client_interaction_disabled`. Если подтверждения недоступны, действие остаётся `waiting_approval`; запрет не означает согласие.

## Входы в run

`POST /runs/{id}/input`, обязательный `Idempotency-Key`:

```json
{"kind":"steering","text":"Выбирай только вечернее время"}
```

Другие варианты: `kind=answer` с `question_id`, `choice_ids` и/или `text`; `kind=approval` с `approval_id` и `decision=approve|reject`. Проверяются схема вопроса, срок и принадлежность. Первый ответ закрывает вопрос; конфликтующий повтор — `409 input_conflict`. Варианты имеют стабильные ID; текст надписи не является ID.

Steering сохраняется сразу и применяется между шагами. Оно не отменяет уже выполненное действие. Для прерывания используется pause/cancel. Событие `input.applied` содержит ID входа и номер границы шага.

Resume применяется к `paused` и к `reconciling` для status-only сверки внешней операции. Неопределённая оплата inference блокирует resume до settlement. Pause не выводит неопределённый run из reconciliation. Остальные состояния ожидания разрешаются соответствующим вводом или авторизацией. Все управляющие команды идемпотентны и принимают `Idempotency-Key`.

Управляющие операции `/input`, `/pause`, `/resume` и `/cancel` возвращают только `run_id`, `status`, `reason`, `version`. История, итог, вопросы и approvals читаются отдельно с `chat.read`.

## Пассивные события

`POST /chats/{id}/events` требует `chat.events.append`. Вместо ID можно использовать `/chats/default/events` с той же семантикой default resolution.

```json
{
  "source_event_id":"ui-event-123",
  "type":"user.action",
  "action":"massage.specialist.selected",
  "text":"Выбрал массажиста Андрея",
  "data":{"specialist_id":"andrey-id","flow_id":"booking-123","step":"specialist_selected"},
  "occurred_at":"2026-10-10T18:00:00Z"
}
```

Actor и subject определяются авторизацией. Уникальность `(actor_id, source_event_id)` обеспечивает deduplication; повтор с другим содержимым конфликтует. `201` означает durable запись; run не создаётся. `occurred_at` хранится как сообщённое время, `received_at` и последовательный номер задаёт harness. Позднее событие не вставляется задним числом в уже прочитанный диапазон.

## История и поиск

Поиск: `mode=fulltext|regex`, `query`, необязательные `before_seq`, `after_seq`, `source_service_id`, `event_types`, `limit`. Все ограничения доступа применяются до выдачи фрагментов. Regex использует RE2-совместимый синтаксис без lookaround/backreferences; ошибки шаблона — `422 invalid_pattern`.

Предлагаемые defaults: 256 символов шаблона, до 50 результатов, 1 MiB просматриваемого текста, 500 ms на запрос. При исчерпании сканирования ответ содержит `partial=true` и `next_cursor`. Если отдельное событие больше всего допустимого scan budget, MVP возвращает `422 regex_event_too_large`; оператор может увеличить разрешённый лимит или клиент сузить диапазон cursor. Продолжение не застревает с прежним cursor и не пропускает запись молча. Ограничивается также считывание строк из БД; regex не запускается над всей таблицей PostgreSQL без предела. В v1 ищем по отображаемому тексту и разрешённым строковым полям события, а не по секретам или сырым binary данным.

## SSE

Durable события имеют монотонный `seq` внутри run; `id` SSE равен этому номеру. Клиент использует `Last-Event-ID` или `after`, но не оба. Типы: `run.status`, `input.applied`, `question.created`, `approval.created`, `tool.status`, `artifact.created`, `message.completed`, `usage.updated`, `run.resumable`.

Текстовые дельты модели могут быть transient и помечаются как preview с `generation_id`; они не получают durable SSE ID. После обрыва итоговое сообщение восстанавливает результат; незавершённый preview сбрасывается событием статуса. Клиент не считает preview подтверждением выполненной операции.

Durable события доставляются как минимум один раз; клиент дедуплицирует по seq. Медленный клиент отключается после заполнения ограниченного буфера и дочитывает события. Отсутствие подписчиков не отменяет run при `continue`. Политика `pause` действует после разрыва последнего stream и disconnect grace; одноразовый HTTP async запрос не считается stream-разрывом.

## Ошибки

```json
{"error":{"code":"dependency_unavailable","message":"Authorization service is unavailable","retryable":true,"request_id":"opaque-id"}}
```

`401` — нет действительных credentials; `403` — нет права; `404` — ресурс не существует или недоступен; `409` — занятый чат/конфликт состояния или версии; `422` — неверный контракт; `429` — квота с `Retry-After`; `503` — зависимость не позволяет принять запрос; `504` — ограниченное ожидание dependency. Ответы не включают токены, upstream bodies и stack traces.

Ошибка HTTP после commit может скрыть успешную операцию. Клиент повторяет только с исходным idempotency key, а не создаёт новое поручение.
