# Авторизация и границы доверия

Этот документ описывает действующие границы и отдельно целевой refreshable flow. Текущий MVP использует access-token sessions с explicit reauthorization; автоматический Token Exchange/refresh и deployment-bound secret authentication не реализованы. [PKCE panel login](operator-login.md) — отдельный действующий механизм.

## Identity и доступ

Внешний access token проверяется по ожидаемому issuer, signature/alg, audience, exp/nbf и обязательным claims. JWKS URL берётся из доверенного issuer discovery, не из jku заголовка JWT. ID token не принимается вместо access token. Opaque access token проверяется через introspection.

Harness различает пользователя (`subject`), вызывающий сервис (`actor`), организацию и плательщика. Credentials сервиса не разрешают произвольный user_id. В v1 сервисные run без пользователя не входят; позже возможен отдельный тип machine principal.

Для сервиса необходимы одновременно:

1. Актуальная Docker registration с caller binding.
2. Доказательство личности сервиса: проверенный Zitadel service access token. Deployment-bound secret остаётся будущим режимом и не принимается MVP.
3. Сервисное право на конкретную операцию.
4. Пользовательская делегация в допустимой области.
5. Пользовательские права, если операция затрагивает предметный API.

Manifest не может назначить права пользователю и не позволяет зарегистрировать caller с неизвестным issuer/audience. Admin API не может изменить trust roots, raw secret bindings или ключи шифрования.

Прямой пользовательский запрос проверяет собственные права и владение ресурсом. Для сервисного запроса к истории достаточно service `chat.read` и делегации соответствующего пользователя. Этот выбор намеренно позволяет чтение общего чата с событиями разных сервисов. `chat.events.append` не подразумевает чтение. Без `chat.read` создание run также запрещено: итог может использовать прежнюю историю. Такой сервис может только добавлять пассивные события.

Подписка SSE, результат run, вопросы и артефакты проверяют те же права чтения. Подписки периодически переавторизуются и закрываются при отзыве; последнее уже отправленное сообщение отозвать невозможно. ID ресурса не является разрешением на доступ.

## Права harness

| Право | Действие |
|---|---|
| `runs.create` | Запуск собственного поручения |
| `runs.control` | Steering, ввод, pause/resume/cancel в своей области |
| `chat.read` | Чтение истории, поиска и ответов |
| `chat.events.append` | Запись пассивных событий |
| `chat.delete` | Удаление своего чата |
| `artifacts.read/write` | Чтение/загрузка разрешённых артефактов |
| `preferences.self.write` | Выбор своей доступной модели/effort |
| `preferences.other.write` | Выбор модели другого пользователя в административной области |
| `users.policy.write` | Изменение лимитов и entitlements пользователя |
| `settings.read/write` | Чтение/изменение несекретных системных настроек |
| `billing.adjust` | Начисления и корректировки с причиной |
| `operations.reconcile` | Ручная сверка неизвестных результатов с доказательством |
| `diagnostics.read` | Организационная read-only диагностика и вход в панель |
| `chat.embeddings.write` | Индексация внешних векторов; также требуется `chat.read` |

Zitadel roles отображаются в эти права явной конфигурацией. Административные права проверяются для целевой организации; произвольного глобального доступа нет. Предметные категории модулей имеют namespace сервиса. Список категорий означает AND; более сложную логику выполняет сам backend.

[Личная память](user-memory.md) доступна каждому авторизованному пользователю без memory roles. Проверки владельца, организации, свежей авторизации и run lease сохраняются; это не cross-user admin доступ.

## Целевой refreshable flow — не реализован

Остальной текст раздела задаёт будущий протокол, а не наличие refresh leases или refresh tokens в текущем коде. Сейчас encrypted session хранит access credentials; при expiry/denial требуется `/v1/auth/sessions/{id}/reauthorize` с той же identity. `refreshable_exchange_enabled: true` отклоняется конфигурацией. Online introspection выполняется при каждой проверке; фактический permission cache TTL сейчас ноль.


