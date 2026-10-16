# Конфигурация и эксплуатационные границы

Этот документ описывает текущую конфигурацию приложения; серверный профиль находится в deployment-репозитории оператора. Для запуска см. [startup](startup.md), для сопровождения — [operations](operations.md).

## YAML и env

YAML/env задают подключения, trusted issuer/audience, provider credentials, Docker proxy, ключи, временные файлы и hard resource ceilings. Env имеет приоритет; неизвестная или неверная настройка останавливает startup. Секреты не возвращаются admin API и не доступны JS. [Поддерживаемые поля](../internal/config/SUPPORT.md), [пример](../examples/config.yaml).

БД содержит model catalog/capabilities, тарифы, defaults, quotas, context budgets, retention и disclosure policy. Bootstrap создаёт настройки только при их отсутствии; рестарт с изменённым YAML не перезаписывает admin revision.

PATCH /v1/admin/settings принимает полный типизированный объект с актуальной version в body. Это не JSON Merge Patch или ETag. Несовпадение версии даёт 409; обновление и audit атомарны. Модель/effort другого пользователя требуют отдельного права.

Run сохраняет settings/tariff snapshot. Новые ограничения admission действуют сразу; script/model использует более строгие snapshot/current/hard значения на следующей границе. Начатый вызов не прерывается по обычному settings patch. Secrets/provider bindings/hard ceilings требуют рестарта. Retention patch относится к будущим ресурсам.

## Runtime

Linux/amd64, PostgreSQL, River и отдельные sandbox/file workers. Docker discovery использует ограниченный proxy; Docker socket не выдаётся API или JS. Non-root workers не имеют сети/секретов и работают с ограничениями контейнера. Goja не является самостоятельной границей безопасности.

Startup сериализует миграторы, проверяет схему и ключи до запуска workers/API. Миграциям нужны DDL-права в выделенной базе, не PostgreSQL superuser. Раздельный migration job можно включить вместо migrate_on_start. Все экземпляры используют согласованные ключи; обновление не должно оставлять старых несовместимых writers.

Артефакты временные; авторитетный источник долгоживущего файла — внешний сервис. Сохраняются БД и ключи сессий, а не обещание вечного хранения файлов. Restore выполняется через [recovery barrier](recovery-mode.md).

## Контракты сопровождения

- Settings и права перепроверяются на ответственных границах. Согласие не заменяет авторизацию.
- Unknown write сверяется с сервисом по прежнему operation ID. Повторный run не является recovery.
- SIGINT/SIGTERM запускает drain с бюджетом до пяти секунд. Измеряйте фактический выход процесса отдельно от задержки Docker notification.
- Logs, metrics и OTel — диагностические каналы. Они не заменяют durable audit/ledger.
- Новые функции и непроверенные сценарии перечислены в [треке](development-track.md). Документация QA задаёт критерии, а результаты прогонов сохраняются вне исходников.
