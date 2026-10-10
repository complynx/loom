# Зависимости и политика обновлений

Проверено 2026-10-11. Полные коммиты выбранного графа находятся в [lock-файле](../tools/dependencies.lock.json). [Аудит](dependency-audit/2026-10-11/) содержит воспроизводимый кандидат `go.mod`/`go.sum`, список импортов, лицензии и снимок GitHub. Это исходный аудит выбора инструментов. Реализованный граф определяется корневым `go.mod`; итоговая проверка MVP хранится отдельно в `dependency-audit/2026-10-11-mvp/`. Неиспользуемые кандидаты не входят в бинарник.

Целевая платформа — **Linux/Docker, linux/amd64**. На complynx.net read-only команда uname подтвердила Linux x86_64. Production gate выполняется в Linux-контейнере; Windows-аудит относится только к локальной разработке. Контейнерные base images фиксируем digest sha256, а не плавающим tag; используемые digest записаны в `Dockerfile`, `compose.yaml` и скрипте Linux-проверок. CGO=0 — проверенный build-кандидат, ограничения OS sandbox требуют отдельной проверки.

## Фиксация

Первоначальный кандидат содержит 86 явно выбранных прямых и косвенных модулей. Lock реализации проверяется по полному `go list -m all`, включая дополнительные зависимости графа; его размер не равен числу импортов бинарника. Релизная версия записана рядом для диагностики. Выбираем коммит проверенного стабильного релиза; для Goja, где нет release tags, используем конкретный проверенный коммит. В production запрещены плавающие `@latest`, ветки и непроверенные `replace`.

