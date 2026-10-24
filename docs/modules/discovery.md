# Discovery

[Guide index](../modules.md)

## Register a service

Add this label to the service container, replacing the hostname and path with your service's values:

```yaml
labels:
  - "clx.harness.service=http://booking-service:8080/internal/agent/manifest"
```

The label opts the service into discovery. Its manifest defines the integration. The harness lists running labelled containers and inspects their metadata at startup and on each poll. It does not need a Docker event stream. Configure the restricted Docker proxy and trusted network as described in [Running the harness](../running.md).

HTTP is supported on the trusted Docker network. HTTPS uses normal certificate verification. The registered hostname must match the container name, alias or DNS name in an allowed network. Userinfo, fragments, queries, redirects and unrelated hosts are rejected. Connections use the IP from Docker metadata rather than DNS resolution. A URL supplied by a model cannot register a module.

The operator who controls labels and manifests controls service registration. Restrict the Docker API to the required GET list/inspect operations. A read-only mount of `docker.sock` does not restrict Docker API methods.

The manifest requires `protocol_version`, `service_id`, `revision`, `description` and `modules`; `caller` is optional. The [JSON Schema](../../schemas/manifest.schema.json) defines its shape. Runtime validation also checks identifiers, method limits, safe paths, schemas and confirmation rules. Unknown versions and unknown v1 fields are rejected. See [the example manifest](../../examples/manifest.json) for a complete document.

Use unique module IDs across services. Replicas of one `service_id` must publish byte-identical manifests, including revision and caller binding. Different hashes cause a conflict and remove that service from the active registry. The runtime keeps one endpoint per service; it does not provide load balancing or session affinity for durable operations. Replicas must share operation and confirmation state if later calls can reach another replica.

Each poll fetches the full manifest with GET; conditional ETag requests are not implemented. A valid document replaces the prior registration atomically. Example configuration uses `discovery.poll_seconds: 300` and `freshness_ttl_seconds: 3600`; these are example settings, not immediate update guarantees. A failed fetch can retain a previous registration until its freshness TTL expires, provided the endpoint is still registered. A successful container list removes missing endpoints. If Docker discovery is unavailable, existing entries expire by TTL. Durable operation records outlive registry entries.

## Caller registration

Caller registration authorizes delegation for a user. For machine-owned tasks,
configure a [service account](../api/service-accounts.md). An SA can call the
service methods allowed by its own rights without registering as a business user
in unrelated services.

`caller.permissions` can grant the registered service `runs.create`, `runs.control`, `chat.read`, `chat.events.append`, `artifacts.read` and `artifacts.write`. These are caller permissions, not user delegation. Incoming caller authentication uses a verified Zitadel access token whose issuer, subject and audience match the registration.

The schema also accepts `auth.type: "secret"` with `secret_ref`. Incoming opaque-secret authentication is not implemented; do not use that branch for an active integration. Never place secret values in a manifest.

## Static and dynamic modules

A static module declares `methods`. A dynamic module declares `discovery_path` and `cache_ttl_seconds` instead. After checking the module's permissions, the harness POSTs `{}` to the discovery path with the delegated bearer token. Return `{"methods":[...]}` with methods of the same shape as static methods, filtered for that user. Discovery cannot change caller registration, issue credentials or change the registered origin. It is bounded to 200 methods, a 256 KiB response and five seconds.

Dynamic cache keys include issuer, subject, organization, actor, policy revision, permission set, token hash, service ID, manifest hash and module ID. The manifest TTL is 0..300 seconds; the effective TTL is the minimum of that value and `discovery.dynamic_cache_ceiling_seconds`. Zero disables caching. The example ceiling is 60 seconds. Writes and artifact authorization perform fresh dynamic lookup; read calls still recheck current module and method permission categories. Failed discovery reports unavailability rather than an empty set of permissions.

Each method declares its ID, description, input/output schemas, permission categories, effect (`read`, `write` or `delete`), timeout, result bound, idempotency, confirmation and artifact policy. All permission categories listed at both module and method level are required. Writes also require a status endpoint and deduplication window; `cancel_path` is schema-supported but not invoked by the current broker. See [Calls and confirmation](calls-confirmation.md) for wire formats and recovery behavior.

The broker checks permissions before a call; the service must enforce its own authorization too. Service descriptions and results are untrusted data and cannot override harness instructions. Schemas are compiled locally; external `$ref` documents are not fetched. Schema depth, request complexity and response sizes are bounded. Endpoint paths must start with `/` and must not contain traversal, query/fragment components or a different origin.

`external_disclosure` controls whether imported method artifacts may be sent to a model. It does not override the user's broader disclosure policy or filter ordinary text results out of the method catalog. Model attachment paths separately check run settings, current settings and model capabilities. Do not return text that the delegated user is not authorized to disclose.
