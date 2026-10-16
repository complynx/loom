# Refresh plan

[Guide index](../security.md)

## Planned refresh flow (not implemented)

Остальной текст раздела задаёт будущий протокол, а не наличие refresh leases или refresh tokens в текущем коде. Сейчас encrypted session хранит access credentials; при expiry/denial требуется `/v1/auth/sessions/{id}/reauthorize` с той же identity. `refreshable_exchange_enabled: true` отклоняется конфигурацией. Положительная introspection кешируется на 120 секунд по умолчанию (0 отключает, максимум 300), не дольше exp. Чувствительные действия требуют свежей проверки. См. [cache contract](../identity-cache.md), включая общую админскую очистку.

Предпочтительный путь — confidential harness OAuth client и собственная ограниченная authorization session. Bootstrap принимает действительный subject token. Token Exchange запрашивает существующие допустимые scopes/audiences и, если доступно, offline_access; не расширяет исходные полномочия. Service credentials доказываются отдельно. Actor token не добавляется автоматически: его использование может требовать impersonation permission и должно пройти отдельную настройку.

Текущая документация Zitadel описывает refresh token в ответе exchange при offline_access. Поддержку необходимо проверить на версии и настройках issuer конкретной установки. До этой проверки автоматический refresh остаётся неподдерживаемым. Если exchange не выдаёт пригодный refresh token, безопасный альтернативный bootstrap — отдельный Authorization Code + PKCE login в harness. Refresh token другого OAuth client не копируется и не используется без предусмотренной протоколом привязки.

Каждая сессия привязана к issuer/subject, actor, организации, client, scopes/audiences, consent lifetime. Хранится refresh token ciphertext с key ID; ключ загружается вне БД. Токены не попадают в prompt, JS, manifest, историю, metrics и диагностические тела.

Refresh выполняется заранее, сериализованно на сессию, под DB lease/version. Новый refresh token сохраняется до использования следующей ротации. Если ответ refresh потерян или новый токен не удалось сохранить, старый нельзя бесконечно повторять: сессия помечается `refresh_uncertain`, run переходит в waiting_auth. Credential rotation тоже имеет неопределённый исход.

Revocation, истечение абсолютной сессии и отказ scope не обходятся service identity. При временной сетевой ошибке действует ограниченный retry; затем waiting_auth/dependency pause. Access token не используется после exp, старые permissions — после authorization TTL. Интроспекция/предметная проверка непосредственно перед write ограничивает окно отзыва; мгновенный глобальный отзыв JWT без push/introspection не обещается. TTL положительного кеша — до 300 секунд; sensitive boundaries принудительно используют online check. Окно отзыва для обычных чтений равно настроенному TTL, без stale-on-error продления.