В Go запрос `go get module@FULL_SHA` канонизируется в release version, если коммит помечен тегом, либо в pseudo-version с датой и сокращённым SHA. Поэтому `go.mod` хранит канонические версии, lock-файл — полные SHA, `go.sum` — хеши содержимого. Не конструируем pseudo-version вручную и не пытаемся записать полный SHA непосредственно в `require`. Проверяем `go mod verify`, согласованность графа с lock и происхождение коммитов. [Go modules reference](https://go.dev/ref/mod), [управление зависимостями](https://go.dev/doc/modules/managing-dependencies).

Dependabot `gomod` работает с `go.mod`/`go.sum`; наш JSON автоматически не обновляет. Его PR должен также обновить lock и пройти аудит. Go proxy перечисляет теги, поэтому регулярное отслеживание каждого нового коммита без тегов нельзя считать гарантированным. Dependabot умеет учитывать pseudo-version границы исправлений в security advisories. Для Goja отдельно проверяем upstream commits при обновлении. Еженедельный Dependabot настроен без auto-merge; обновление lock входит в проверку каждого PR. [Настройки Dependabot](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference), [реализация Go update checker](https://github.com/dependabot/dependabot-core/blob/main/go_modules/lib/dependabot/go_modules/update_checker/latest_version_finder.rb).

Go — **1.27.2**, свежий стабильный релиз на дату проверки. Закреплены коммит исходников и официальные SHA-256 архивов Linux amd64, Windows amd64 и исходников. Обновления Go выбирают новый стабильный релиз и проходят проверки; nightly/rc не используем. Локальная системная установка не изменена. [Официальные релизы](https://go.dev/dl/?mode=json).

## Исходный выбор и реализация

MVP использует pgx, sqlc, Goose, Huma, JSON Schema, Goja, YAML, River и Testify. Проверка JWT использует `go-jose/v4`, а OIDC discovery/introspection — стандартный HTTP. Zitadel SDK, goja_nodejs, Moby client, env, Testcontainers, goleak, backoff и gobreaker из исходного кандидата пока не нужны: async host broker, ограниченный Docker discovery и тестовый стенд реализованы без их импорта. Провайдер Responses вызывается через стандартный HTTP. Ниже сохранён первоначальный выбор для сравнения, а не список библиотек бинарника.

Звёзды — снимок GitHub, приблизительные значения в таблице. Это показатель распространённости, а не доказательство безопасности. Все перечисленные репозитории не архивированы; точные даты создания, последнего push и счётчики сохранены в `github-evidence.json`.

| Назначение | Модуль / версия | Лицензия | Популярность и решение |
| --- | --- | --- | --- |
| PostgreSQL | pgx/v5 5.11.0 | MIT | 14.3k, с 2013; основной драйвер и pool |
| SQL codegen, dev tool | sqlc 1.31.1 | MIT | 18.4k, с 2019; типизированный SQL, без sqlx/ORM |
| SQL migrations | Goose 3.28.0 | MIT | 11.6k, с 2016; SQL migrations, pgx stdlib для database/sql |
| HTTP/OpenAPI | Huma/v2 2.39.1 | MIT | 4.4k, с 2020; адаптер стандартного ServeMux |
| Manifest validation | jsonschema/v6 6.0.3 | Apache-2.0 | 1.3k, с 2017; отключаем произвольные remote refs, ограничиваем размер |
| OIDC / Zitadel | zitadel/oidc/v3 3.51.14 | Apache-2.0 | 1.9k, с 2019; claims/delegation дополнительно проверяем на нашем Zitadel |
| JS engine | Goja, 73dc7edc278f… | MIT + MIT/Lucent notice | 7.1k, с 2016; runtime API не обещает стабильность релизами, обязательный spike |
| JS async loop | goja_nodejs, b481721df8a2… | MIT | 441, с 2016; только eventloop, без предоставления JS файловой системы/сети |
| Docker discovery | moby/moby/client 0.6.2 | Apache-2.0 | 72.2k у Moby в целом; новый split module, не 72k пользователей этого клиента |
| YAML | go.yaml.in/yaml/v3 3.0.5 | MIT и Apache-2.0 notices | 534 у нового maintained repo; зрелая v3, security fixes; v4 пока RC |
| Env config | caarlos0/env/v11 11.4.1 | MIT | 6.3k, с 2015; поверх typed config |
| PG jobs | River core 0.49.0 + riverpgxv5 | MPL-2.0 | 5.8k, с 2023; самый молодой существенный компонент, v0 API: pin и интеграционный gate |
| Assertions | Testify 1.12.1 | MIT; внутренний spew ISC | 26.2k, с 2012; assert/require, без mock framework по умолчанию |
| PG integration tests | Testcontainers Go/postgres 0.44.0 | MIT | 5k, с 2018; реальные PG только для SQL/transaction semantics |
| Goroutine leak checks | goleak 1.3.0 | MIT | 5.3k, с 2017; редкие tags, repo активен в 2026 |
| Retry scheduling | backoff/v5 5.0.3 | MIT | 4.1k, с 2014; retries только разрешённых контрактом операций |
| Circuit breaker | gobreaker/v2 2.4.0 | MIT | 3.7k, с 2015; transient dependency failures |

Для HTTP, логов, regex, cancellation и дешёвых тестов используем стандартные `net/http`, `log/slog`, `regexp`, `context`, `httptest`, `testing/synctest`. Не добавляем библиотеку ради того, что уже делает stdlib. Для первого провайдера используем стандартный HTTP; SDK не добавлен.

River управляет доставкой PG jobs, повторами и worker lifecycle. Состояние run, fencing, billing и idempotency внешних изменений остаются доменными гарантиями приложения. River не даёт exactly-once внешнего API. Вставка job и изменение run выполняются в одной PG транзакции. Проверяем отмену, recovery и shutdown до принятия интеграции; River Pro не используем.

Goja не является границей безопасности. JS работает в отдельном OS worker с ограничениями ресурсов; host callbacks должны соблюдать context и собственные таймауты. Принудительный interrupt не делает прерванный внешний write отменённым. Проверка памяти, async execution и kill остаётся release gate.

## Лицензии

Разрешены MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, MPL-2.0, ISC, Unlicense и конкретный Lucent notice из Goja `ftoa/LICENSE_LUCENE` (`LicenseRef-MIT-Lucent`). Последние три варианта явно разрешены Daniel. Сохраняем все требуемые copyright/license notices; для YAML не теряем оба уведомления. MPL требует выполнения условий для изменённых покрытых файлов; не переносим их код в закрытые файлы без отдельной проверки.

Исключение: **golangci-lint 2.14.0, GPL-3.0, только инструмент разработки/CI**, явно разрешён Daniel. В бинарник сервиса не входит. Другие GPL/AGPL/LGPL зависимости этим исключением не разрешены. Новый неизвестный license, custom license или смена лицензии требуют отдельного решения.

`go-licenses/v2 2.0.1` (Apache-2.0) проверил импортируемые библиотеки Linux amd64 и Windows amd64: 92 и 93 license entries. Это не число модулей: вложенные пакеты могут иметь отдельную лицензию. Основные tools проверены по корневой лицензии; полный транзитивный/native аудит исполняемых sqlc/линтера не выполнен. Scanner предупреждает, что assembly/non-Go код не может полностью проверить; перед поставкой нужны notices фактического бинарника и включённых native assets. Скрипты и tool binaries пользователям сейчас не поставляются.

## Уязвимости и ограничения проверки

Аудит выполнен `govulncheck 1.8.0`, Linux и Windows amd64, CGO=0, package scan импортов кандидата. База сообщила последнюю модификацию 2026-10-08. Это проверка известных advisories и импортов, не pentest и не доказательство безопасности будущих вызовов приложения.

Начальный граф требовал исправлений: Go 1.27.0 → 1.27.2; `golang.org/x/crypto` 0.55.0 → **0.56.0** (SSH DoS advisories); `moby/go-archive` 0.2.0 → **0.3.0** (path traversal в тестовом Docker стеке). Исправленные commits входят в lock. [Go advisories](https://pkg.go.dev/vuln/), [go-archive advisory](https://pkg.go.dev/vuln/GO-2026-6253).

Осталось одно предупреждение уровня модуля — **GO-2026-5932**: deprecated `golang.org/x/crypto/openpgp` небезопасен во всех версиях. Его пакетов нет в импортируемом графе кандидата; исправлять это повышением версии x/crypto бессмысленно. Запрещаем импорт openpgp и его подпакетов через depguard. Не подавляем целиком проверки x/crypto. В сохранённых JSON — config, соответствующий advisory и findings; остальные записи справочной базы удалены для компактности. [OpenPGP advisory](https://pkg.go.dev/vuln/GO-2026-5932).

Перед merge обновления проверяем SHA/содержимое, изменение лицензий, govulncheck, lint, компиляцию и релевантные тесты. Для pgx/River/Goose — реальные PG integration tests; для auth — local JWKS/introspection fake и отдельный Zitadel contract test; для Goja — interrupt/async/resource-limit tests. Не требуется Docker или Zitadel для каждого unit test. Ни один пакет не объявляется безопасным только по звёздам или дате релиза.

## Инструменты

golangci-lint закреплён **v2.14.0**, полный commit в lock, настройки v2 в [`.golangci.yml`](../.golangci.yml). Windows archive проверен по официальному SHA-256; config проверяется самим линтером. Официальный готовый linter собран на Go 1.27.0: для CI предпочтительна сборка закреплённого commit обновлённым Go 1.27.2; toolchain приложения от этого не меняется. `govulncheck`, `go-licenses`, sqlc также закреплены commit. MVP содержит `scripts/install-tools.sh` для сборки линтера и govulncheck из полных коммитов и CI workflow с PostgreSQL, race-тестами, проверкой lock, линтером, vulnerability scan и Docker build. Локальные проверки выполнены; запуск workflow на GitHub ещё не проверен.
