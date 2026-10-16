# Retention

[Guide index](../data.md)

## Retention и удаление

Для истории и idempotency metadata задаётся duration либо `forever`. Deployment-пример: история 365 дней. Файлы временные: deployment `artifacts.ttl_seconds`, default 86400 (24 часа), finite ceiling 7 дней. Legacy `artifact_retention` сохраняется в settings для совместимости, но не управляет файлами. Их `expires_at` не продлевается активным run; сборщик удаляет байты после истечения. Отдельного persisted trace stream нет; duration для trace отклоняется, поддерживается forever/unspecified. Recovery и финансовые records сохраняются; operational Docker/Loki logs имеют отдельную deployment retention policy. `forever` всё равно подчиняется storage quota и явному удалению.

Idempotency записи живут минимум максимального окна повторов/восстановления; после него сохраняются tombstones для запрета повторного исполнения использованных ключей. Принятые операции не исчезают раньше завершения reconciliation. Срок хранения backend deduplication проверяется отдельно от срока чата.

Удаление чата сначала блокирует новые run и доступ, затем отменяет существующий, ставит очистку событий, summary, индексов и blobs. Financial ledger сохраняет минимальные ссылки без содержимого сообщений. Неопределённые внешние операции не уничтожаются до сверки. Удаление default-чата позволяет атомарно создать новый при следующем запросе.

После восстановления старого backup требуется отдельная сверка удалений, отзывов и внешних эффектов до исполнения. Автоматический post-PITR barrier/replay tombstones не реализован; процедура и ограничения описаны в [restore recovery](../recovery-mode.md). Пользовательское удаление не обещает мгновенного удаления из исторических backups; срок их истечения документируется.
## Реализованные дополнительные слои

`previous_run_id` — необязательный cache hint для terminal run того же чата, владельца и организации. Только совместимый completed run позволяет повторно использовать сохранённый history prefix; model/effort/settings, actor, artifacts, retention и source count проверяются. Текущие права и micro-memory не берутся из этого кеша. TTL prefix — 15 минут; экономия provider cache не гарантируется. Исполнительные tool checkpoints предыдущего run не копируются.

[Active-run compaction](../active-compaction.md) сохраняет новую input window отдельно от истории и durable эффектов. Нативный Responses compact и fallback summary учитываются как отдельные платные шаги.

[Личная память](../user-memory.md) имеет scope issuer/subject/org, micro до 500 Unicode code points и три уровня on-demand данных. Она доступна без отдельных ролей, не удаляется вместе с чатом и очищается собственным API.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
