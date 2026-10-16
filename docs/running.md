# Local setup and client workflow

The supported runtime is Linux/amd64 with Docker. Use a Linux shell or WSL for the commands in this guide. Docker must run Linux containers. Ordinary Go tests can run on Windows; process isolation requires Linux checks.

The development Compose file starts PostgreSQL, a migration job, the discovery proxy, the sandbox and the API. It does not include a Zitadel installation, service modules or the optional file processor. Commands use the repository root.

## Настройка

Read [Setup](running/setup.md) for prerequisites, credentials, keys and startup.

## Границы

Read [Setup boundaries](running/setup.md#границы) for the limits of the development profile.

## Клиент

Read [Client](running/client.md) to create a run and reconnect to its events.

## Дополнительные возможности

Read [Additional client features](running/client.md#дополнительные-возможности).

## Восстановление

Read [Recovery testing](running/recovery-testing.md) for interruption and reconciliation behavior.

## Проверки

Read [Checks](running/recovery-testing.md#проверки) and [Contributing](../CONTRIBUTING.md) for development checks. For an installed service, use [Operations](operations.md).
