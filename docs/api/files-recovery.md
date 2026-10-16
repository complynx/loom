# Files and recovery

[API index](../api.md)

## File upload and lifetime

Send raw bytes to `POST /v1/artifacts` with `artifacts.write` and the file's
`Content-Type`. This route does not accept a JSON descriptor or multipart form.
Success returns `201` with artifact metadata including `artifact_id`, MIME, size, hash and
`expires_at`. Use that ID in run input. Read metadata at `GET /v1/artifacts/{id}`
with `artifacts.read`.

Files are temporary: the default TTL is 24 hours, configured by
`artifacts.ttl_seconds`. Expired files are unavailable even before physical cleanup.
Service imports include source IDs and a stable `reference_id`; source module,
method and fetch method identify how to retrieve the file again.

For service-backed files, `modules.artifacts.fetch({module,method,external_id})`
requests a descriptor from an authorized service and imports fresh bytes as a new
temporary artifact. `modules.artifacts.file(ref)` refreshes its internal file ID
through that contract, so model attachments can continue in the same run after TTL.
HTTP content and new HTTP handles still require an unexpired physical file.
There is no arbitrary-URL download API. See [File tools](../file-tools.md#temporary-files-and-external-ids).

## Partial blob reads

Use `GET /v1/artifacts/{id}/content` or
`GET /v1/artifact-handles/{id}/content` with `artifacts.read`. To create a handle,
send `POST /v1/runs/{id}/artifacts/handles` with `{artifact_id}`. A handle expires
within 15 minutes, no later than its file; it is not a bearer credential.

Both content routes support `Range: bytes=offset-end`, open-ended, suffix and
multipart ranges. Full reads return `200`; single ranges return `206` with
`Content-Range`; multipart responses use `multipart/byteranges`. `HEAD` returns
headers without a body and ignores `Range`/`If-Range`.

The quoted `ETag` is the SHA-256 hash of the original bytes. Resume with `If-Range`,
checking status before appending: a mismatch returns the full `200` body.
Matching `If-None-Match` returns `304`; failed `If-Match` returns `412`; invalid or
unsatisfiable ranges can return `416`. These standard HTTP responses may have
plain-text bodies rather than the JSON API error shape.

Ownership, permissions, file expiry and handle TTL are checked before conditional
or range processing. A foreign or expired handle cannot reveal size through `304`.
Transfers stream directly from storage and use cancellation, periodic authorization
checks and a 30-second write deadline. MIME is preserved.

## Errors

```json
{"error":{"code":"dependency_unavailable","message":"Authorization service is unavailable","retryable":true,"request_id":"opaque-id"}}
```

| Status | Meaning |
| --- | --- |
| 401 | Credentials absent or invalid |
| 403 | Required permission or organization access missing |
| 404 | Resource absent or unavailable to the caller |
| 409 | State/version conflict or busy chat |
| 413 | Request or artifact exceeds its size limit |
| 415 | JSON endpoint received an unsupported content type |
| 422 | Invalid request contract |
| 429 | Configured quota or active-run limit; a `Retry-After` header is not guaranteed |
| 500 | Unexpected internal failure |
| 503 | Dependency unavailable, or recovery mode restricts the route |

Use `error.code` and `retryable` as well as status. Not every 503 is retryable:
recovery-mode restrictions need operator action. Responses exclude credentials,
raw upstream bodies and stack traces. An HTTP failure after commit can hide a
successful command: retry only with the original idempotency key/body.

A missing current caller registration returns `503 caller_binding_unavailable`
after credential/session checks. Preserve the original run/session and handle
`waiting_auth` explicitly. See [Caller recovery](../caller-recovery.md).

## Prepare a restored run

Stop all execution workers before preparing restored uncertain work. The API must
run with `execution.recovery_mode: true`; the caller needs `operations.reconcile`
and access to the run's organization. Send
`POST /v1/admin/runs/{id}/recovery/prepare` with `Idempotency-Key`:

```json
{
  "expected_fence": 3,
  "evidence": {
    "kind": "operator_attestation",
    "reference": "incident-record-123",
    "reason": "Workers stopped; restored run requires reconciliation"
  }
}
```

Use the run's current `fence` from admin diagnostics, not the example value.
`expected_fence` must be positive; `reference` and `reason` must be nonempty
(maximum 2048 and 1024 bytes respectively).

Success returns `run_id`, `status`, `reason`, new `fence`, `unresolved_steps`,
`spent_microcredits` and `reserved_microcredits`. An expired-lease, uncertain
`running` run becomes `reconciling`. Unknown/pending steps, external-effect records
and credit reservations remain intact. Preparation does not call inference or queue
execution. A matching retry returns the current receipt without another transition
or audit entry.

Conflicts include `lease_busy`, `recovery_fence_changed`, `run_not_running`,
`run_not_uncertain`, `idempotency_conflict` and `recovery_mode_required` (409).
Invalid evidence/fence returns `422 invalid_recovery_evidence`. A foreign run is
unavailable (404); missing rights return 403. After preparation, use the normal
owner cancellation route and reconcile confirmed provider receipts separately.
Follow the complete [recovery procedure](../recovery-mode.md) before enabling workers.
