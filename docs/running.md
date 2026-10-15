# Запуск MVP

Целевая платформа — Linux amd64 с Docker. Сервис не запускается в Windows. Windows подходит для обычных Go-тестов; граница песочницы проверяется в Linux.

## Настройка

Скопируйте `deploy/config.example.yaml` в `deploy/config.yaml`, а `deploy/api.env.example` — в `deploy/api.env`. Задайте issuer, audience, introspection client, организацию администраторов, endpoint/key и имя модели. Проверьте capabilities и тарифы: примерные microcredits не являются ценами провайдера. Начальный баланс равен нулю; начисление выполняется через аудируемый billing API.

Для обращения к провайдеру явно разрешите `external_disclosure_default: true` до создания run. В MVP все provider endpoints считаются внешними: при запрете сервис не отправляет запросы, историю или сохранённые tool results провайдеру. Отзыв политики блокирует следующие шаги, а не стирает неизвестный оплаченный исход.

Настройте роли Zitadel и явное соответствие роли → permissions. Для удаления чата требуется `chat.delete`, для сверки inference — `operations.reconcile`. У сервисного actor должны совпадать права из проверенного токена и доверенного manifest. ID сессии сам по себе не даёт доступ.

Создайте отдельный AES-256 ключ:

```sh
openssl rand -base64 32 > deploy/auth.key
chmod 600 deploy/auth.key
```

Контейнер API читает ключ как UID 10001. Владелец или ACL bind-mounted файла должен разрешать этому UID чтение. Не добавляйте ключ, `.env`, `api.env` или рабочий config в Git. Храните резервную копию ключа отдельно: без него нельзя расшифровать сохранённые сессии.

Задайте `CLX_DEV_DB_PASSWORD` для локального Compose, затем:

```sh
docker compose --profile app up --build -d
```

Compose запускает миграции приложения и River перед API. PostgreSQL и API опубликованы только на loopback; внешний TLS и маршрутизация на сервере настраиваются отдельно. Docker proxy разрешает только ограниченное чтение discovery и удаляет container env/mounts из ответа. Полный Docker socket доступен только отдельному proxy; монтирование `:ro` само по себе не ограничивает команды Docker API.

JS supervisor запускается без сети, секретов, Linux capabilities и writable root filesystem. Общий лимит контейнера — 128 MiB и 32 PID; это лимит всех процессов песочницы, а не отдельного вызова. CLI supervisor и Compose задают число worker и cleanup deadline. Изменяйте соответствующие deployment ограничения вместе.

`/healthz` проверяет процесс. `/readyz` проверяет PostgreSQL, отдельную песочницу и свежесть доверенного discovery. `/openapi.json` описывает реализованные HTTP DTO. Bootstrap settings сохраняются один раз: после первого запуска их меняют через авторизованный settings API с проверкой версии.

## Границы MVP

Реализованы HTTP one-shot и SSE. WebSocket, автоматический OAuth token exchange/refresh, входящие непрозрачные сервисные секреты не входят в этот срез. Приватные метрики и JSON-логи настроены под [Alloy/Grafana Cloud](observability.md). Сервисы используют проверенные Zitadel access tokens. Динамическая конфигурация моделей может добавлять модели в пределах provider aliases, заданных deployment config; новый provider требует перезапуска.

## Клиент

```sh
curl -H "Authorization: Bearer $USER_TOKEN" \
  http://127.0.0.1:8080/v1/chats/default

curl -H "Authorization: Bearer $USER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-request-1' \
  --data '{"input":{"text":"Помоги выбрать"},"interaction":{"questions":"choices_and_text","approvals":true},"delivery":{"mode":"async"},"budget":{"max_microcredits":100000}}' \
  http://127.0.0.1:8080/v1/runs
```

Сохраните `run_id`. Статус читается через `/v1/runs/{id}`, события — `/v1/runs/{id}/events`. SSE поддерживает `Last-Event-ID`. Вопросы, steering и подтверждения отправляют через `/input` с отдельным idempotency key. `pause`, `resume` и `cancel` — отдельные POST ручки. Для новой задачи используется новый run; один nonterminal run занимает чат, включая состояния ожидания и паузы.

Сервис начинает делегацию с собственного Bearer и `X-Subject-Token` пользователя. Затем может использовать `X-Auth-Session-ID` со свежим собственным Bearer. Сессия связана с user, actor, issuer, audience и организацией. Просроченная или отозванная авторизация блокирует дальнейшее исполнение; reauthorize заменяет сессию авторизацией той же стороны.

## Дополнительные возможности

[Личная память](user-memory.md) доступна каждому авторизованному пользователю без отдельных ролей. Micro-профиль добавляется в каждый inference; три уровня остальной памяти читаются по необходимости. [API](api.md) описывает bounded history search/read, previous-run cache hints и temporary artifact handles. `search.semantic_enabled: false` полностью скрывает vector tool и отключает indexing route; включённый режим требует pgvector и готовых векторов, без embeddings-сервиса. Автоматические миграции и ключи описаны в [startup](startup.md).

## Восстановление

При SIGTERM сервис прекращает приём, отменяет inference/скрипты, сохраняет состояние и закрывает соединения в пределах пяти секунд. Клиент возвращается с прежним run ID. Неопределённый inference учитывается по [sponsored policy](active-compaction.md); исключённые случаи сохраняют `reconciling` до сверки. Неизвестный write новый worker не повторяет вслепую. [Hybrid retry](hybrid-retry.md) применяется отдельно к доказанным output-free отказам.

После исчерпания пяти попыток River задания startup/periodic sweep переводит run без живого job и lease в `paused` с причиной `queue_attempts_exhausted`. После восстановления зависимости клиент явно вызывает resume. При неопределённых результатах вместо этого требуется reconciliation; резерв сохраняется.

Write сверяется через status API модуля с прежним operation ID. Потерянную оплату inference сверяет оператор через `/v1/admin/runs/{id}/steps/{step}/reconcile`, с receipt или явной attestation, причиной и точной тарифицированной usage. После сверки resumable run становится paused. Резерв не освобождается по догадке; отменённые terminal run не возобновляются.

Удаление чата закрывает доступ и отменяет run. История и summaries очищаются согласно retention; финансовый аудит и необходимые recovery records сохраняются. Это контролируемое удаление, без обещания secure erase всех резервных копий и operational checkpoints. Retention новых ресурсов фиксируется при создании; обычный settings patch не сокращает срок уже существующих данных.

Для ротации шифрования сессий используйте [keyring и пакетную миграцию](session-key-rotation.md). Удаление старого ключа из конфигурации до завершения миграции блокирует startup; ключи для исторических backup сохраняйте отдельно.

## Проверки

```sh
go test ./...
CLX_TEST_DATABASE_DSN='postgres://...' sh scripts/linux-tests.sh
python3 scripts/dependency-lock.py --check
go mod verify
```

Без тестового DSN SQL/integration проверки пропускаются. Для acceptance нужен отдельный PostgreSQL и Linux `-race` gate. Обычные тесты используют fixtures. Отдельные отчёты фиксируют [local Zitadel browser QA](validation/2026-10-11-panel-login-qa/README.md) и [один live OpenAI call](validation/2026-10-11-openai-live-qa/LIVE-RESULT.md). Эти проверки не подтверждают production-настройки или все live model capabilities; deployment остаётся отдельной проверкой оператора.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
