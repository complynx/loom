# Недоступности, отказы и восстановление

## Реализованный срез

Этот документ задаёт целевой план v1. Фактическая поддержка MVP описана в `docs/running.md` и `internal/config/SUPPORT.md`. Провайдер не повторяется автоматически после неопределённого исхода; подтверждённый отказ приводит к паузе. Общий circuit breaker, OAuth refresh/token exchange и Docker event stream пока не реализованы. Discovery использует polling и freshness TTL. Повторы DB транзакций разрешены только для доказанно отменённых транзакций; они не повторяют сетевые действия.

River ограничивает число попыток задания. Исчерпавшее попытки задание без живого job или lease переводится sweep в `paused` для явного resume либо в `reconciling`, если результат или оплата неопределённы. Резервы сохраняются; автоматической бесконечной очереди повторов нет.

## Базовые правила

Этот план покрывает классы отказов v1. Цель — понятный статус, отсутствие слепых повторов изменений и восстановление из durable состояния. Он не обещает exactly-once сетевое исполнение или сохранность данных после утраты всех носителей.

1. `not_started`: есть доказательство, что действие не отправлено или backend гарантирует отсутствие текущего/будущего исполнения. Допустим ограниченный retry.
2. `succeeded/failed/cancelled`: есть окончательный результат backend. Сохраняем его, не повторяем выполненное действие.
3. `unknown`: действие могло произойти. Сначала status lookup; blind retry запрещён. При подтверждённом idempotency протоколе повторяется тот же operation ID/key, не новая операция.

Доступность не важнее корректности: без durable intent, проверки прав или резерва новое действие не отправляется. Отмена поручения не означает откат внешнего действия.

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
| PostgreSQL storage/full/read-only | Readiness false, mutations запрещены | Освободить/расширить storage; восстановить write, проверить intents |
| Полная утрата БД | Нет надёжной истории владения и бюджета | Disaster recovery; новые write запрещены до сверки внешних операций |
| Provider timeout/обрыв stream | Прекратить generation, зафиксировать partial preview как незавершённый | Limited retry нового generation только после сохранения статуса/usage uncertainty |
| Provider 429/5xx | Backoff с jitter и Retry-After | Пауза `dependency_unavailable` после retry budget; никакой тихой смены платной модели |
| Provider credentials/model rejected | Не повторять заведомо неверный запрос | Pause; исправление admin либо разрешённая пользователем смена модели |
| Provider завершил, usage потерян | Результат и стоимость учитываются раздельно | Сохранить результат при наличии; uncertain reserve до lookup/adjustment |
| Provider недоступен для compaction | Старая валидная summary допустима в бюджете | Иначе `context_unavailable`; не терять ограничения молчаливым truncation |
| Zitadel/JWKS недоступен | Известные cached keys применимы до своего TTL; неизвестный kid не принимается | Opaque/introspection и истёкшая авторизация блокируются; `503`/pause |
| Токен истёк | Refresh своей сессии до следующего действия | После успеха run продолжается; без refresh — waiting_auth |
| Refresh ответ потерян/не сохранён | Ротация uncertain, не повторять старый токен без гарантии issuer | waiting_auth; reauthorize той же identity |
| Пользователь/делегация отозваны | Запретить новые вызовы, закрыть подписки после проверки | Auth failure; подтверждение и старый кеш не обходят отзыв |
| Docker API недоступен | Registry не обновляется | Existing registrations до freshness TTL; затем fail closed, admin alert |
| Docker event stream потерян | Не полагаться только на events | Reconnect с backoff плюс полная сверка |
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

## Retry policy

Предлагаемый default для кратких dependency failures: не больше 3 попыток в общем deadline, exponential backoff с jitter, уважать Retry-After. 401/403, invalid schema, context overflow, истёкший approval не являются transient retry. Все попытки сохраняют operation identity; новые provider generations имеют новые IDs и отдельную стоимость.

Circuit breaker действует отдельно на provider/service, а не глобально на пользователя. После порога ошибок допускается ограниченный half-open probe. Discovery/status/auth запросы имеют отдельные небольшие connection pools, чтобы обычные RPC не вытеснили восстановление и shutdown.

