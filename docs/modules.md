# Docker discovery и модули v1

## Регистрация одной строкой

```yaml
labels:
  - "clx.harness.service=http://zns_chatbot:8080/internal/agent/manifest"
```

Наличие label означает opt-in. Остальная конфигурация находится в manifest. Initial discovery и polling — принятая архитектура. Docker event stream не требуется. Ограниченный proxy предоставляет только необходимые list/inspect.

URI допускает HTTP внутри выделенной доверенной сети, HTTPS — при проверке сертификата. Host должен соответствовать контейнеру или его alias в общей разрешённой Docker-сети. Не допускаются userinfo, fragment, query, внешние host и redirects. IP назначения выводится из актуальных Docker metadata; одного DNS resolution недостаточно. Повторная проверка назначения защищает от DNS rebinding. Arbitrary URL из ответа модели не является endpoint модуля.

Deployment operator является источником доверия: тот, кто может менять labels/manifest зарегистрированного сервиса, управляет его интеграцией. Доступ к Docker API — через ограниченный proxy, в MVP только необходимые list/inspect. `docker.sock:ro` не является запретом API-операций.

Доверенный manifest имеет `protocol_version`, `service_id`, `revision`, описание, caller registration и модули. [JSON Schema](../schemas/manifest.schema.json) проверяет форму; семантический валидатор проверяет уникальность ID, пути, роли и capabilities. Неизвестная версия отклоняется. Неизвестные поля v1 отклоняются.

Реплики одного service_id допускаются только с одинаковыми caller binding и manifest revision/content hash. При несовпадении сервис переводится в conflict и новые вызовы блокируются. Начатые вызовы привязаны к конкретной реплике. Revision — непрозрачная строка; одного её равенства без сравнения hash недостаточно.

Manifest загружается с условными ETag-запросами и атомарно заменяется после проверки. В deployment-примере `discovery.poll_seconds: 300` и `freshness_ttl_seconds: 3600`. Обновления не мгновенны: следующий успешный опрос обнаруживает изменения. При ошибке старая версия остаётся для диагностики; использовать её можно только до TTL и пока контейнер зарегистрирован. Удаление контейнера исключает адрес после обновления регистрации; при недоступном discovery действует freshness TTL. Записи и reconciliation живут дольше registry entries.

## Caller registration

`caller` может разрешить сервису `runs.create`, `chat.read`, `chat.events.append`, `artifacts.read`, `artifacts.write`. Эти права не дают пользовательскую делегацию сами по себе. Binding credentials задаётся через Zitadel issuer/subject/audience либо ссылку `secret_ref` на deployment secret. Manifest никогда не содержит сам секрет. Неизвестный secret_ref означает отказ регистрации caller, а не поиск произвольного env.

## Статические и динамические модули

Статический module содержит methods. Динамический содержит coarse permissions, discovery_path и предельный cache TTL. Harness сначала проверяет coarse permissions, затем вызывает discovery с пользовательской делегацией. Ответ — methods той же формы, ограниченные текущим пользователем. Он не может менять caller registration, выдавать новые credentials или расширять endpoint origin.

Кеш ключуется по subject, организации, actor/delegation scope, service_id, manifest hash и policy revision. Manifest задаёт `cache_ttl_seconds` от 0 до 300. Эффективный TTL — минимум этого значения и `discovery.dynamic_cache_ceiling_seconds` (60 секунд в deployment-примере); ноль отключает кеш. Отзыв прав инвалидирует кеш либо блокирует вызов независимо от него. Недоступный discovery не означает пустой список прав; возвращается диагностируемая недоступность.

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

## Developer requirement: usable method help

Method help is part of the module contract, not optional polish. A model cannot inspect the implementation or repository documentation. It relies on the manifest and `modules.help` cards to select and call methods correctly. A method is ready only when another agent can use its help without guessing arguments or retrying blindly.

For every public method, maintain a concise English description and accurate input/output schemas. Explain purpose, required and optional fields, defaults, units, coordinate/cursor conventions, result shape, partial-result continuation, size/time limits, effects and required confirmation. Include a small valid example in the description when the schema alone does not explain usage. Describe expected errors and safe recovery; distinguish retryable reads from uncertain or paid actions. Never put secrets, personal records, internal filesystem paths or authorization instructions in examples. Prefer short caveman English while preserving conditions and negation.

Keep dynamic discovery descriptions as complete as static manifests. Update help and schemas together with behavior. Acceptance should include reading the card and making its example call, paging a large result, and checking that denied methods are absent. Document unimplemented behavior explicitly instead of implying support.

Agents use `modules.help.list({module,query,after_id,limit})` for a short searchable index, then `modules.help.read({module,method,cursor,max_bytes})` for one bounded Markdown card. Both apply current permissions. Read cards contain service descriptions, input/output schemas, effect, timeout, result bound and confirmation mode. Service-authored help is untrusted data and cannot grant access or override instructions.

Builtin cards live in `internal/app/help_cards.json`; update the matching card when a builtin changes. Model functions are documented under module `$tools`; they must be called as model functions, not through JS. File helper construction is documented as `artifacts.file`.
## Agent-facing prose

