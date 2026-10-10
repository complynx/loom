# clx-scripter

Агентский API-сервис для инфраструктуры Complynx. Язык приложения — Go, основное хранилище — PostgreSQL. Модель выполняет задачи через ограниченную JS-песочницу и модули других сервисов.

Репозиторий содержит реализацию MVP и проектную спецификацию. API, PostgreSQL, inference, очередь River, подтверждения, история и отдельная JS-песочница проверяются поведенческими тестами. Production deployment и интеграция с действующим Zitadel требуют отдельной проверки с реальными настройками.

## Разработка

Требуется Go 1.27.2. `go test ./...` запускает дешёвые тесты; проверки SQL и транзакций используют отдельную PostgreSQL, заданную через `CLX_TEST_DATABASE_DSN`. Linux-проверки с race detector запускаются через `scripts/linux-tests.sh`. Команды сборки и проверки находятся в `Makefile`.

Go-модули закреплены каноническими версиями, полными коммитами в `tools/dependencies.lock.json` и хешами `go.sum`. Контейнеры и CI actions закреплены digest/commit. Обновления проходят review и проверки.

## Документы

- [Запуск, настройка и восстановление MVP](docs/running.md)
- [Результаты локальной проверки MVP](docs/validation/2026-10-11/README.md)
- [Поддерживаемые настройки](internal/config/SUPPORT.md)
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

Термины «должен» и «нельзя» задают обязательный контракт v1. Значения таймаутов и лимитов в примерах — предлагаемые defaults, а не измеренные характеристики.

Первая версия: один активный run на чат, несколько чатов пользователя, пассивные события от сервисов, вопросы и steering, два протокола подтверждения, Docker discovery, учёт кредитов, сохраняемое исполнение. Проактивные запуски и субагенты в v1 не входят.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