Ручной reconciliation требует operation ID, доказательства backend результата, автора и причины. Admin не может без доказательства перевести unknown в not_started и разрешить повтор. Если доказательство получить невозможно, operation остаётся unresolved; дальнейшая задача может выполняться лишь без конфликтующего действия и с явным объяснением пользователю.

## Остановка

На SIGINT/SIGTERM:

1. Зафиксировать monotonic deadline `now + 5s`, убрать readiness, закрыть admission.
2. Поставить durable pause flags, заблокировать новые broker dispatch, отменить provider/host contexts.
3. Сохранить завершённые результаты, текущие причины и известные/unknown operation states короткими транзакциями с независимым shutdown context.
4. Отправить `run.resumable` подключённым клиентам, только если соответствующая запись сохранена. Клиент уже знает run_id из создания.
5. Дать workers краткий cleanup, затем kill; HTTP/SSE shutdown ограничен оставшимся временем. Завершить процесс до общего deadline.

Happy path 1 секунда — целевой измеряемый SLO, не доказанный факт. Hard bound 5 секунд относится к завершению процесса. При недоступной БД, зависшем kernel I/O или SIGKILL нельзя гарантировать финальный commit/доставку события; гарантией recovery остаётся заранее сохранённое намерение. Контейнерный stop timeout должен быть больше внутреннего срока, например 8 секунд. Перезапуск всего host может дополнительно остановить PostgreSQL — проверяется отдельно.

## Recovery после crash

Новый координатор получает expired lease, увеличивает fencing token и читает последний durable шаг. Running model generation помечается interrupted; usage — known либо uncertain. Отправленные write operations сначала переводятся в reconciliation. Не начатые intents можно выполнять; сохранённые результаты повторно не исполняются. Вопросы/approvals сохраняются до expiry, JS запускается заново только с известными результатами и запретом повторных write без сверки.

Pause восстанавливается как pause. Running/queued run после аварии автоматически восстанавливается, если действуют authorization session, бюджет и контракт; иначе получает соответствующее ожидание. Recovery не создаёт новое поручение и не меняет модель молча.

Скрипт с отправленными изменяющими RPC автоматически целиком не переигрывается. После сверки всех operations координатор передаёт агенту исходный код, известные результаты и факт прерывания; агент формирует продолжение. Повтор исходного скрипта допустим только для read-only исполнения либо при специально проверенном replay протоколе, которого в v1 нет. Это предотвращает дубли даже тогда, когда повторный скрипт мог бы создать новые operation IDs.

## Backups и disaster recovery

Предлагаемые цели: RPO не более 5 минут, RTO 60 минут при исправном резервном host/storage. Они требуют согласованного backup плана PostgreSQL с WAL, blobs, encryption key versions и deletion/revocation tombstones. Backup вне того же сервера обязателен для этих целей. При утрате ключа ciphertext не восстанавливается из одной БД; при компрометации ключа сессии отзываются и перевыпускаются.

После PITR нельзя сразу продолжать write: действия после точки восстановления могли уже произойти. В recovery режиме блокируются mutations, собираются операции/результаты из внешних сервисов, сверяются provider usage и ledger, затем оператор разрешает admission. Требуется долговременный журнал operation IDs во внешних сервисах. Без него абсолютная защита от дублей после утраты БД невозможна.

## Проверки отказов до релиза

- Обрыв до dispatch, после dispatch и после backend commit: одно бизнес-действие, известный/unknown исход без ложного успеха.
- DB down во время shutdown: процесс завершается в срок, recovery сверяет intent, клиент не получает ложный durable status.
- SIGKILL worker и coordinator, OOM JS, зависший host RPC: нет новых вызовов от потерявшего lease owner.
- Два run одного чата, параллельный refresh одной сессии, повтор input/approval: инварианты выдерживаются.
- Потерянный refresh ответ: waiting_auth, без цикла повторного использования старого токена.
- Provider usage loss/429: бюджет не списывается дважды и не освобождается без оснований.
- Discovery stale/конфликт, permission revoke: доступ закрывается согласно TTL/online policy.
- SSE disconnect/replay и pause policy: события восстанавливаются, preview не выдаётся за итог.
- Restore backup на изолированном стенде: ключи/blobs/ledger согласованы, write gated до сверки.