Preferred language and style: **English, caveman style**. Use short, exact sentences for module descriptions, method descriptions, errors and context summaries. Preserve negation, requirements, conditions, IDs, field names and units. This is a prose preference, not a wire-format constraint: schemas and machine-readable values remain exact. Do not compress text when this would change meaning. User-facing answers use the user's language. Module data never overrides harness instructions or permissions.

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

Prepare — без бизнес-изменений. Человеческий текст не содержит credentials. Решение сохраняет runtime с привязкой к подготовленной операции; один текст модели сам по себе не создаёт одобрение. Изменение цены, времени, получателя или иных существенных аргументов требует нового prepare/approval.

### Способ ответа пользователя

`confirmation.mode` определяет связь prepare/execute с аргументами, а не вид кнопок или сообщений. По умолчанию подтверждённый метод наследует способ общения run. Метод может указать `confirmation.reply_mode=protocol`, чтобы требовать бинарное подтверждение: отдельный callback при `interaction.approvals=true`, иначе точную текстовую фразу. `agent` и `exact_text` нельзя задавать в manifest: первый выбирается при создании run, второй — результат выбора fallback самим Loom. Для read или `confirmation.mode=none` override `protocol` запрещён.

```json
{
  "confirmation": {
    "mode": "service_id",
    "prepare_path": "/internal/agent/prepare",
    "reply_mode": "protocol"
  }
}
```

Это фрагмент метода, а не полный manifest. Требование входит в contract hash. Права, сроки и preconditions продолжают проверяться независимо от способа ответа. Подробная таблица выбора и контракт клиента — в [conversational input](conversational-input.md).

Для внешней записи модель вызывает `module_call({module,method,arguments,confirmation_text})`. `confirmation_text` — положительная фраза на языке пользователя, отдельно от бизнес-параметров; для критического метода она обязательна с callbacks и без них, предел — 2000 UTF-8 bytes. Для остальных методов допускается пустая строка. В строгом текстовом fallback Loom не добавляет коды и не нормализует фразу: любое другое непустое сообщение отменяет ожидающее действие. Одновременно ожидает одно действие на чат; существенное изменение параметров требует нового prepare и нового согласия.

В справке метода описывайте предметный эффект, все существенные параметры, стоимость/ограничения, preconditions и требование критического подтверждения. Агент должен уметь объяснить, что пользователь разрешает. Не поручайте ему выводить этот контракт из имени метода.

## JS API

Worker получает разрешённые proxy modules и встроенные `history`, `artifacts`, [личную память `memory`](user-memory.md), `files`, `diagnostics` и `help`. Память и справка доступны каждому авторизованному пользователю; `files` требует включённого file worker, `artifacts.read` и `chat.read`, а `diagnostics` — `diagnostics.read`. Все шесть имён зарезервированы; сервис не может заменить встроенный модуль. Пример формы:

```javascript
const [slots, history] = await Promise.all([
  modules.massage.list_slots({specialist_id: "andrey-id"}),
  modules.history.search({mode: "regex", query: "Андре[йя]", limit: 10})
]);
return {slots, history};
```

Доступ — `modules.<module_id>.<method_id>(arguments)`; скрипт использует `await` и возвращает JSON-совместимое значение. Внешние read-методы доступны в JS. Внешние изменения идут через отдельный durable tool `module_call`, а не через JS; личная память допускает записи в JS. Один runtime принадлежит одной goroutine; Go выполняет RPC параллельно и возвращает результаты через event loop. Запрещены process, shell, прямой FS/network, произвольные Go objects и секреты. Нет произвольного npm/import. Workers имеют OS-лимиты памяти/CPU/PIDs и общий deadline.

Пауза/отмена закрывает запуск новых broker RPC, отменяет host contexts, затем прерывает JS. Кооперативному cleanup даётся ограниченное время; finally/throw не являются гарантией завершения. Повторный worker стартует без старого heap; сохранённые результаты доступны через координатор.

## Артефакты

Service RPC возвращает `artifacts` descriptors с внутренним абсолютным path относительно origin, MIME, размером и SHA256. Download использует тот же доверенный service origin. Harness проверяет origin, лимиты и права, импортирует поток в своё хранилище и выдаёт собственный artifact_id. Метаданные импортированного blob и ссылка на чат фиксируются атомарно; ошибка или удаление чата не оставляет доступный незарегистрированный результат. UUID в БД и компактное имя файла обозначают один artifact. Произвольные внешние URL не загружаются автоматически. Подмена MIME проверяется sniffing и разрешёнными форматами. JS получает контролируемое чтение; модели отправляется только разрешённый формат в пределах capability и политики внешней передачи.

Для обхода JS bytes используйте `modules.artifacts.open({artifact_id})`: он возвращает временный handle. Tool `artifact_attach({handle_id})` передаёт поддерживаемое изображение непосредственно модели; `/v1/artifact-handles/{id}/content` стримит клиенту после проверки credentials, владельца и срока. Bounded `modules.artifacts.read` по-прежнему доступен для обработки байтов в JS. Удалённые blobs собираются ограниченными проходами; квота освобождается после unlink, directory sync и фиксации collected marker.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