Предпочтительный путь — confidential harness OAuth client и собственная ограниченная authorization session. Bootstrap принимает действительный subject token. Token Exchange запрашивает существующие допустимые scopes/audiences и, если доступно, offline_access; не расширяет исходные полномочия. Service credentials доказываются отдельно. Actor token не добавляется автоматически: его использование может требовать impersonation permission и должно пройти отдельную настройку.

Текущая документация Zitadel описывает refresh token в ответе exchange при offline_access. Это не доказательство поддержки на установленном v4.16.3. До интеграционной проверки непрерывная авторизация считается незакрытым release gate. Если exchange не выдаёт пригодный refresh token, безопасный альтернативный bootstrap — отдельный Authorization Code + PKCE login в harness. Refresh token другого OAuth client не копируется и не используется без предусмотренной протоколом привязки.

Каждая сессия привязана к issuer/subject, actor, организации, client, scopes/audiences, consent lifetime. Хранится refresh token ciphertext с key ID; ключ загружается вне БД. Токены не попадают в prompt, JS, manifest, историю, metrics и диагностические тела.

Refresh выполняется заранее, сериализованно на сессию, под DB lease/version. Новый refresh token сохраняется до использования следующей ротации. Если ответ refresh потерян или новый токен не удалось сохранить, старый нельзя бесконечно повторять: сессия помечается `refresh_uncertain`, run переходит в waiting_auth. Credential rotation тоже имеет неопределённый исход.

Revocation, истечение абсолютной сессии и отказ scope не обходятся service identity. При временной сетевой ошибке действует ограниченный retry; затем waiting_auth/dependency pause. Access token не используется после exp, старые permissions — после authorization TTL. Интроспекция/предметная проверка непосредственно перед write ограничивает окно отзыва; мгновенный глобальный отзыв JWT без push/introspection не обещается. Предлагаемый верхний TTL кеша прав — 30 секунд, ноль для изменяющих операций, если backend требует online check.

## Поручение и подтверждение

История UI-действий — наблюдение, не согласие. Поручение определяет цель, предметные права определяют доступ, approval разрешает конкретное подготовленное действие. Это три разных проверки.

Approval привязан к subject, actor/разрешённому каналу подтверждения, chat/run, методу, canonical argument hash, precondition, confirmation_id при наличии, сроку и operation_id. Только авторизованный input endpoint может сменить pending на approved/rejected. Модель не может создать факт одобрения своим текстом. Применение approval и создание execute intent связаны транзакцией; retry использует тот же operation_id.

После истечения, steering с изменением параметров, смены версии контракта или precondition conflict требуется новое подтверждение. Повтор одинакового решения идемпотентен, противоположное решение после принятия — conflict. Отзыв прав имеет приоритет над сохранённым approval.

## Скрипты, данные и внешняя передача

OS worker запускается non-root без сети и credentials. RPC проходит только через broker, который сверяет актуальную аренду run, права, бюджет, deadlines и разрешённый метод. Проверка отмены выполняется до отправки каждого RPC, не только при создании worker. Просроченная аренда не позволяет новый вызов; ранее отправленные операции учитываются как отдельные возможные side effects.

Tool results, manifests, файлы и история являются недоверенным содержимым для модели. Prompt-инструкция не является механизмом доступа: права обеспечиваются broker/API. Скрипт не может заменить subject, origin, artifact ACL или billing account.

Передача артефакта внешней модели требует одновременно чтения, разрешения external disclosure и поддержки MIME/capability. Отсутствие подходящей модели возвращает понятную ошибку, а не скрытую смену провайдера. Provider credentials принадлежат harness, а не пользователю. Логи содержат IDs, статусы и санитизированные ошибки; raw prompts/results по умолчанию не логируются вне истории.

## Источники

- [Zitadel Token Exchange](https://zitadel.com/docs/guides/integrate/token-exchange): ограничения scope/audience и refreshable обмена.
- [Zitadel OIDC endpoints](https://zitadel.com/docs/apis/openidoauth/endpoints): refresh grant и client authentication.
- [Zitadel roles](https://zitadel.com/docs/guides/integrate/retrieve-user-roles): роль claims зависит от scopes/settings.
- [Goja](https://github.com/dop251/goja): runtime и Interrupt не заменяют отмену host operations.

Документация upstream подтверждает общий механизм, не конфигурацию production.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
