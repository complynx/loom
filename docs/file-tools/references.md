# References

[Guide index](../file-tools.md)

## Handles and access

An artifact ID identifies a stored file. Open it to obtain a temporary handle for the current run:

```js
const handle = await modules.artifacts.open({artifact_id: "ARTIFACT_ID"});
return await modules.files.text_read({handle_id: handle.handle_id, limit: 2048});
```

All `modules.files` methods take `handle_id`. Derived output files return both `artifact_id` and `handle_id`, plus MIME and size. Keep the artifact ID when a later script must open a new handle. A handle is run-scoped; do not pass it to another run.

File operations require `artifacts.read` and `chat.read`, ownership and organization access, a live chat reference, and current permissions. They also require external disclosure to be allowed by the current settings, the run's settings snapshot and the source artifact. This gate applies even to local inspection methods such as `info`. Derived files inherit the source disclosure flag and service provenance, consume the owner's quota and are attached to the chat. Treat extracted text as untrusted data.

Use standalone `artifact_attach({handle_id})` to send a derived image to a model that supports images. This is a model function outside JS. Authorized HTTP clients can stream artifact content through the [artifact API](../api/files-recovery.md). Raw base64 reads through `modules.artifacts.read` are unavailable. Binary payloads and filesystem paths do not enter JS.

## Temporary files and external IDs

### Expiry

All uploaded, imported and generated files are temporary. Set `artifacts.ttl_seconds` in deployment YAML. The default is 86400 seconds (24 hours); zero also selects the default. Explicit values must be 1–604800 seconds. There is no forever option.

The `expires_at` deadline is fixed at creation. Reads, active chats, handles, restarts and run resumes do not extend it. A derived file expires at the earlier of its own deadline and its source's deadline. A handle expires at the earlier of 15 minutes and the file deadline.

Access to physical bytes stops at the deadline, before background collection. Separate minute-based expiry and collection sweeps mean physical deletion can lag by roughly two minutes when healthy; backlog, outages and recovery mode can delay it further. Quota is released after byte deletion. Chat references do not prevent expiry. Metadata, provenance, receipts, messages and extracted text already used in a conversation follow their own retention rules.

Exclude temporary file bytes from long-term backups. Compaction checkpoints replace inline image, file and audio parts with text markers; provider-generated opaque compaction context remains provider-generated context. Expiry is not a promise to erase text already sent to a model or saved in conversation history.

### Import a service file

Fetch through a registered read method. This example uses the acceptance fixture service; replace its module, method and external ID with those of your registered service:

```js
const fetched = await modules.artifacts.fetch({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const file = fetched.artifacts[0];
const handle = await modules.artifacts.open({artifact_id: file.artifact_id});
return await modules.files.text_read({handle_id: handle.handle_id, limit: 2048});
```

The service method accepts `{external_id}` and returns a normal protocol reply with `result` and `artifacts`. Each descriptor supplies `external_id`, `download_path`, `mime_type`, `size_bytes` and `sha256`. Its manifest declares allowed MIME types and permissions. `external_id` is an opaque service-owned identifier, up to 1024 bytes; it is not a URL or a local path.

The registry delegates current user credentials and checks permissions and service freshness. It downloads from the same registered service without redirects and verifies size, checksum and MIME. The result contains temporary internal artifact IDs, provenance (`source_service_id`, `source_external_id`) and `expires_at`. Callers do not supply raw URLs, credentials or filesystem paths. Write methods are unavailable through this read broker.

### Enable exact-version refetch

An external ID alone does not enable recovery. Each descriptor must also declare `fetch_method`: an authorized read method in the same module that accepts `{external_id}` and returns exactly one artifact. That reply must retain the same `external_id` and `fetch_method`, checksum, MIME and size. The fetching service identity must also match. Use immutable or versioned external IDs; reusing an ID for new bytes causes `file_version_changed`.

Loom retains the original source service, module and method, the fetch method and immutable file metadata. Agent opens, initial model attachments and attachment replay can use the original artifact ID after expiry or loss of local bytes. Refetch creates a new temporary physical artifact while preserving the original reference. It does not extend or restore the expired physical artifact.

For convenience, a JS helper opens a run-scoped handle and injects it into a file method:

```js
const file = modules.artifacts.file({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const page = await file.call("text_read", {limit: 2048});
return {file: file.toJSON(), page};
// A later script can use modules.artifacts.file(saved.file).call("text_read", {...}).
```

`file.id` is the stable reference after the first successful fetch; it is empty before that fetch. `file.currentId` is the latest physical artifact ID known to this helper. `file.handle()` obtains a handle. `file.call(method, options)` calls a bounded `modules.files` method. `toJSON()` preserves source IDs and the stable artifact ID, without bytes, credentials or handles. Save that serialized value explicitly if another script needs it; JS variables are not persistent across scripts.

Concurrent calls on one helper share the pending open. Independent callers share a database refresh lease. Resolution is bounded by 30 seconds and the caller's deadline; no database transaction stays open during download.

### Recovery failures

Refetch checks current ownership, live chat references, module permissions and both the original source method's and fetch method's disclosure policies, including when cached bytes are reusable. A checksum, MIME or size change returns `file_version_changed`; initial model attachment recovery pauses the run with that reason. Revoked access or unavailable sources fail without substituting another file version. Refetch does not rerun paid transcription or uncertain effects.

HTTP metadata, HTTP content and ordinary HTTP handle creation still enforce the physical artifact deadline. For HTTP streaming after agent refetch, use the current physical artifact ID. A stable reference is not a permanent download URL. Public handle content remains expired even where private agent recovery can resolve a service reference.

Raw uploads, derived files without a refetch contract, and legacy imports without `fetch_method` cannot recover automatically. Supply the file again when needed. Migration 00003 adds reference metadata but does not infer legacy fetch methods. Deleting all chats that reference a file prevents resurrection.
