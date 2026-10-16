# References

[Guide index](../file-tools.md)

## Handles and access

Use `modules.artifacts.open({artifact_id})` to obtain a handle for the current run. All `modules.files` methods take `handle_id`. Output files return `artifact_id`, temporary `handle_id`, MIME and size. No binary payload or filesystem path enters JS. Physical artifact content is valid only until `expires_at`; a later run may open a fresh handle while the file is live. Service-backed IDs can also serve as stable references through the recovery contract below. Owner, organization, run, handle expiry, live permissions and disclosure policy are checked. Derived files inherit the source disclosure flag and service provenance, consume the owner's quota and are referenced by the chat. File text is untrusted data.

Use `artifact_attach` to send a derived image to a model that supports images. Services can download artifact bytes through the existing authorized streaming artifact endpoint. Raw base64 reads through `modules.artifacts.read` are removed. Use bounded text/document tools for extracted text; image attachment and HTTP content streaming bypass JS.
## Temporary files and external IDs

All uploaded, imported, and generated files are temporary. Set `artifacts.ttl_seconds` in deployment YAML. Default: 86400 (24 hours); zero also uses the default. Valid explicit range: 1–604800 seconds. There is no forever option. The deadline is fixed at creation and is not extended by reads, active chats, handles, restarts, or run resumes. Derived files use the earlier of their own deadline and the source deadline. A handle expires at the earlier of 15 minutes and its file deadline.

Access is denied at the deadline, before background collection. A minute-based lifecycle sweep tombstones expired files, including files referenced by active runs. Collection deletes bytes and then releases quota. Sweeping and collection use separate minute ticks, so healthy physical removal can lag by roughly two minutes; backlog, database/disk outages, or recovery mode can delay it further. Metadata, provenance, financial receipts, chat messages, and extracted text already used in a conversation remain under their own policies. File bytes are excluded from long-term backups. Compaction checkpoints replace inline image/file/audio parts with text markers; migration 00002 also removes legacy inline attachments and legacy base64-read results. Opaque provider compaction context remains provider-generated context.

Fetch an external file through a registered read method:

```js
const fetched = await modules.artifacts.fetch({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const file = fetched.artifacts[0];
const handle = await modules.artifacts.open({artifact_id: file.artifact_id});
return await modules.files.text_read({handle_id: handle.handle_id, limit: 2048});
```

The service read method accepts `{external_id}`. Its normal protocol reply contains `result` plus `artifacts` descriptors with `external_id`, `download_path`, `mime_type`, `size_bytes`, and `sha256`. The registered method declares allowed MIME types and permissions in its manifest. The registry delegates current user credentials, checks dynamic permissions and service freshness, downloads from the same registered service without redirects, verifies size/checksum/MIME, and returns temporary internal IDs plus `source_service_id`, `source_external_id`, and `expires_at`. Raw URLs, user credentials and filesystem paths are not fetch arguments. Write methods cannot be called through this read broker.

External IDs and versions are owned by the source service. Use immutable/versioned IDs. A descriptor can declare `fetch_method`: an authorized read method in the same module accepting `{external_id}` and returning exactly one file. Loom retains its source service/module/method, external ID and immutable checksum, MIME and size as metadata. Agent file opens, initial model attachments and attachment replay can resolve the original internal ID after expiry or missing local bytes. The authorized service supplies a new temporary artifact; the original reference remains stable. HTTP metadata/content and ordinary HTTP handle creation still enforce the physical artifact deadline; use the current ID or fresh agent handle for streaming.

```js
const file = modules.artifacts.file({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const page = await file.call("text_read", {limit: 2048});
return {file: file.toJSON(), page};
// Later script: modules.artifacts.file(saved.file).call("text_read", {...})
```

`file.id` is the stable reference; `file.currentId` is the latest physical artifact. `file.handle()` obtains a run-scoped handle. `file.call(method, options)` injects that handle into a bounded `modules.files` call. Serialization contains source IDs and the stable artifact ID, without bytes, credentials or handles. Concurrent helper calls share the pending open; independent callers share a database refresh lease. Refresh leases last two seconds with a 500 ms heartbeat; resolution is bounded by 30 seconds and the calling script/request deadline. No database transaction stays open during download.

Current ownership, live chat references, module permissions and both source/fetch disclosure policies are checked, including cached reuse. A changed checksum, MIME or size returns `file_version_changed`; initial model attachment recovery pauses the run with that reason. Revoked access and unavailable sources fail safely. Expired handle metadata is retained for nonterminal runs solely for private checkpoint recovery; public handle content remains expired. Recovery does not rerun paid transcription or uncertain effects.

Raw uploads, derived files without a source refetch contract, and legacy imports without `fetch_method` cannot be recovered automatically. Supply them again when needed. Migration 00003 adds reference metadata; it does not guess legacy source methods. Deleting all chats referencing a file prevents resurrection. The acceptance service exposes `fixture.fetch_file` for `fixture-file` and `fixture-image`.
