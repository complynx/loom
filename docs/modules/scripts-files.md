<a id="scripts-files"></a>

# Scripts and files

[Guide index](../modules.md)

## JS API

The `script` model function runs JavaScript with permission-filtered remote read methods and builtins under `modules`. Use `await` for calls and return a JSON-compatible value:

```javascript
const [result, history] = await Promise.all([
  modules.fixture.read({query: "example"}),
  modules.history.search({mode: "literal", query: "example", limit: 10})
]);
return {result, history};
```

The fixture call requires the [acceptance service](../acceptance-service.md). In a business integration, replace it with a method from `modules.help.list` and read that method's card before calling it.

Remote reads use `modules.<module_id>.<method_id>(arguments)`. External writes and deletes use the durable `module_call` model function. They cannot run through JS. Personal [memory](../user-memory.md) supports writes in JS. No arbitrary npm/import, process, shell, direct filesystem/network access, Go objects or credentials are exposed to scripts.

Builtin names are reserved: `history`, `artifacts`, `memory`, `files`, `diagnostics` and `help`. Help and personal memory are available to authenticated users. History operations require `chat.read`; artifact operations require the applicable artifact and chat permissions. The `files` module also requires the file worker to be enabled. Diagnostics requires `diagnostics.read`. Runtime authorization is checked for each operation; a name in the script catalog does not grant access. Read builtin cards with `modules.help.read` for exact permissions and limits.

One JS runtime is owned by one Go goroutine. Go performs RPC work concurrently and returns results through the event loop. Workers have OS memory, CPU and PID limits plus a deadline. Pause/cancel blocks new broker RPCs, cancels host contexts and interrupts JS after bounded cleanup. A `finally` block is not a completion guarantee. A restarted worker has a fresh heap; durable operation records preserve external action outcomes.

## Artifacts

Service responses can include up to 16 artifact descriptors. Each descriptor contains `download_path` (an absolute path on the registered origin), `mime_type`, `size_bytes` and `sha256`. The harness downloads with the delegated bearer token, checks current authority, verifies size/hash/MIME and imports the bytes into its artifact store. The script receives temporary artifact IDs and metadata, not downloaded bytes. Arbitrary external URLs are not fetched.

A descriptor can also supply `external_id` and `fetch_method` for later exact-version refresh. The fetch method must be an authorized read taking `{external_id}`. A service-backed reference can refresh expired temporary storage only when the returned checksum, size and MIME still match. Changed content or revoked rights require a new authorized reference. See [File API and recovery](../api/files-recovery.md).

Use these entry points:

- `await modules.artifacts.fetch({module,method,external_id})` imports a service file and returns artifact metadata.
- `await modules.artifacts.open({artifact_id})` returns a temporary `handle_id` and expiry, without bytes.
- `modules.artifacts.file({module,method,external_id})` constructs a synchronous JS helper. Use `await f.handle()`, `await f.call(method,options)` and `f.toJSON()` for serializable references. Read `artifacts.file` help first.
- `artifact_attach({handle_id})` is a model function that attaches a supported image directly to the model. Format, model capability, current rights and disclosure policy are checked.

There is no `modules.artifacts.read` method. Use bounded methods from `modules.files` for text, document and media processing; inspect their help for supported formats and continuation fields. The client content route `/v1/artifact-handles/{id}/content` streams bytes after checking credentials, ownership and expiry.

Artifact metadata and the chat link are committed together. Imported files remain subject to TTL and storage quotas. Blob collection is bounded; quota is freed after unlink, directory sync and the collected marker are recorded. Service artifact policy does not override the user's broader external-disclosure policy.

## Script failures

Uncaught classified failures return model-safe JSON with `error` and `recovery`.
Module failures also identify `module` and `method`. Argument validation can
include bounded declared top-level `fields`; raw values and backend messages
are excluded. JavaScript failures can include a standard `exception` name.
The same classification survives the worker IPC boundary.

A rejected module promise can be caught in JavaScript:

```js
try {
  return await modules.example.read({limit: 10});
} catch (error) {
  return {error: error.error, recovery: error.recovery};
}
```

Codes include `script_syntax_error`, `script_error`, `script_too_large`,
`script_result_too_large`, `script_call_limit`, `script_parallel_limit`,
`invalid_arguments`, `permission_denied`, `method_unavailable`,
`module_result_too_large`, `invalid_module_result` and `tool_failed`.
A live-run script deadline can return `script_timeout`; run cancellation and
lease/auth suspensions retain their existing control flow. Unknown failures use
`tool_failed` without exposing private error text. Limits do not automatically
retry work. An error does not prove that earlier memory/skill writes or other
completed calls were rolled back. Inspect current state before repeating an
effect. External writes still require the durable `module_call` path.
