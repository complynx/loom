# Recovery testing

[Guide index](../running.md)

## Восстановление

При SIGTERM сервис прекращает приём, отменяет inference/скрипты, сохраняет состояние и закрывает соединения в пределах пяти секунд. Клиент возвращается с прежним run ID. Неопределённый inference учитывается по [sponsored policy](../active-compaction.md); исключённые случаи сохраняют `reconciling` до сверки. Неизвестный write новый worker не повторяет вслепую. [Hybrid retry](../hybrid-retry.md) применяется отдельно к доказанным output-free отказам.

После исчерпания пяти попыток River задания startup/periodic sweep переводит run без живого job и lease в `paused` с причиной `queue_attempts_exhausted`. После восстановления зависимости клиент явно вызывает resume. При неопределённых результатах вместо этого требуется reconciliation; резерв сохраняется.

Write сверяется через status API модуля с прежним operation ID. Потерянную оплату inference сверяет оператор через `/v1/admin/runs/{id}/steps/{step}/reconcile`, с receipt или явной attestation, причиной и точной тарифицированной usage. После сверки resumable run становится paused. Резерв не освобождается по догадке; отменённые terminal run не возобновляются.

Удаление чата закрывает доступ и отменяет run. История и summaries очищаются согласно retention; финансовый аудит и необходимые recovery records сохраняются. Это контролируемое удаление, без обещания secure erase всех резервных копий и operational checkpoints. Retention новых ресурсов фиксируется при создании; обычный settings patch не сокращает срок уже существующих данных.

Для ротации шифрования сессий используйте [keyring и пакетную миграцию](../session-key-rotation.md). Удаление старого ключа из конфигурации до завершения миграции блокирует startup; ключи для исторических backup сохраняйте отдельно.
## Проверки

```sh
go test ./...
CLX_TEST_DATABASE_DSN='postgres://...' sh scripts/linux-tests.sh
go mod verify
```

Без тестового DSN SQL/integration проверки пропускаются. Для acceptance нужен отдельный PostgreSQL и Linux `-race` gate. Обычные тесты используют fixtures. Для live IAM/model и аварийных границ используйте [QA-руководство](../qa.md). Unit/integration fixtures не подтверждают эти внешние свойства.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
