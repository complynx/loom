# Authentication routes

[Guide index](../api.md)

## Аутентификация

Положительные token claims используют [ограниченный кэш прав](../identity-cache.md); sensitive writes требуют online-проверки. Админская ручка `POST /v1/admin/identity/cache/clear` очищает его для всех API/workers общей базы; нужны `settings.write`, admin organization и свежая авторизация.

- Прямой запрос: `Authorization: Bearer <user-access-token>`.
- Сервисный запрос: `Authorization: Bearer <service-access-token>` и, при bootstrap делегации, `X-Subject-Token: <user-access-token>`.
- После bootstrap сервис указывает `X-Auth-Session-ID` (либо `auth_session_id` в run-create body); сессия привязана к пользователю, actor, организации и разрешённым аудиториям. ID не является bearer credential.
- Один запрос не может одновременно использовать `X-Subject-Token` и `auth_session_id`.
- MVP принимает проверенные access tokens. Входящие непрозрачные сервисные секреты и автоматический token exchange/refresh не реализованы; deployment binding остаётся требованием для будущего секретного режима.

Все запросы выполняются по TLS на внешней границе. По умолчанию credentials не логируются; явный debug payload mode может сохранять claims и JWT с замаскированной подписью. См. [logging policy](../observability.md). Запросы без refreshable сессии допустимы, но могут перейти в `waiting_auth`.
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
| POST | `/admin/runs/{id}/recovery/prepare` | Подготовка expired uncertain Running к сверке; только recovery mode |
| POST | `/admin/runs/{id}/steps/{step}/reconcile` | Аудируемая сверка неизвестной оплаты inference |
| POST | `/artifacts` | Ограниченная загрузка файла |
| GET | `/artifacts/{id}` | Метаданные |
| GET | `/artifacts/{id}/content` | Авторизованное чтение целиком или HTTP byte ranges |
| HEAD | `/artifacts/{id}/content` | Те же проверки доступа, только headers |

`/healthz` проверяет процесс; `/readyz` — способность безопасно принимать durable запросы. Metrics доступны только через отдельный private listener. Полный актуальный wire contract отдаёт `/openapi.json`; таблица выше — основные routes. [Личная память](../user-memory.md) и [diagnostics/panel](../diagnostics.md) описывают дополнительные endpoints.

| Метод | Путь относительно `/v1` | Назначение |
| --- | --- | --- |
| GET | `/users/me/credits` | Баланс и резерв |
| POST | `/chats/{id}/history/read` | Bounded страницы событий или текста одного события |
| POST | `/chats/{id}/history/embeddings` | Внешний вектор для события; route только при enabled, нужны `chat.embeddings.write` и `chat.read` |
| POST | `/runs/{id}/artifacts/handles` | Временный handle из `{artifact_id}` |
| GET | `/artifact-handles/{id}/content` | Авторизованный stream/ranges по handle, ID сам не является credential; TTL до 15 минут, не дольше срока файла |
| HEAD | `/artifact-handles/{id}/content` | Headers действующего handle без body |
