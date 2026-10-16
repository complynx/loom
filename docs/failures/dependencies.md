# Dependencies

[Guide index](../failures.md)

## Матрица зависимостей

| Отказ | Поведение | Восстановление / что видит клиент |
|---|---|---|
| API недоступен, процесс/host умер | Client connection обрывается; durable run остаётся | Повтор с исходным key либо GET run; expired lease запускает recovery |
| Traefik/DNS/TLS недоступны | Клиент не достигает API | Не создавать новое поручение с новым key; восстановить маршрут, дочитать SSE |
| БД недоступна до принятия run | Не принимать и не отправлять действий | `503`; повтор с тем же key после восстановления |
| DB commit прошёл, ответ потерян | Клиенту исход неизвестен | Idempotency lookup возвращает исходный run |
| БД потеряна во время inference | Остановить новые действия; попытаться отменить текущее | Durable intent определяет recovery; результат/usage могут стать unknown |
| БД потеряна после внешнего write | Не сообщать сохранённый успех без commit | После восстановления status lookup по сохранённому operation_id |
| DB failover/долгая блокировка | Короткие statement/lock timeouts; backpressure | Retry транзакции с тем же intent; повтор сети не следует из DB retry |
| PostgreSQL storage/full/read-only | Запись может отказать при readiness 200; нужны storage/SQL alerts | Освободить/расширить storage; восстановить write, проверить intents |
| Полная утрата БД | Нет надёжной истории владения и бюджета | Disaster recovery; новые write запрещены до сверки внешних операций |
| Provider timeout/обрыв stream | Прекратить generation, зафиксировать partial preview как незавершённый | Limited retry нового generation только после сохранения статуса/usage uncertainty |
| Provider 429/5xx | Backoff с jitter и Retry-After | Пауза `dependency_unavailable` после retry budget; никакой тихой смены платной модели |
| Provider credentials/model rejected | Не повторять заведомо неверный запрос | Pause; исправление admin либо разрешённая пользователем смена модели |
| Provider завершил, usage потерян | Результат и стоимость учитываются раздельно | Сохранить результат при наличии; uncertain reserve до lookup/adjustment |
| Provider недоступен для compaction | Старая валидная summary допустима в бюджете | Иначе `context_unavailable`; не терять ограничения молчаливым truncation |
| Zitadel/JWKS недоступен | Известные cached keys применимы до своего TTL; неизвестный kid не принимается | Opaque/introspection и истёкшая авторизация блокируются; `503`/pause |
| Токен истёк | Следующее защищённое действие блокируется; автоматического refresh нет | waiting_auth; explicit reauthorize той же стороны |
| Refresh ответ потерян/не сохранён | Ротация uncertain, не повторять старый токен без гарантии issuer | waiting_auth; reauthorize той же identity |
| Пользователь/делегация отозваны | Запретить новые вызовы, закрыть подписки после проверки | Auth failure; подтверждение и старый кеш не обходят отзыв |
| Docker API недоступен | Registry не обновляется | Existing registrations до freshness TTL; затем fail closed, admin alert |
| Изменение Docker-регистрации между опросами | Изменение обнаруживается следующим poll | Polling и freshness TTL — штатный контракт |
| Manifest timeout/невалиден | Не подменять registry непроверенной версией | Старый valid manifest до TTL; после — недоступность сервиса |
| Manifest revision конфликтует между репликами | Новые вызовы блокируются | Исправить rollout; статус conflict, сохранить старые operation references |
| Сервис остановлен/redeploy | Новые RPC к удалённой реплике запрещены | Обновить registry; операции lookup по устойчивому ID на новом сервисе |
| Dynamic discovery недоступен | Не выдавать дополнительные методы | Valid scoped cache до TTL; затем явная недоступность |
| Read RPC timeout | Может не иметь бизнес-изменения, но быть платным | Retry только по контракту и бюджету; partial results сохраняются |
| Write RPC timeout/502/обрыв | Outcome unknown, даже при «ошибке» прокси | reconciling; lookup, затем тот же key только по гарантиям backend |
| Status endpoint недоступен | Unknown остаётся unknown | Пауза, bounded scheduled lookup; затем admin action |
| Status вернул неоднозначный not_found | Не считать отсутствием исполнения | Нужна гарантия backend либо ручная сверка |
| Approval/precondition истёк | Не исполнять старое подтверждение | Новый prepare и новый approval |
| JS бесконечный цикл/deadline | Broker прекращает новые RPC, runtime interrupt | Worker kill к deadline; completed RPC сохраняются, sent RPC сверяются |
| Worker OOM/crash | Скрипт потерян, координатор жив | Recovery по сохранённым operations; повтор JS только без blind writes |
| Worker не остановился | Кооперативный cleanup ограничен | Kill process group; run paused/cancelled, внешние операции отдельно |
| Blob store заполнен/недоступен | Не обещать durable файл, частичный upload скрыт | Artifact unavailable; повтор импорта по hash, garbage collection temporary blobs |
| Blob отсутствует/hash не совпал | Не передавать повреждённые данные | Ошибка artifact_corrupt; restore/повторный импорт из доверенного источника |
| Клиент ушёл | По policy continue или pause после grace | Run ID уже получен; SSE дочитывается по cursor |
| Медленный SSE клиент | Ограниченный буфер; отключить подписку | Повторное подключение и durable replay; не тормозить весь run |
| Клиент дважды отправил input | Deduplication на транзакции | Тот же ответ или conflict; одно применение |
| Два клиента запускают один чат | DB unique invariant | Один run принят, второй получает chat_busy |
| Credits исчерпаны/резерв uncertain | Не начинать следующий платный шаг | Pause budget_exhausted; пополнение/сверка, затем resume |
| Неверная admin configuration | Validate before commit; reject whole patch | 422; предыдущая revision сохраняется |
| Процесс потерял lease | Broker блокирует новые RPC старого owner | Новый owner сверяет начатые операции; late result относится к старому operation |
| Часы host смещены | Token/skew policy и UTC DB сроки | Alert clock drift; подозрительная авторизация не обходится увеличением leeway |
| Перегрузка CPU/памяти/API | Admission quotas, bounded queues | 429/503, Retry-After; принятый run остаётся durable |
