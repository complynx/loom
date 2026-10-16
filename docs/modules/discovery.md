# Discovery

[Guide index](../modules.md)

## Регистрация одной строкой

```yaml
labels:
  - "clx.harness.service=http://booking-service:8080/internal/agent/manifest"
```

Наличие label означает opt-in. Остальная конфигурация находится в manifest. Initial discovery и polling — принятая архитектура. Docker event stream не требуется. Ограниченный proxy предоставляет только необходимые list/inspect.

URI допускает HTTP внутри выделенной доверенной сети, HTTPS — при проверке сертификата. Host должен соответствовать контейнеру или его alias в общей разрешённой Docker-сети. Не допускаются userinfo, fragment, query, внешние host и redirects. IP назначения выводится из актуальных Docker metadata; одного DNS resolution недостаточно. Повторная проверка назначения защищает от DNS rebinding. Arbitrary URL из ответа модели не является endpoint модуля.

Deployment operator является источником доверия: тот, кто может менять labels/manifest зарегистрированного сервиса, управляет его интеграцией. Доступ к Docker API — через ограниченный proxy, в MVP только необходимые list/inspect. `docker.sock:ro` не является запретом API-операций.

Доверенный manifest имеет `protocol_version`, `service_id`, `revision`, описание, caller registration и модули. [JSON Schema](../../schemas/manifest.schema.json) проверяет форму; семантический валидатор проверяет уникальность ID, пути, роли и capabilities. Неизвестная версия отклоняется. Неизвестные поля v1 отклоняются.

Реплики одного service_id допускаются только с одинаковыми caller binding и manifest revision/content hash. При несовпадении сервис переводится в conflict и новые вызовы блокируются. Начатые вызовы привязаны к конкретной реплике. Revision — непрозрачная строка; одного её равенства без сравнения hash недостаточно.

Manifest загружается с условными ETag-запросами и атомарно заменяется после проверки. В deployment-примере `discovery.poll_seconds: 300` и `freshness_ttl_seconds: 3600`. Обновления не мгновенны: следующий успешный опрос обнаруживает изменения. При ошибке старая версия остаётся для диагностики; использовать её можно только до TTL и пока контейнер зарегистрирован. Удаление контейнера исключает адрес после обновления регистрации; при недоступном discovery действует freshness TTL. Записи и reconciliation живут дольше registry entries.
## Caller registration

`caller` может разрешить сервису `runs.create`, `chat.read`, `chat.events.append`, `artifacts.read`, `artifacts.write`. Эти права не дают пользовательскую делегацию сами по себе. Caller identity связывается с Zitadel issuer/subject/audience; запрос предъявляет проверенный access token. Schema также резервирует `secret_ref`, но runtime-аутентификация входящим opaque secret пока не поддерживается. Не используйте этот вариант для действующей интеграции; manifest не содержит секреты.
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
