# Local setup and client workflow

The supported runtime is Linux/amd64 with Docker. Use a Linux shell or WSL for the commands in this guide. Docker must run Linux containers. Ordinary Go tests can run on Windows; process isolation requires Linux checks.

The development Compose file starts PostgreSQL, a migration job, the discovery proxy, the sandbox and the API. It does not include a Zitadel installation, service modules or the optional file processor. Commands use the repository root.

## Setup

Read [Setup](running/setup.md) for prerequisites, credentials, keys and startup.

## Boundaries

Read [Setup boundaries](running/setup.md#boundaries) for the limits of the development profile.

## Client

Read [Client](running/client.md) to create a run and reconnect to its events.

## Additional features

Read [Additional client features](running/client.md#additional-features).

## Recovery

Read [Recovery testing](running/recovery-testing.md) for interruption and reconciliation behavior.

## Verification

Read [Checks](running/recovery-testing.md#verification) and [Contributing](../CONTRIBUTING.md) for development checks. For an installed service, use [Operations](operations.md).
