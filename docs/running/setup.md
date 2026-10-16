# Setup

[Guide index](../running.md)

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

JS supervisor запускается без сети, секретов, Linux capabilities и writable root filesystem. Общий лимит контейнера — 128 MiB и 128 PID; это лимит всех процессов песочницы, а не отдельного вызова. CLI supervisor и Compose задают число worker и cleanup deadline. Изменяйте соответствующие deployment ограничения вместе.

`/healthz` проверяет процесс. `/readyz` проверяет PostgreSQL, отдельную песочницу и свежесть доверенного discovery. `/openapi.json` описывает реализованные HTTP DTO. Bootstrap settings сохраняются один раз: после первого запуска их меняют через авторизованный settings API с проверкой версии.
## Границы

Реализованы HTTP one-shot и SSE. WebSocket, автоматический OAuth token exchange/refresh, входящие непрозрачные сервисные секреты не входят в этот срез. Приватные метрики и JSON-логи настроены под [Alloy/Grafana Cloud](../observability.md). Сервисы используют проверенные Zitadel access tokens. Динамическая конфигурация моделей может добавлять модели в пределах provider aliases, заданных deployment config; новый provider требует перезапуска.
