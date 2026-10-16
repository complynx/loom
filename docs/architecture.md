# Архитектура v1

Авторизация использует [ограниченный positive cache и общую инвалидацию](identity-cache.md). Финальные permissions и локальные ограничения пересчитываются на существующих границах; sensitive operations обходят кэш.

Начните с [runtime](architecture/runtime.md): он объясняет границы API, координатора, БД, моделей и sandbox. [Execution](architecture/execution.md) описывает состояния run, подтверждение действий, аренды исполнения и сроки. Для расположения кода см. [Contributing](../CONTRIBUTING.md).

## Цель и контекст

Read [Runtime](architecture/runtime.md).

## Компоненты

Read [Runtime](architecture/runtime.md).

## Основные решения

Read [Runtime](architecture/runtime.md).

## Состояния run

Read [Execution](architecture/execution.md).

## Диалог и ожидающее действие

Read [Execution](architecture/execution.md).

## Шаг исполнения

Read [Execution](architecture/execution.md).

## Отдельные виды времени

Read [Execution](architecture/execution.md).
