# Docker discovery и модули v1

## Регистрация одной строкой

```yaml
labels:
  - "clx.harness.service=http://zns_chatbot:8080/internal/agent/manifest"
```

Наличие label означает opt-in. Остальная конфигурация находится в manifest. MVP выполняет initial discovery и polling; Docker event stream относится к следующему этапу, ограниченный proxy реализует только list/inspect. Целевой Docker provider также слушает events. После добавления event stream его потеря не должна навсегда оставлять registry неверным: polling остаётся обязательным.

URI допускает HTTP внутри выделенной доверенной сети, HTTPS — при проверке сертификата. Host должен соответствовать контейнеру или его alias в общей разрешённой Docker-сети. Не допускаются userinfo, fragment, query, внешние host и redirects. IP назначения выводится из актуальных Docker metadata; одного DNS resolution недостаточно. Повторная проверка назначения защищает от DNS rebinding. Arbitrary URL из ответа модели не является endpoint модуля.

Deployment operator является источником доверия: тот, кто может менять labels/manifest зарегистрированного сервиса, управляет его интеграцией. Доступ к Docker API — через ограниченный proxy, в MVP только необходимые list/inspect. `docker.sock:ro` не является запретом API-операций.

Доверенный manifest имеет `protocol_version`, `service_id`, `revision`, описание, caller registration и модули. [JSON Schema](../schemas/manifest.schema.json) проверяет форму; семантический валидатор проверяет уникальность ID, пути, роли и capabilities. Неизвестная версия отклоняется. Неизвестные поля v1 отклоняются.

Реплики одного service_id допускаются только с одинаковыми caller binding и manifest revision/content hash. При несовпадении сервис переводится в conflict и новые вызовы блокируются. Начатые вызовы привязаны к конкретной реплике. Revision — непрозрачная строка; одного её равенства без сравнения hash недостаточно.

Manifest загружается с условными ETag-запросами и атомарно заменяется после проверки. Предлагается poll 60 секунд с jitter и freshness TTL 5 минут. При ошибке старая версия остаётся для диагностики; использовать её можно только до TTL и пока контейнер зарегистрирован. Удаление контейнера исключает адрес немедленно. Записи и reconciliation живут дольше registry entries.

## Caller registration

`caller` может разрешить сервису `runs.create`, `chat.read`, `chat.events.append`, `artifacts.read`, `artifacts.write`. Эти права не дают пользовательскую делегацию сами по себе. Binding credentials задаётся через Zitadel issuer/subject/audience либо ссылку `secret_ref` на deployment secret. Manifest никогда не содержит сам секрет. Неизвестный secret_ref означает отказ регистрации caller, а не поиск произвольного env.

## Статические и динамические модули

Статический module содержит methods. Динамический содержит coarse permissions, discovery_path и предельный cache TTL. Harness сначала проверяет coarse permissions, затем вызывает discovery с пользовательской делегацией. Ответ — methods той же формы, ограниченные текущим пользователем. Он не может менять caller registration, выдавать новые credentials или расширять endpoint origin.

Кеш ключуется по subject, организации, actor/delegation scope, service_id, manifest hash и policy revision. Предлагаемый TTL — до 60 секунд. Отзыв прав инвалидирует кеш либо блокирует вызов независимо от него. Недоступный discovery не означает пустой список прав; возвращается диагностируемая недоступность.

Метод описывает:

- ID, описание, входную и выходную JSON Schema;
- категории пользовательских прав, все перечисленные категории обязательны;
- `effect=read|write|delete`;
- deadline и предел результата;
- idempotency, operation status и cancellation;
- протокол подтверждения;
- допустимые MIME types артефактов и возможность передачи модели.

Метод может быть видимым только после coarse/dynamic checks. Go broker и конечный сервис повторно проверяют доступ при исполнении. Данные и описания модуля не могут переопределять системные инструкции.

`external_disclosure` в manifest относится к артефактам метода и не отменяет общую пользовательскую политику передачи данных провайдеру. Обычные текстовые результаты также проходят общую политику; если передача запрещена, метод не предоставляется модели для этого run. Input/output schemas проверяются локально: внешние `$ref` не загружаются, глубина и размер schema ограничены. Относительные пути не допускают `//`, `..`, encoded traversal и смену origin.

