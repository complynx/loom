# History events

[Guide index](../api.md)

## История и поиск

Поиск: `mode=literal|fulltext|regex|semantic`; пустой mode означает literal. Для текстового поиска нужен непустой `query`. Фильтры: `before_seq`, `after_seq`, `source_service_id`, `event_types`, `limit`; `case_sensitive` управляет literal/regex. Fulltext использует PostgreSQL `simple`. Regex использует Go RE2 без lookaround/backreferences; неверный шаблон — `422 invalid_pattern`.

Deployment-пример: query до 256 Unicode characters, до 50 результатов, scan до 1 MiB и deadline 500 ms. `max_bytes` ограничивает выдачу; по умолчанию 64 KiB, `snippet_chars` — 500. Ответ содержит `events`, `partial`, `reason`, `next_cursor`, `truncated_seqs` и `skipped_seqs`. Продолжайте event pagination через `after_seq=next_cursor`. Слишком большое для scan событие явно отмечается в `skipped_seqs`, а cursor продвигается; это не доказательство отсутствия совпадения. Для полного текста используйте read. Partial result не равен полному отсутствию данных.

`POST /chats/{id}/history/read` принимает `{after_seq,before_seq,limit,max_bytes,snippet_chars}` для event pages. `{event_seq,offset_chars,max_bytes,snippet_chars}` читает текст одного события; `text_partial` и `next_text_offset` задают продолжение Unicode-текста. Оригинальные события сохраняются.

При `search.semantic_enabled: true` тот же search endpoint принимает `mode: "semantic"`, `embedding_model`, `embedding`, `offset`, `limit` и фильтры. Следуйте `next_offset`; векторы должны иметь ту же модель и размерность, что индекс. Indexing endpoint принимает `{seq,model,vector}`. pgvector требуется в database harness. Это exact cosine ranking с авторизационными фильтрами, без embeddings provider и ANN index. Disabled режим скрывает semantic tool и indexing route, запрос semantic возвращает отказ.
## Сессии и версии

`POST /auth/sessions` принимает JSON `{}` и возвращает 201 с session receipt. `POST /auth/sessions/{id}/reauthorize` принимает `{version}` и свежую авторизацию того же subject/actor/org; receipt 200. Это explicit reauthorization, а не OAuth refresh. Изменение settings, policy или preferences передаёт текущую `version` в body; settings принимает полный типизированный объект, а не JSON Merge Patch. `If-Match` не заменяет body version.

Billing adjustment принимает `{amount_microcredits,reason}` и `Idempotency-Key`; произвольная запись баланса через API не предусмотрена.
## SSE

Durable события имеют монотонный `seq` внутри run; `id` SSE равен этому номеру. Клиент использует `Last-Event-ID` или `after`, но не оба. Основные типы: `run.status`, `input.applied`, `question.created`, `approval.created`, `interaction.resolved`, `artifact.ready`, `message.completed`, `usage.updated`, `run.resumable`; provider recovery/retry добавляет свои durable события. Клиент должен сохранять cursor и терпимо обрабатывать неизвестный тип.

MVP доставляет сохранённые run events, а не поток token deltas провайдера. SSE читает bounded database batches. Запись в медленное соединение ограничена deadline; клиент после обрыва дочитывает durable события. Доставка как минимум один раз; дедупликация по seq.

Отсутствие подписчиков не отменяет run при `continue`. Политика `pause` действует после разрыва последнего stream и disconnect grace. Reconnect согласован с финальной проверкой pause; одноразовый HTTP async запрос не считается stream-разрывом.
