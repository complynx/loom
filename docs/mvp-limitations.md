# Возможности, проверки и ограничения

Этот документ отделяет поддерживаемые возможности от непроверенных сценариев. Приёмку установленного сервиса выполняет оператор. Прежнее имя файла сохранено для совместимости ссылок.

## Реализовано и проверено

| Область | Подтверждённая граница |
| --- | --- |
| API, River, PostgreSQL | Поведенческие тесты транзакционности inputs/jobs, idempotency, одного run на чат, истории, SSE и billing. Linux race, Golden и Docker build входят в CI. |
| Разговорный ввод | Детерминированные тесты steering, уточнений с blocked action, нового prepare, native callbacks, exact-text, stale consent и restart. Живая интерпретация отдельно. |
| Zitadel | Настоящий локальный issuer: JWT/introspection, cross-org отказ, live role removal, signing-key rotation и API/River restart → reauthorize после отзыва credentials. Proxy SSO поддерживается отдельно от direct PKCE. |
| OpenAI | Минимальный live Responses вызов и settlement. Expanded tools/vision/compaction quality этим не подтверждены. |
| Внешний клиент | Выделенный стенд: Telegram → делегированный run → ответ, история следующего обращения, фильтрация методов по правам и восстановление того же run. Provider — manual mailbox. |
| Write через внешний сервис | Локальная consumer-приёмка: nested prepare → restart → approval → реальный write; replay/status сохраняют один receipt/эффект. Это protocol-mode сценарий. |
| Внешние файлы | Native import из внешнего сервиса, читаемый PDF render/text, video frame/audio extraction, audio metadata и явный refetch внешнего ID после expiry artifact. Автоматический refresh helper проверен отдельно локально. |
| Память, поиск, контекст | Owner isolation, micro до 500 Unicode characters, literal/fulltext/RE2, bounded read, vector-only API, prefix reuse и durable compaction проверены fixtures/PG. |
| Отказы | Изолированные стенды: disk-full/read-only, отмена, потерянные ответы, worker kill/OOM и bounded restore/recovery barrier. Это не нагрузочные пределы production-хоста. |
| Диагностика | API/agent functions, logs/metrics и bounded OTel export проверены локально. |

Consumer-приёмка закрывает отсутствие реального адаптера и ограниченные read/write/file сценарии. Она относится к конкретным сборкам, а не автоматически к каждой следующей версии. Порядок повторной проверки — в [QA](qa.md).

## Реализовано, но соответствующая проверка открыта

| Сценарий | Что есть | Что остаётся |
| --- | --- | --- |
| PKCE | Прямой authorization-code/PKCE реализован, проверен с локальным настоящим Zitadel. | Настроить и принять выбранный режим установки; proxy SSO не требует прямого PKCE-клиента. |
| Live role removal | Online authority, bounded positive cache, sensitive bypass и purge через PG generation. | Отзыв/purge в текущем issuer/cache профиле установки. Warm cache может задержать обычный отзыв до TTL; stale-on-error нет. |
| Signing-key rotation | JWT/JWKS refresh и локальная настоящая rotation. | Операционная rotation и отдельный отказ JWKS при доступной introspection в текущем серверном профиле. |
| Queue restart → reauthorize | Сохранение run/CAS и локальная цепочка после настоящего отзыва JWT. | Полная цепочка именно при TTL expiry и клиентском flow установки. |
| Свободное согласие | Runtime связывает решение с последним input и точной операцией. | Live RU/EN, условные/поздние ответы и интеграция UI клиента. Старые бинарные approval-тесты этого не доказывают. |
| Модель/файлы | Image attachment, native compaction и paid transcription реализованы. | Live vision, качество/стоимость compaction, платная транскрипция и unattended выбор инструментов. |
| Эксплуатация | Startup, telemetry, keyring и recovery guards реализованы. | Сквозной backend/alert acceptance, target load/host OOM, off-host/PITR restore и backup key rotation. |

## Не реализовано

- Разрешённая cross-org delegation: запрещена намеренно. Для поддержки нужен отдельный trust contract.
- Автоматический token exchange/offline refresh/refresh rotation внутри Loom. Expiry требует reauthorize; run может ждать в waiting_auth.
- Входящие opaque service secrets. Сервисы используют Zitadel access tokens и trusted caller binding.
- Model token deltas/WebSocket. SSE передаёт durable run events; steering применяется на границах шагов.
- Универсальный scheduler параллельных model calls. Parallel host RPC доступны внутри JS.
- Embeddings-сервис, автоиндексация и ANN. Semantic API принимает готовые векторы и делает exact cosine ranking; выключенный режим скрывает инструмент.
- Provider previous_response_id и полный transcript reuse. previous_run_id — совместимый локальный history-prefix hint до 15 минут, не resume и не гарантия cache hit.
- Общий circuit breaker всех зависимостей, автоматическое получение поздней usage и global platform spend cap. Provider hybrid retry и bounded sponsored recovery уже есть.
- Remote cancellation через manifest cancel_path. Cancel не означает rollback внешнего эффекта.
- Object storage/chunk upload sessions/перенос blobs между hosts и native PDF/audio/video model input. Картинки передаются напрямую; остальные форматы обрабатывает worker.
- Доказанная private-inference граница, automatic memory extraction, memory admin UI, субагенты и proactive runs.
- Privileged retention migration существующих данных и secure erase всех checkpoint/audit/backup следов.

## Эксплуатационные границы

Один нетерминальный run занимает чат, включая паузу/ожидание. Пассивные события не запускают inference. Ожидающие действия показываются последовательно; без reply reference одинаковый поздний текст неотличим от нового. Исправленный prepare требует нового согласия.

Файлы временные: default 24 часа, конечный TTL до 7 дней. Активный run не продлевает expiry. Raw uploads/derived outputs без внешнего refetch-контракта не восстанавливаются автоматически. Долгоживущий оригинал хранит сервис-владелец.

Readiness не является writable-capacity probe: при ENOSPC/read-only соединение с БД может работать. Контролируйте свободное место и ошибки записи отдельно. Sandbox limits общие для контейнера; это не per-worker memory isolation или HA.

OpenAI hard/trigger/target defaults — 272000/244800/204000 на модель. Byte bounds могут запустить compaction раньше. Native output выбирает provider; target не гарантирует итоговую usage. Compaction оплачивается; reservation может потребовать полный возможный output window.

Все model endpoints считаются внешними и требуют disclosure. Секреты не выдаются JS. Debug payload logs могут содержать пользовательский текст/JWT claims; маскирование известных credentials не распознаёт произвольный секрет в тексте.

Startup применяет pending migrations; старые workers должны быть остановлены. Автоматического down migration нет. БД/ключи сохраняются между заменами контейнеров. Серверные конфиги и инструкции конкретной установки находятся вне репозитория.
