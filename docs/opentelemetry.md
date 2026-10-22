# OpenTelemetry

Loom can export traces and a small operation metric family through OTLP/HTTP. The private Prometheus `/metrics` endpoint remains the source for database, queue, lifecycle, credit, dependency, and HTTP aggregates. OTLP exports `loom.otel.operations` and `loom.otel.operation.duration`; it does not export those existing gauges a second time.

## Application configuration

```yaml
observability:
  otel:
    enabled: false
    endpoint: http://alloy:4318
    service_name: loom
    sample_ratio: 0.1
    timeout_ms: 1000
    export_interval_ms: 15000
    max_queue: 512
    max_batch: 64
```

The endpoint is a collector origin. The signal paths are explicitly configured as `/v1/traces` and `/v1/metrics`. Authentication credentials, endpoint paths, query strings, and fragments are not accepted. Enable only after the private collector receiver is available. A zero sampling ratio is valid and intentionally disables locally sampled root traces. A sampled inbound W3C parent remains sampled through parent-based sampling. Metric export does not depend on trace sampling.

## Data and correlation

HTTP spans contain registered route patterns, a bounded method, and status. Operation spans contain fixed component/outcome names. Raw URLs, queries, headers, bodies, filenames, user text, credentials, exception text, user IDs, and run IDs are not span attributes. JSON logs add `trace_id` and `span_id` when their context has a span; existing debug payload settings still govern diagnostic payload logs. W3C `traceparent` and `tracestate` propagate through explicitly wrapped outgoing transports. Baggage is not extracted or forwarded by the tracing wrapper.

Providers are scoped to each Loom observer. No global tracer, meter, propagator, or error handler is replaced. Run IDs remain available in normal logs for operational correlation.

## Durable run context

Run acceptance stores the current W3C span context in the same transaction as the run and its initial River delivery. That origin remains immutable. Each `run_execution` delivery starts under the last applied input context, or the origin if none is available. The ingress HTTP span can finish before delivery begins. Each delivery creates a distinct span; no span stays open for the entire conversation.

Input acceptance stores its own validated carrier with its durable input ID and a database acceptance sequence. Acceptance does not switch running work. In-flight provider/module requests finish under the context with which they started. The engine switches subsequent `execution_step` segments when it marks inputs applied, including steering, question answers, protocol approvals and exact/free-text replies. Provider and module HTTP calls inherit that segment's context through the existing transport wrappers. Segments cover turn preparation, input application and subsequent work; they are diagnostic boundaries, not additional business steps.

For a group, the newest valid input above the applied sequence watermark becomes the parent. Missing/invalid carriers retain the previous valid parent, then fall back to the origin or a new root. Each segment links the original run context, the previous segment and every valid carrier in its actual applied group, including the parent carrier. Duplicate span identities are linked once. Inputs that were only accepted are excluded. An applied group contains at most 64 inputs, below the SDK span-link limit. A larger pending set is fully processed in consecutive groups before model/tool dispatch; group segments link the previous group. Existing business input order is unchanged. Trace parent order uses database acceptance order, not client timestamps.

Application flags and their trace relationship commit together under the execution fence. Applied-input IDs, the sequence watermark and selected parent survive restart. Replaying an already applied ID changes neither the relationship nor its revision. Applying a delayed older ID links that input without reverting the newer parent. Idempotent acceptance replay retains its original input carrier. Resume and reauthorization do not replace the parent with a control request's trace.

The last-attempt context commits with a successful lease claim. A rejected claimant cannot replace it. Each step segment records its span context under that same fence before proceeding. Graceful restart and expired-executor recovery restore the applied parent and contributing IDs; recovered segments link the prior segment and origin. A killed process may never export its unfinished span, so a link can point to an absent backend span. Telemetry does not authorize recovery, reconcile unknown work or change run/task/operation IDs, idempotency, consent, leases, fencing or accounting.

Per-run storage contains `trace_origin`, `trace_attempt`, `trace_step` and `trace_applied`. The applied document is bounded to 8192 serialized bytes and contains the selected carrier, sequence/revision and at most 64 contributing input IDs. Each `run_inputs` row contains a carrier and a generated acceptance sequence. Each carrier contains only `traceparent` and optional `tracestate`, bounded to 4096 serialized bytes including JSON escaping. The writer accepts a normalized 55-byte traceparent and at most 512 bytes of tracestate. No baggage, arbitrary headers, SDK objects, credentials or user content is stored in this metadata. Fields are internal and are not part of run API documents. They follow their run/input retention and deletion.

Migration 00005 adds these columns with empty carrier defaults and assigns sequences to existing input rows without changing business processing order. Legacy, missing, oversized or malformed carriers do not block authorized work. Invalid optional tracestate is dropped; invalid links are omitted. Sampling configuration and parent-based sampling are unchanged. With tracing disabled, business execution continues without exported spans.

Older binaries can use the expanded schema and ignore these columns. Keep the columns for a binary rollback; old writers may leave diagnostic context absent or outdated. Startup never runs a down migration. An explicit schema downgrade removes diagnostic context only and requires stopping newer binaries first.

## Export failures

Trace export uses a bounded nonblocking batch queue; a full queue drops spans. Exporters use a bounded HTTP timeout, no redirects, and no automatic retry. Collector failures do not block business requests or change readiness. Scoped exporter adapters deliberately drop failed exports and partial rejections, emit a fixed structured warning, and increment `clx_otel_export_failures_total` with `signal="traces"` or `signal="metrics"` on the existing scrape endpoint. Collector response bodies and partial-success error messages are never passed to the SDK global error handler. This counter counts failed export requests, not individual lost spans or metric points. Telemetry is best effort and is not a durable audit trail. Shutdown flushes both providers concurrently within the existing process shutdown deadline. Unexported telemetry may be lost at the deadline.

## Collector integration

Configure a private OTLP HTTP receiver in your collector and select a trace backend with collector-side TLS/authentication. Keep port 4318 on a private network shared with Loom. Do not expose it through the public ingress. Collector configuration is maintained in the operator deployment repository.

Check that a request produces a route-pattern trace and correlated JSON log. Check OTLP operation metrics separately from the existing `clx_*` scrape metrics. Stop the collector temporarily and verify that API requests and process shutdown remain responsive. Set `enabled: false` and restart Loom to disable export.

## Dependencies and QA

Use the official Go SDK/exporters selected in go.mod. Retain notices and follow [dependency review](dependencies.md) when updating them. Verify trace/metric export, route privacy, inbound parent propagation, outgoing trace injection, log correlation, exporter outage and bounded shutdown using [QA](qa.md). For durable delivery, use the PostgreSQL setup in [QA setup](qa/setup.md), then run:

```sh
go test -race -tags=integration -run 'TestQueuedTrace|TestSteeringTrace|TestApprovalContinuation|TestOrphanQueueSurvivesSIGKILL' -count=1 -v ./internal/app
go test -run 'TestDurableTrace|TestTraceContext|TestAppliedTrace' -count=1 -v ./internal/telemetry ./internal/store
```

Process signal tests require Linux. These tests use an actual OTLP/HTTP receiver and PostgreSQL/River with synthetic HTTP identity/provider peers. They check delivery after ingress completes, SIGTERM process restart before input application, SIGKILL recovery after application, live A-to-B/C steering, all contributing input links, approval continuation, duplicate delivery and delayed older application. The approval test uses a consent/effect fixture to check engine receipts; full external-coordinator authorization/dedup checks remain in the integration suite. Recovery retains unknown provider exposure. Collector acceptance, remote backend visibility, consumer end-to-end acceptance and notification delivery are separate checks.
