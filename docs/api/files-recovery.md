# Files recovery

[Guide index](../api.md)

## Частичное чтение blobs

Оба content endpoint принимают `Range: bytes=offset-end`, открытый конец, suffix и multipart ranges. Ответ — 206 с `Content-Range`; обычное чтение — 200. HEAD возвращает полные headers без body и игнорирует Range/If-Range. `ETag` содержит SHA-256 исходных bytes; для возобновления используйте `If-Range`. При несовпадении `If-Range` сервер отдаёт полный 200, поэтому клиент обязан проверять status перед дописыванием файла. Совпадающий `If-None-Match` даёт 304, проваленный `If-Match` — 412, недоступный/невалидный диапазон — 416. Arbitrary MIME сохраняется; multipart использует `multipart/byteranges`.

Владение, `artifacts.read`, `expires_at` файла и TTL handle проверяются до conditional/range обработки. Чужой или истёкший handle не даёт 304 или раскрытие размера. Long transfer сохраняет периодическую reauthorization, cancellation и 30-second write deadline. Bytes идут из файла в HTTP напрямую, без JS и без полной загрузки blob в память. Structured API errors остаются JSON; стандартные HTTP range/precondition errors могут иметь text body.
## Ошибки

```json
{"error":{"code":"dependency_unavailable","message":"Authorization service is unavailable","retryable":true,"request_id":"opaque-id"}}
```

`401` — нет действительных credentials; `403` — нет права; `404` — ресурс не существует или недоступен; `409` — занятый чат/конфликт состояния или версии; `422` — неверный контракт; `429` — квота с `Retry-After`; `503` — зависимость не позволяет принять запрос; `504` — ограниченное ожидание dependency. Ответы не включают токены, upstream bodies и stack traces.

Ошибка HTTP после commit может скрыть успешную операцию. Клиент повторяет только с исходным idempotency key, а не создаёт новое поручение.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
## Подготовка восстановленного запуска

`POST /v1/admin/runs/{id}/recovery/prepare` требует `execution.recovery_mode: true`, `operations.reconcile` и `Idempotency-Key`. Body: `expected_fence` из admin diagnostics и `evidence` с `kind: operator_attestation`, непустыми `reference` и `reason`. См. [контракт recovery](../recovery-mode.md).

Ответ содержит `run_id`, `status`, `reason`, новый `fence`, `unresolved_steps`, `spent_microcredits` и `reserved_microcredits`. Запуск переходит из Running в reconciling; pending/unknown шаги, записи внешних эффектов и финансовые резервы сохраняются. Подготовка не делает inference и не ставит job в очередь. Diagnostics run теперь содержит `fence` для условной команды оператора.

403 — нет права; 404 — запуск вне организации; 409 `lease_busy`, `recovery_fence_changed`, `run_not_running`, `run_not_uncertain`, `idempotency_conflict` или `recovery_mode_required`; 422 `invalid_recovery_evidence` — неверное основание/expected_fence. Совпадающий повтор с тем же ключом возвращает текущую квитанцию без нового перехода или аудита. После подготовки можно отменить запуск штатной owner-ручкой и отдельно сверить подтверждённый provider receipt.

Файлы временные: по умолчанию 24 часа, deployment `artifacts.ttl_seconds`. Метаданные upload/import содержат `expires_at` и, для service imports, `source_service_id`/`source_external_id`. Истёкший файл недоступен до физической очистки. Для повторного получения используйте `modules.artifacts.fetch({module,method,external_id})`: авторизованный сервис возвращает blob descriptor, Loom потоково импортирует его и выдаёт новый временный `artifact_id`. См. [контракт файлов](../file-tools.md#temporary-files-and-external-ids). Service-backed metadata также содержит `source_module`, `source_method`, `source_fetch_method` и стабильный `reference_id`. JS `modules.artifacts.file(ref)` автоматически обновляет внутренний ID по этому контракту; model attachments могут продолжить тот же run после TTL. HTTP content и обычное создание HTTP handle проверяют TTL физического файла. Прямого arbitrary-URL download API нет.

Caller restart: missing current caller registration returns HTTP 503, `error.code=caller_binding_unavailable`, `retryable=true` after credential/session verification. Definitive denials remain non-retryable; retain the original run/session and handle waiting_auth explicitly. [Contract and acceptance](../caller-recovery.md).