## RPC

Все method endpoints — POST по пути относительно зарегистрированного origin, принимают envelope:

```json
{
  "protocol_version":1,
  "operation_id":"opaque-id",
  "run_id":"opaque-id",
  "arguments":{},
  "idempotency_key":"stable-operation-key",
  "confirmation":null
}
```

Credentials передаются broker в заголовках. Subject из body не заменяет credentials. `operation_id` устойчив при повторе и создан до отправки. Для изменяющих методов idempotency и status lookup по этому ID обязательны. Backend должен хранить deduplication и lookup не меньше срока допустимого повтора; по умолчанию окно автоматического восстановления — 24 часа, затем только lookup/ручная сверка. Истечение окна не разрешает новый blind retry.

Ответ: `status=succeeded|accepted|rejected`, operation_id, result/artifacts либо структурированный error. `accepted` означает durable владение операцией сервисом и требует status endpoint. Ошибки классифицируются как `not_started`, `terminal_rejection`, `unknown`; HTTP status сам по себе не доказывает отсутствие изменения. Только явно гарантированный `not_started` позволяет безопасный повтор без сверки.

Status endpoint принимает POST `{operation_id}` и возвращает `not_found|pending|succeeded|failed|cancelled`. `not_found` должен иметь документированную гарантию: прежний запрос уже не сможет позже выполнить действие. Без неё harness трактует ответ как unknown. Cancellation endpoint принимает operation_id и возвращает известный статус; отмена не обещает rollback.

## Подтверждения

Manifest поддерживает `confirmation.mode=none|service_id|exact_parameters`.

- `none`: метод можно выполнить только при пользовательском поручении и разрешённой политике; отсутствие протокола не расширяет права.
- `service_id`: prepare endpoint возвращает confirmation_id, нормализованное описание, expires_at и hash аргументов. Execute привязывает ID к subject, методу, аргументам и operation_id. Backend проверяет всё повторно.
- `exact_parameters`: prepare возвращает нормализованные аргументы, описание, expires_at и opaque precondition/version. Harness сохраняет hash детерминированного JSON утверждённых аргументов. Числовое представление сохраняется; совместимость с RFC 8785/JCS в MVP не заявляется. Execute отправляет подтверждённые аргументы и precondition; backend обязан атомарно проверить актуальность.

Prepare — без бизнес-изменений. Человеческий текст не содержит credentials. Одобрение хранится в отдельной записи и не является свободным сообщением модели. Изменение цены, времени, получателя или иных существенных аргументов требует нового prepare/approval.

## JS API

Worker получает разрешённые proxy modules и встроенные `history`, `artifacts`. Пример формы:

```javascript
const [slots, history] = await Promise.all([
  modules.massage.list_slots({specialist_id: "andrey-id"}),
  modules.history.search({mode: "regex", query: "Андре[йя]", limit: 10})
]);
return {slots, history};
```

Конкретный синтаксис module names и JS entrypoint фиксируется при runtime spike. Один runtime принадлежит одной goroutine; Go выполняет RPC параллельно и возвращает результаты через event loop. Запрещены process, shell, прямой FS/network, произвольные Go objects и секреты. Нет произвольного npm/import. Workers имеют OS-лимиты памяти/CPU/PIDs и общий deadline.

Пауза/отмена закрывает запуск новых broker RPC, отменяет host contexts, затем прерывает JS. Кооперативному cleanup даётся ограниченное время; finally/throw не являются гарантией завершения. Повторный worker стартует без старого heap; сохранённые результаты доступны через координатор.

## Артефакты

Service RPC возвращает descriptor с artifact_id либо внутренним относительным download path, MIME, размером и hash. Harness проверяет origin, лимиты и права, импортирует поток в своё хранилище и выдаёт собственный artifact_id. Произвольные внешние URL не загружаются автоматически. Подмена MIME проверяется sniffing и разрешёнными форматами. JS получает контролируемое чтение; модели отправляется только разрешённый формат в пределах capability и политики внешней передачи.
