# HTTP API v1

Базовый путь `/v1`. JSON UTF-8; время RFC 3339 UTC; ID непрозрачные UUID; денежные значения в целых microcredits. Неизвестные поля входных объектов отклоняются. Версия протокола несовместимо меняется новым базовым путём.

## Аутентификация

- Прямой запрос: `Authorization: Bearer <user-access-token>`.
- Сервисный запрос: `Authorization: Bearer <service-access-token>` и, при bootstrap делегации, `X-Subject-Token: <user-access-token>`.
- После bootstrap сервис указывает `X-Auth-Session-ID` (либо `auth_session_id` в run-create body); сессия привязана к пользователю, actor, организации и разрешённым аудиториям. ID не является bearer credential.
- Один запрос не может одновременно использовать `X-Subject-Token` и `auth_session_id`.
- MVP принимает проверенные access tokens. Входящие непрозрачные сервисные секреты и автоматический token exchange/refresh не реализованы; deployment binding остаётся требованием для будущего секретного режима.

Все запросы выполняются по TLS на внешней границе. По умолчанию credentials не логируются; явный debug payload mode может сохранять claims и JWT с замаскированной подписью. См. [logging policy](observability.md). Запросы без refreshable сессии допустимы, но могут перейти в `waiting_auth`.

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
| POST | `/chats/{id}/history/search` | Literal/fulltext/regex и опциональный vector-поиск |
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

`/healthz` проверяет процесс; `/readyz` — способность безопасно принимать durable запросы. Metrics доступны только через отдельный private listener. Полный актуальный wire contract отдаёт `/openapi.json`; таблица выше — основные routes. [Личная память](user-memory.md) и [diagnostics/panel](diagnostics.md) описывают дополнительные endpoints.

| Метод | Путь относительно `/v1` | Назначение |
| --- | --- | --- |
| GET | `/users/me/credits` | Баланс и резерв |
| POST | `/chats/{id}/history/read` | Bounded страницы событий или текста одного события |
| POST | `/chats/{id}/history/embeddings` | Внешний вектор для события; route только при enabled, нужны `chat.embeddings.write` и `chat.read` |
| POST | `/runs/{id}/artifacts/handles` | Временный handle из `{artifact_id}` |
| GET | `/artifact-handles/{id}/content` | Авторизованный stream по handle, ID сам не является credential; TTL 15 минут |

## Создание run

`POST /runs`, обязательный `Idempotency-Key`:

```json
{
  "chat_id": null,
  "previous_run_id": null,
  "auth_session_id": "opaque-id",
  "input": {"text": "Помоги выбрать массаж", "artifact_ids": []},
  "interaction": {"questions": "choices_and_text", "approvals": true},
  "disconnect_policy": "continue",
  "delivery": {"mode": "async", "wait_ms": 0},
  "budget": {"max_microcredits": 1000000},
  "billing_account_id": null
}
```

`previous_run_id` — необязательная ссылка на terminal run того же чата/владельца/org, а не команда возобновить его. Active previous run даёт `409 previous_run_active`. Совместимый completed run может дать сохранённый history prefix с TTL 15 минут; при несовместимости строится обычный контекст. Prefix не копирует tool effects и не гарантирует provider cache discount.

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

Steering сохраняется сразу и применяется между шагами. Оно не отменяет уже выполненное действие. Для прерывания используется pause/cancel. Событие `input.applied` содержит `input_id`; граница исполнения определяется порядком durable шагов, отдельный номер шага в этом payload не заявляется.

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

Поиск: `mode=literal|fulltext|regex|semantic`; пустой mode означает literal. Для текстового поиска нужен непустой `query`. Фильтры: `before_seq`, `after_seq`, `source_service_id`, `event_types`, `limit`; `case_sensitive` управляет literal/regex. Fulltext использует PostgreSQL `simple`. Regex использует Go RE2 без lookaround/backreferences; неверный шаблон — `422 invalid_pattern`.

Deployment-пример: query до 256 Unicode characters, до 50 результатов, scan до 1 MiB и deadline 500 ms. `max_bytes` ограничивает выдачу; по умолчанию 64 KiB, `snippet_chars` — 500. Ответ содержит `events`, `partial`, `reason`, `next_cursor`, `truncated_seqs` и `skipped_seqs`. Продолжайте event pagination через `after_seq=next_cursor`. Слишком большое для scan событие явно отмечается в `skipped_seqs`, а cursor продвигается; это не доказательство отсутствия совпадения. Для полного текста используйте read. Partial result не равен полному отсутствию данных.

`POST /chats/{id}/history/read` принимает `{after_seq,before_seq,limit,max_bytes,snippet_chars}` для event pages. `{event_seq,offset_chars,max_bytes,snippet_chars}` читает текст одного события; `text_partial` и `next_text_offset` задают продолжение Unicode-текста. Оригинальные события сохраняются.

При `search.semantic_enabled: true` тот же search endpoint принимает `mode: "semantic"`, `embedding_model`, `embedding`, `offset`, `limit` и фильтры. Следуйте `next_offset`; векторы должны иметь ту же модель и размерность, что индекс. Indexing endpoint принимает `{seq,model,vector}`. pgvector требуется в database harness. Это exact cosine ranking с авторизационными фильтрами, без embeddings provider и ANN index. Disabled режим скрывает semantic tool и indexing route, запрос semantic возвращает отказ.

## Сессии и версии

`POST /auth/sessions` принимает JSON `{}` и возвращает 201 с session receipt. `POST /auth/sessions/{id}/reauthorize` принимает `{version}` и свежую авторизацию того же subject/actor/org; receipt 200. Это explicit reauthorization, а не OAuth refresh. Изменение settings, policy или preferences передаёт текущую `version` в body; settings принимает полный типизированный объект, а не JSON Merge Patch. `If-Match` не заменяет body version.

Billing adjustment принимает `{amount_microcredits,reason}` и `Idempotency-Key`; произвольная запись баланса через API не предусмотрена.

## SSE

Durable события имеют монотонный `seq` внутри run; `id` SSE равен этому номеру. Клиент использует `Last-Event-ID` или `after`, но не оба. Основные типы: `run.status`, `input.applied`, `question.created`, `approval.created`, `artifact.ready`, `message.completed`, `usage.updated`, `run.resumable`; provider recovery/retry добавляет свои durable события. Клиент должен сохранять cursor и терпимо обрабатывать неизвестный тип.

MVP доставляет сохранённые run events, а не поток token deltas провайдера. SSE читает bounded database batches. Запись в медленное соединение ограничена deadline; клиент после обрыва дочитывает durable события. Доставка как минимум один раз; дедупликация по seq.

Отсутствие подписчиков не отменяет run при `continue`. Политика `pause` действует после разрыва последнего stream и disconnect grace. Reconnect согласован с финальной проверкой pause; одноразовый HTTP async запрос не считается stream-разрывом.

## Ошибки

```json
{"error":{"code":"dependency_unavailable","message":"Authorization service is unavailable","retryable":true,"request_id":"opaque-id"}}
```

`401` — нет действительных credentials; `403` — нет права; `404` — ресурс не существует или недоступен; `409` — занятый чат/конфликт состояния или версии; `422` — неверный контракт; `429` — квота с `Retry-After`; `503` — зависимость не позволяет принять запрос; `504` — ограниченное ожидание dependency. Ответы не включают токены, upstream bodies и stack traces.

Ошибка HTTP после commit может скрыть успешную операцию. Клиент повторяет только с исходным idempotency key, а не создаёт новое поручение.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
