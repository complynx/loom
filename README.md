# Loom

Loom — агентский API-сервис для инфраструктуры Complynx. Язык приложения — Go, основное хранилище — PostgreSQL. Модель выполняет задачи через ограниченную JS-песочницу и модули других сервисов.

Репозиторий содержит реализацию MVP и проектную спецификацию. API, PostgreSQL, inference, очередь River, подтверждения, история и отдельная JS-песочница проверяются поведенческими тестами. Изолированные local Zitadel и минимальный live OpenAI call имеют отдельные отчёты; production deployment и production identity/client integration требуют проверки с реальными настройками.

Кодовое название проекта — **Loom**. Репозиторий и Go module — `github.com/complynx/loom`, executable и Docker image — `loom` и `complynx/loom`. Существующие `CLX_*` env variables, права и протокольные `clx.harness.*` labels сохранены для совместимости.

Схема до первого production release объединена в `migrations/00001_loom.sql`. Она предназначена для новой БД; старые QA/dev базы с цепочкой 00001–00012 не обновляйте этой baseline поверх существующей схемы. Сохраните их со старым образом или перенесите данные отдельным проверенным процессом. Объединение не изменяет River migrations.

## Разработка

Требуется Go 1.27.2. `go test ./...` запускает дешёвые тесты; проверки SQL и транзакций используют отдельную PostgreSQL, заданную через `CLX_TEST_DATABASE_DSN`. Linux-проверки с race detector запускаются через `scripts/linux-tests.sh`. Команды сборки и проверки находятся в `Makefile`.

Go-модули закреплены каноническими версиями, полными коммитами в `tools/dependencies.lock.json` и хешами `go.sum`. Контейнеры и CI actions закреплены digest/commit. Обновления проходят review и проверки.

## Документы

- [Ограничения MVP](docs/mvp-limitations.md)
- [Дальнейший трек разработки](docs/development-track.md)
- [Запуск, настройка и восстановление MVP](docs/running.md)
- [Запуск, миграции и ключи](docs/startup.md)
- [Review всего кода и итоговые gates](docs/validation/2026-10-11-full-review/README.md)
- [Личная память пользователя](docs/user-memory.md)
- [Diagnostics API и панель](docs/diagnostics.md)
- [Hybrid retry](docs/hybrid-retry.md)
- [Active-run compaction и platform recovery](docs/active-compaction.md)
- [OpenAI, тарифы и компактификация](docs/openai.md)
- [Тестовые провайдеры](docs/test-providers.md)
- [CI, Docker Hub и триггер серверного деплоя](docs/ci.md)
- [Результаты локальной проверки MVP](docs/validation/2026-10-11/README.md)
- [Поддерживаемые настройки](internal/config/SUPPORT.md)
- [Ротация ключей сессий](docs/session-key-rotation.md)
- [Метрики, уровни логов и подключение Alloy](docs/observability.md)
- [Аудит реализованных зависимостей](docs/dependency-audit/2026-10-11-mvp/README.md)

- [Архитектура и принятые решения](docs/architecture.md)
- [Клиентский HTTP API](docs/api.md)
- [Discovery и контракт модулей](docs/modules.md)
- [Авторизация и подтверждения](docs/security.md)
- [Данные, история, контекст и кредиты](docs/data.md)
- [Недоступности, отказы и восстановление](docs/failures.md)
- [Конфигурация, эксплуатация и план реализации](docs/implementation.md)
- [Зависимости, лицензии, аудит и фиксация коммитов](docs/dependencies.md)
- [Пример manifest](examples/manifest.json)
- [JSON Schema manifest v1](schemas/manifest.schema.json)
- [Пример конфигурации](examples/config.yaml)

Термины «должен» и «нельзя» задают целевой обязательный контракт v1. Реализованный срез и следующие этапы перечислены в ограничениях MVP и треке разработки. Значения таймаутов и лимитов в примерах — предлагаемые defaults, а не измеренные характеристики.

Первая версия: один активный run на чат, несколько чатов пользователя, пассивные события от сервисов, вопросы и steering, два протокола подтверждения, Docker discovery, учёт кредитов, сохраняемое исполнение. Проактивные запуски и субагенты в v1 не входят.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk

Go linting: [Golden configuration and commands](docs/linting.md).

Personal user memory: [layout, HTTP API and agent tools](docs/user-memory.md).
