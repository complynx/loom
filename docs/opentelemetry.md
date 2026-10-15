# OpenTelemetry

Loom can export traces and a small operation metric family through OTLP/HTTP. The existing Prometheus `/metrics` endpoint remains the source for database, queue, lifecycle, credit, dependency, and HTTP aggregates. OTLP exports `loom.otel.operations` and `loom.otel.operation.duration`; it does not export those existing gauges a second time.

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

HTTP spans contain registered route patterns, a bounded method, and status. Operation spans contain fixed component/outcome names. Raw URLs, queries, headers, bodies, filenames, user text, credentials, exception text, user IDs, and run IDs are not span attributes. JSON logs add `trace_id` and `span_id` when their context has a span; existing debug payload settings still govern diagnostic payload logs. W3C `traceparent` and `tracestate` propagate through explicitly wrapped outgoing transports. Baggage is not extracted or forwarded by the tracing wrapper.

Providers are scoped to each Loom observer. No global tracer, meter, propagator, or error handler is replaced. Async queue work starts a fresh run trace; trace parents are not persisted in PostgreSQL across queue restarts. Run IDs remain available in normal logs for operational correlation.

Trace export uses a bounded nonblocking batch queue; a full queue drops spans. Exporters use a bounded HTTP timeout, no redirects, and no automatic retry. Collector failures do not block business requests or change readiness. Scoped exporter adapters deliberately drop failed exports and partial rejections, emit a fixed structured warning, and increment `clx_otel_export_failures_total{signal="traces|metrics"}` on the existing scrape endpoint. Collector response bodies and partial-success error messages are never passed to the SDK global error handler. This counter counts failed export requests, not individual lost spans or metric points. Telemetry is best effort and is not a durable audit trail. Shutdown flushes both providers concurrently within the existing process shutdown deadline. Unexported telemetry may be lost at the deadline.

## Server rollout

Configure a private OTLP HTTP receiver in your collector and select a trace backend with collector-side TLS/authentication. Keep port 4318 on a private network shared with Loom. Do not expose it through the public ingress. Collector configuration is maintained in the operator deployment repository.

Check that a request produces a route-pattern trace and correlated JSON log. Check OTLP operation metrics separately from the existing `clx_*` scrape metrics. Stop the collector temporarily and verify that API requests and process shutdown remain responsive. Set `enabled: false` and restart Loom to disable export.

## Dependency provenance and validation

The official Go SDK/exporters are v1.47.0, released on 2026-10-02. Full canonical VCS commits are recorded in `tools/dependencies.lock.json`. Exact license notices for new/upgraded graph modules are in `docs/dependency-audit/2026-10-12-otel/`. Notices were inspected: Apache-2.0, MIT, and BSD licenses meet the project's allowed list. The upstream project is maintained by OpenTelemetry/CNCF; no separate third-party tracing wrapper is used.

Behavioral tests use a local fake OTLP collector to check trace/metric export, route privacy, inbound parent propagation, nested log correlation, outgoing trace injection without mutating original headers, exporter outage, bounded queue, and shutdown. A real local Alloy v1.20.1 accepted synthetic HTTP/operation spans and metrics; the production receiver snippet also passed validation. [Current acceptance and remaining gates](validation/2026-10-12-files-otel/README.md). No live remote backend or production server acceptance is implied.

Sources: https://opentelemetry.io/docs/languages/go/exporters/ and https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.47.0.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
