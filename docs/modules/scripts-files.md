# Scripts files

[Guide index](../modules.md)

## JS API

Worker получает разрешённые proxy modules и встроенные `history`, `artifacts`, [личную память `memory`](../user-memory.md), `files`, `diagnostics` и `help`. Память и справка доступны каждому авторизованному пользователю; `files` требует включённого file worker, `artifacts.read` и `chat.read`, а `diagnostics` — `diagnostics.read`. Все шесть имён зарезервированы; сервис не может заменить встроенный модуль. Пример формы:

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
