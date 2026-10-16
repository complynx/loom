# Architecture v1

Авторизация использует [ограниченный positive cache и общую инвалидацию](identity-cache.md). Финальные permissions и локальные ограничения пересчитываются на существующих границах; sensitive operations обходят кэш.

Начните с [runtime](architecture/runtime.md): он объясняет границы API, координатора, БД, моделей и sandbox. [Execution](architecture/execution.md) описывает состояния run, подтверждение действий, аренды исполнения и сроки. Для расположения кода см. [Contributing](../CONTRIBUTING.md).

## Purpose and scope

Read [Runtime](architecture/runtime.md).

## Components

Read [Runtime](architecture/runtime.md).

## Design decisions

Read [Runtime](architecture/runtime.md).

## Run states

Read [Execution](architecture/execution.md).

## Conversation and pending action

Read [Execution](architecture/execution.md).

## Execution step

Read [Execution](architecture/execution.md).

## Clocks and deadlines

Read [Execution](architecture/execution.md).
