# Runs input

[Guide index](../api.md)

## Создание run

`POST /runs`, обязательный `Idempotency-Key`:

```json
{
  "chat_id": null,
  "previous_run_id": null,
  "auth_session_id": "opaque-id",
  "input": {"text": "Помоги выбрать массаж", "artifact_ids": []},
  "interaction": {"reply_mode": "agent", "questions": "choices_and_text", "approvals": true},
  "disconnect_policy": "continue",
  "delivery": {"mode": "async", "wait_ms": 0},
  "budget": {"max_microcredits": 1000000},
  "billing_account_id": null
}
```

`previous_run_id` — необязательная ссылка на terminal run того же чата/владельца/org, а не команда возобновить его. Active previous run даёт `409 previous_run_active`. Совместимый completed run может дать сохранённый history prefix с TTL до 15 минут, не дольше срока файла; при несовместимости строится обычный контекст. Prefix не копирует tool effects и не гарантирует provider cache discount.

В v1 модель выбирается по пользовательским настройкам. Произвольного обхода через поле model в запросе нет. Бюджет запроса может только уменьшать разрешённый лимит. Плательщик проверяется отдельно; default — пользовательский аккаунт.

Ответ `202`: `run_id`, разрешённый `chat_id`, статус, URL событий. Ответ выдаётся только после commit run и исходного сообщения. Повтор с тем же ключом и идентичным семантическим body возвращает исходный ресурс; другой body — `409 idempotency_conflict`. Уже сохранённый повтор проверяется до `chat_busy`.

`delivery.mode=wait` ждёт ограниченное время и возвращает `200`, если получен итог, либо `202` с тем же ID. Это способ доставки одного durable run, а не отдельное исполнение. Ответ не удерживается до бесконечности. Поддерживаются `questions=none|text|choices|choices_and_text`.

В режиме `agent` вопросы и ответы идут через обычный чат; `questions` описывает представление вариантов, а `approvals` — наличие отдельных бинарных callbacks. В режиме `protocol` вопрос при `questions=none` сохраняется как `waiting_input / client_interaction_disabled`. Обычное подтверждение без поддержки callbacks остаётся `waiting_approval / client_interaction_disabled`. Метод с обязательным `confirmation.reply_mode=protocol` использует точную текстовую фразу, если callbacks недоступны. Отсутствие возможности ответа никогда не означает согласие.
## Входы в run

`interaction.reply_mode=agent` включает один поток steering для обычного текста
и подсказок-кнопок, с возможностью отдельных бинарных callbacks для критических
действий. `interaction.approvals` сообщает о поддержке этих callbacks.
[Conversational input](../conversational-input.md) описывает схему, переходы,
fallback без кнопок, границы подтверждения и восстановления.

`POST /runs/{id}/input`, обязательный `Idempotency-Key`:

```json
{"kind":"steering","text":"Выбирай только вечернее время"}
```

В режиме `agent` это общий вход для сообщений, ответов на вопросы и обычных подсказок-кнопок. Отдельного типа «ответ на действие» нет. Steering будит тот же run из `waiting_input` или `waiting_approval`; во время исполнения применяется между шагами. Закрытие формы не отменяет обычное ожидающее действие. Для строгого текстового подтверждения критического действия любое другое непустое сообщение означает отказ.

В режиме `protocol` ответ на вопрос передают как `kind=answer` с `question_id`, `choice_ids` и/или `text`. Проверяются схема вопроса, срок и принадлежность; в `agent` этот вид входа отклоняется с `409 use_steering`. Варианты имеют стабильные ID; надпись кнопки не является ID.

Отдельные бинарные callbacks используют `kind=approval`, `approval_id` и `decision=approve|reject`. Они доступны при `interaction.approvals=true`, в том числе внутри разговорного run, если конкретный approval имеет `reply_mode=protocol`. Обычный `agent` approval нельзя подтвердить таким callback: нужен steering. Callback относится к действию по ID и может поступить, пока отображается последующий вопрос.

К `steering` можно добавить `presentation` с `reply_to`, `question_id`, `approval_id`, если клиент действительно знает, на какое сообщение или форму ответил пользователь. Эти поля не добавляются в текст и не обязательны для обычного сообщения. Снимок текущего действия на сервере не доказывает его доставку пользователю.

`POST /input` возвращает `run_id`, `status`, `reason` при наличии и `version`. Для клиента с `chat.read` также возвращается `input_id`; у разговорного steering — `context` с назначенными сервером `revision`, `source_actor` и известными `question_id`/`approval_id`. Повтор по тому же idempotency key возвращает ID исходного принятого входа, даже если уже были другие сообщения. Legacy-квитанции могут не иметь `input_id`. Это квитанция приёма, а не результат эффекта или факт согласия.

Например, квитанция уточняющего вопроса пользователя может выглядеть так:

```json
{
  "run_id": "RUN_ID",
  "status": "queued",
  "version": 12,
  "input_id": "INPUT_ID",
  "context": {
    "revision": 3,
    "source_actor": "CLIENT_ACTOR",
    "approval_id": "APPROVAL_ID"
  }
}
```

`input.applied` содержит `input_id` и означает включение входа в durable обработку. `interaction.resolved` у agent resolver и строгого текстового fallback сообщает сохранённое решение и связь с input/approval; это не универсальный ack для native callbacks. Исполнение операции происходит отдельно и может потребовать авторизации или сверки. Для итогового состояния, текущих `question` и `approval` читайте `GET /runs/{id}` с `chat.read`. Они могут присутствовать одновременно: `waiting_input` после уточнения не означает отмены действия.

Steering не откатывает уже выполненный эффект. `/pause` и `/cancel` — отдельные операционные команды; они не требуют интерпретации текста моделью. `/pause`, `/resume` и `/cancel` возвращают только `run_id`, `status`, `reason`, `version`.
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
