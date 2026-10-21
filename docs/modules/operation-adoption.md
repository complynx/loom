# Adopting an existing preparation

[Module guide](../modules.md) · [Calls and confirmation](calls-confirmation.md)

Use adoption when a native client already owns a backend preparation and the
user wants to continue through Loom. Adoption reads that preparation. It never
calls prepare with the same arguments to manufacture a replacement.

## Capability and request

A service advertises its optional read-only inspector in the HTTP response to
the manifest GET:

```http
Loom-Operation-Inspect-Path: /internal/agent/operations/inspect
```

The value must be an absolute path on the registered service origin. The
manifest JSON and its strict schema stay unchanged. Older Loom clients ignore
the header and can read the same manifest. Adding or removing this capability
does not change an existing method's manifest hash or consent seal.

The action catalog and method help expose `SupportsOperationAdoption`. Without
the header, a resume request returns `operation_adoption_unsupported`; there is
no prepare fallback. A service can reject inspection for an unsupported method.
Do not advertise the header until the authenticated inspector is implemented.

The model calls:

```js
module_call({
  module: "massage",
  method: "book_slot",
  arguments: {},
  confirmation_text: "Confirm the original booking",
  resume_operation_id: "original-backend-operation-id"
})
```

The backend ID is opaque and limited to 256 UTF-8 bytes. Its shape or possession
does not prove ownership. Empty arguments request the original authoritative
terms. If arguments are supplied, they must match those terms exactly; adoption
cannot change them. Read-only inspection, fresh authority and normal consent
checks still apply. This is a model function, not a JavaScript write proxy.

Loom allocates its own operation ID and stores `backend_operation_id` separately
in the sealed request. Approval exposes both IDs. Execute, idempotency and status
always use the original backend ID. A retry of the saved call keeps both IDs;
changing the requested backend ID under that call fails the binding check.

## Inspector contract

Loom sends POST with its normal delegated bearer credentials:

```json
{
  "protocol_version": 1,
  "operation_id": "original-backend-operation-id",
  "module": "massage",
  "method": "book_slot"
}
```

The service must check current permissions and original operation ownership
before returning any terms or receipt. It must verify the original issuer,
subject, organization and actor/service delegation. It must not copy identity
from untrusted request parameters. Inspection must not prepare, execute, renew
expiry or change the stored operation.

Example response:

```json
{
  "protocol_version": 1,
  "status": "prepared",
  "operation_id": "original-backend-operation-id",
  "service_id": "booking-service",
  "module": "massage",
  "method": "book_slot",
  "contract_revision": "example-v1",
  "binding": {
    "issuer": "https://id.example.com",
    "subject": "user-subject",
    "actor": "caller-subject",
    "org_id": "organization-id"
  },
  "arguments": {"slot_id": "original-slot"},
  "description": "Original booking terms",
  "confirmation_id": "original-confirmation",
  "preconditions": {"version": 7},
  "expires_at": "2030-01-01T12:00:00Z"
}
```

`contract_revision` is the revision stored with the original native preparation
and must equal the current manifest revision. The service must change that
revision when a material contract, authority rule or precondition meaning
changes. Do not relabel an old preparation with the current revision. Loom also
seals its current full method/manifest hash and rechecks it at dispatch.

Loom checks the service, module, method, revision and authenticated scope. It
validates original arguments against the current input schema and preserves the
original description, confirmation token, preconditions and absolute expiry.
The usual confirmed-mode requirements and response limits apply. Never return
credentials in descriptions, arguments, preconditions or receipts.

## Consent, completion and uncertainty

A `prepared` result always needs fresh exact consent, including for a method
whose normal confirmation mode is `none`. Inspection is not consent. The
coordinator does not apply its default preparation TTL to an adopted expiry:
it neither renews nor shortens the original absolute expiry. An expired
preparation fails closed. Return the original expiry even if it is in the past.

For an already `succeeded` operation, include its existing `result` matching the
method output schema and the original preparation metadata. Loom returns that
receipt without another effect or approval. Original expiry can be in the past
for a completed operation. Native and Loom execute requests can race; the
service must atomically deduplicate both on the original backend ID and return
the same committed receipt.

`pending`/`accepted` or `unknown` remains status-only on the original ID.
Inspection transport failures also retain that ID as unknown. The runtime does
not create another preparation or automatically resend an uncertain effect.
If status cannot establish a terminal result, reconciliation remains blocked.
`not_found`, foreign scope, wrong service/method, changed revision or mismatched
terms cannot authorize adoption. Terminal `failed`/`cancelled` can be returned
with the original metadata. Raw HTTP failures never establish that an effect
is safe to repeat.

## Rollout and validation

Upgrade all Loom workers before enabling this capability in a service. Mixed
workers with and without adoption support are unsupported once adopted intents
exist. Disable new adoption and drain/reconcile adopted intents before rolling
back to an older runtime. Existing intents without `backend_operation_id` keep
their original request encoding, seal and backend ID behavior.

Loom tests cover original scope and revision rejection, exact terms, expiry,
seal tampering, unsupported capability, completed receipts, unknown status-only
recovery and concurrent native/Loom requests with a single backend effect.
The engine test uses real PostgreSQL and fixture identity/provider/module
peers. Actual native-client, backend and IAM acceptance requires the service to
implement this inspector and verify the complete handoff. This page does not
claim that a service implements the capability before it advertises it.
