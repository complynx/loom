# Client

[Guide index](../running.md)

## Client

Complete [Setup](setup.md) first. These shell examples use a user access token in `USER_TOKEN`. The user needs mapped run permissions, a funded billing account and an enabled disclosure policy. Do not paste access tokens into committed files or reports.

```sh
curl -H "Authorization: Bearer $USER_TOKEN" \
  http://127.0.0.1:8080/v1/chats/default

curl -H "Authorization: Bearer $USER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-request-1' \
  --data '{"input":{"text":"Помоги выбрать"},"interaction":{"reply_mode":"agent","questions":"choices_and_text","approvals":true},"delivery":{"mode":"async"},"budget":{"max_microcredits":100000}}' \
  http://127.0.0.1:8080/v1/runs
```

An asynchronous response confirms admission, not task completion. If `chat_id` is omitted, Loom uses the default chat. Save the returned `run_id` as `RUN_ID`, then read status or follow events:

```sh
curl --fail -H "Authorization: Bearer $USER_TOKEN" \
  "http://127.0.0.1:8080/v1/runs/$RUN_ID"

curl --fail -N -H "Authorization: Bearer $USER_TOKEN" \
  "http://127.0.0.1:8080/v1/runs/$RUN_ID/events"
```

Reuse the same idempotency key only when retrying the same immutable request. Use a new key for a new request.

SSE поддерживает `Last-Event-ID`. В режиме `agent` весь обычный текст, ответы и подсказки-кнопки отправляют как steering в `/input` существующего run с отдельным idempotency key. `approvals=true` дополнительно разрешает отдельные бинарные callbacks для критических действий. [Режимы, примеры и точное текстовое подтверждение](../conversational-input.md). `pause`, `resume` и `cancel` — отдельные POST ручки. Для новой задачи используется новый run; один nonterminal run занимает чат, включая состояния ожидания и паузы.

Сервис начинает делегацию с собственного Bearer и `X-Subject-Token` пользователя. Затем может использовать `X-Auth-Session-ID` со свежим собственным Bearer. Сессия связана с user, actor, issuer, audience и организацией. Просроченная или отозванная авторизация блокирует дальнейшее исполнение; reauthorize заменяет сессию авторизацией той же стороны.
## Additional features

[Личная память](../user-memory.md) доступна каждому авторизованному пользователю без отдельных ролей. Micro-профиль добавляется в каждый inference; три уровня остальной памяти читаются по необходимости. [API](../api.md) описывает bounded history search/read, previous-run cache hints и temporary artifact handles. `search.semantic_enabled: false` полностью скрывает vector tool и отключает indexing route; включённый режим требует pgvector и готовых векторов, без embeddings-сервиса. Автоматические миграции и ключи описаны в [startup](../startup.md).
