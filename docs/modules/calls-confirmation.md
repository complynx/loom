# Calls confirmation

[Guide index](../modules.md)

## RPC

Все method endpoints — POST по пути относительно зарегистрированного origin, принимают envelope:

```json
{
  "protocol_version":1,
  "operation_id":"opaque-id",
  "run_id":"opaque-id",
  "arguments":{},
  "idempotency_key":"stable-operation-key",
  "confirmation":null
}
```

Credentials передаются broker в заголовках. Subject из body не заменяет credentials. `operation_id` устойчив при повторе и создан до отправки. Для изменяющих методов idempotency и status lookup по этому ID обязательны. Backend должен хранить deduplication и lookup не меньше срока допустимого повтора; по умолчанию окно автоматического восстановления — 24 часа, затем только lookup/ручная сверка. Истечение окна не разрешает новый blind retry.

Ответ: `status=succeeded|accepted|rejected`, operation_id, result/artifacts либо структурированный error. `accepted` означает durable владение операцией сервисом и требует status endpoint. Ошибки классифицируются как `not_started`, `terminal_rejection`, `unknown`; HTTP status сам по себе не доказывает отсутствие изменения. Только явно гарантированный `not_started` позволяет безопасный повтор без сверки.

Status endpoint принимает POST `{operation_id}` и возвращает `not_found|pending|succeeded|failed|cancelled`. `not_found` должен иметь документированную гарантию: прежний запрос уже не сможет позже выполнить действие. Без неё harness трактует ответ как unknown. Cancellation endpoint принимает operation_id и возвращает известный статус; отмена не обещает rollback.
## Подтверждения

Manifest поддерживает `confirmation.mode=none|service_id|exact_parameters`.

- `none`: метод можно выполнить только при пользовательском поручении и разрешённой политике; отсутствие протокола не расширяет права.
- `service_id`: prepare endpoint возвращает confirmation_id, нормализованное описание, expires_at и hash аргументов. Execute привязывает ID к subject, методу, аргументам и operation_id. Backend проверяет всё повторно.
- `exact_parameters`: prepare возвращает нормализованные аргументы, описание, expires_at и opaque precondition/version. Harness сохраняет hash детерминированного JSON утверждённых аргументов. Числовое представление сохраняется; совместимость с RFC 8785/JCS в MVP не заявляется. Execute отправляет подтверждённые аргументы и precondition; backend обязан атомарно проверить актуальность.

Prepare — без бизнес-изменений. Человеческий текст не содержит credentials. Решение сохраняет runtime с привязкой к подготовленной операции; один текст модели сам по себе не создаёт одобрение. Изменение цены, времени, получателя или иных существенных аргументов требует нового prepare/approval.

### Способ ответа пользователя

`confirmation.mode` определяет связь prepare/execute с аргументами, а не вид кнопок или сообщений. По умолчанию подтверждённый метод наследует способ общения run. Метод может указать `confirmation.reply_mode=protocol`, чтобы требовать бинарное подтверждение: отдельный callback при `interaction.approvals=true`, иначе точную текстовую фразу. `agent` и `exact_text` нельзя задавать в manifest: первый выбирается при создании run, второй — результат выбора fallback самим Loom. Для read или `confirmation.mode=none` override `protocol` запрещён.

```json
{
  "confirmation": {
    "mode": "service_id",
    "prepare_path": "/internal/agent/prepare",
    "reply_mode": "protocol"
  }
}
```

Это фрагмент метода, а не полный manifest. Требование входит в contract hash. Права, сроки и preconditions продолжают проверяться независимо от способа ответа. Подробная таблица выбора и контракт клиента — в [conversational input](../conversational-input.md).

Для внешней записи модель вызывает `module_call({module,method,arguments,confirmation_text})`. `confirmation_text` — положительная фраза на языке пользователя, отдельно от бизнес-параметров; для критического метода она обязательна с callbacks и без них, предел — 2000 UTF-8 bytes. Для остальных методов допускается пустая строка. В строгом текстовом fallback Loom не добавляет коды и не нормализует фразу: любое другое непустое сообщение отменяет ожидающее действие. Одновременно ожидает одно действие на чат; существенное изменение параметров требует нового prepare и нового согласия.

В справке метода описывайте предметный эффект, все существенные параметры, стоимость/ограничения, preconditions и требование критического подтверждения. Агент должен уметь объяснить, что пользователь разрешает. Не поручайте ему выводить этот контракт из имени метода.
