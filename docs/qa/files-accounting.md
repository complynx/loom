# File and accounting QA

Use [file tools](../file-tools.md), the [isolated worker](../file-worker.md), [memory](../user-memory.md) and [billing contract](../data.md). Pass IDs and metadata through JS, not complete files.

## Files and media

- Import/upload: compare byte length and hash. Check foreign-owner/disclosure denial, quota/size bounds, range/HEAD/ETag and partial-read cursors.
- Expiry: direct internal ID becomes unavailable even during an active run. Fetch the same external source ID: new temporary bytes match the expected hash. Test the automatic helper, exact-version checks and restart separately.
- Source outage: keep durable metadata and source ID. After recovery, read the same reference. Do not infer permanent deletion from an arbitrary 503.
- PDF: extract text and render a standard-font document. Decode and inspect the output for readable text; HTTP 200 alone is insufficient. OCR/scans/font fidelity are separate properties.
- Images: resize/crop, invalid bounds, malformed/oversized input and a model attachment. Recheck ownership, disclosure and model capabilities.
- Video/GIF: metadata, storyboard, a detailed interval, a selected frame and extracted audio. Decode the result and compare dimensions, duration and a known signal.
- Transcription: reserve, settle, lose a reply, and reconcile without redispatch using a fixture provider. Use a real provider separately for speech quality and actual usage.
- Isolation: check non-root/no-network/read-only execution, bounded temp/memory/CPU/PIDs, cancellation and the next healthy job.

## Credits

Check exact single settlement, unknown-outcome reserves, cancellation, budget exhaustion and privileged reconciliation. A sponsored uncertain attempt is platform exposure. A saved successful checkpoint must not be charged again.

## Memory, search and context

Check owner/org isolation, the micro-memory limit, refresh before each inference and on-demand index → notes → sources. Check literal/fulltext/regex, partial results and cursors. A disabled semantic tool must not be advertised.

A previous-run hint cannot disclose foreign context or transfer consent. Compact a long run while preserving searchable source history and completed tool checkpoints. Compare the hard ceiling, actual provider usage and cost. A target is not a guarantee of output size or a provider cache hit.
