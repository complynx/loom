# Зависимости и обновления

Авторитетный граф Go-зависимостей задают [go.mod](../go.mod) и [go.sum](../go.sum). Отдельный lock-файл, full-commit pins библиотек и синхронизация JSON с Go-графом не используются. Для модулей без release tags допустимы стандартные pseudo-versions. Не добавляйте непроверенные replace.

## Состав

Runtime использует pgx, Goose, River core, Huma/ServeMux, JSON Schema, Goja, go-jose, YAML и официальный OpenTelemetry SDK. Responses вызывается через стандартный HTTP. Testify используется для тестов. SQLc генерирует код там, где он применяется; реальные зависимости проверяйте по imports/go.mod, а не по первоначальному списку кандидатов.

HTTP, slog, regexp, context, httptest и testing/synctest покрывают задачи стандартной библиотеки. River даёт PG jobs, но не exactly-once внешнего API. Goja работает в отдельном ограниченном процессе и не заменяет OS isolation.

Go/tool версии определены go.mod, Dockerfile и scripts/install-tools.sh. CI actions и base images закреплены SHA/digest; application latest/commit tags описаны в [CI](ci.md). Не смешивайте правила build inputs и выбор образа оператором.

## Лицензии

Разрешены MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, MPL-2.0, ISC, Unlicense и конкретный MIT/Lucent notice Goja. Сохраняйте необходимые уведомления; MPL-изменения проверяйте на уровне покрытых файлов.

Разрешён GPL golangci-lint как development/CI tool, не часть Go-бинарника. Отдельно разрешены изолированные CLI Poppler/FFmpeg в media container. Их лицензии и corresponding-source обязательства описаны в [file-worker](file-worker.md); это не общее разрешение добавлять любые GPL/AGPL Go-библиотеки.

Лицензии проверяются периодически вручную и при выборе новых прямых зависимостей. Это не CI merge gate. [Процедура](dependency-audit/README.md). Обязательные notices остаются в репозитории и поставляемых образах; старый аудит не удостоверяет новый граф.

## Обновления и проверка

Dependabot меняет go.mod/go.sum обычным способом и группирует minor/patch обновления. Major updates проходят отдельно. Для merge нужны релевантные behavioral/race tests, Golden lint, govulncheck и Docker builds; для PG/auth/sandbox изменений проверяйте соответствующую внешнюю границу. Команды: [QA](qa.md), [lint](linting.md).

Govulncheck проверяет известные advisories используемого графа, не доказывает отсутствие любых уязвимостей. Популярность и старые даты аудита также не являются гарантией. Инструменты/native packages проверяются отдельно от runtime Go imports. Deprecated OpenPGP запрещён depguard; не подавляйте целиком проверки x/crypto.
