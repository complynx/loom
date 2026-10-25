# File and accounting QA

Use [file tools](../file-tools.md), the [isolated worker](../file-worker.md), [memory](../user-memory.md) and [billing contract](../data.md). Pass IDs and metadata through JS, not complete files.

## Repeatable fixture checks

Prepare the disposable database in [setup](setup.md), then run from the repository root:

```sh
go test -v -race -parallel=4 -tags=integration -count=1 ./integration \
  -run 'Test(ArtifactRangeAndConditionalDelivery|HandleStreamsOriginalBytesAndDeniesForeignCaller|PaidAudioDispatchSettlesOrRetainsHoldWithoutRestartReplay|PersonalMicroMemoryReloadsWithinRunAndPreviousRun)$'
go test -v -race -parallel=4 -count=1 ./internal/fileproc
```

Media tests skip when required CLIs are absent. Use the pinned media test command
in [CI](../../.github/workflows/ci.yml) to exercise Poppler and FFmpeg in Linux.
Passing processor tests does not prove container isolation; verify the deployed
worker's limits and security settings separately using [worker setup](../file-worker.md).

For manual cases, use synthetic inputs with recorded hashes, known text or signals,
and explicit dimensions/duration. Record artifact/reference IDs, the test owner's
identity, quota before/after and the expected expiry. Do not use private user media.

## Files and media

- Import/upload: compare byte length and hash. Check foreign-owner/disclosure denial, quota/size bounds, range/HEAD/ETag and partial-read cursors.
- Expiry: direct internal ID becomes unavailable even during an active run. Fetch the same external source ID: new temporary bytes match the expected hash. Test the automatic helper, exact-version checks and restart separately.
- Source outage: keep durable metadata and source ID. After recovery, read the same reference. Do not infer permanent deletion from an arbitrary 503.
- PDF: extract text and render a standard-font document. Decode and inspect the output for readable text; HTTP 200 alone is insufficient. OCR/scans/font fidelity are separate properties.
- Images: resize/crop, invalid bounds, malformed/oversized input and a model attachment. Check that default PNG outputs retain transparent and semitransparent pixels and that model attachment preserves the original bytes. Explicit JPEG output must composite onto white. Recheck ownership, disclosure and model capabilities.
- Video and animation: metadata, storyboard, a detailed interval, a selected frame and extracted audio when present. Cover GIF/WebP/APNG/AVIF/HEIF/JXL/WebM/MNG/TGS with [animation fixtures](../file-tools/animations.md#worker-dependencies-and-verification). Compare dimensions, precise duration and known frame changes. For WebP, check offsets and disposal. Verify held-frame timestamps, fractional playback boundaries, cursors and the ordinary-video fallback above 1000 frames. Verify that direct model attachment and image resize/crop reject animations without sending a single still frame.
- Transcription: reserve, settle, lose a reply, and reconcile without redispatch using a fixture provider. Use a real provider separately for speech quality and actual usage.
- Isolation: check non-root/no-network/read-only execution, bounded temp/memory/CPU/PIDs, cancellation and the next healthy job.

## Credits

Check exact single settlement, unknown-outcome reserves, cancellation, budget exhaustion and privileged reconciliation. A sponsored uncertain attempt is platform exposure. A saved successful checkpoint must not be charged again.

## Memory, search and context

Check owner/org isolation, the micro-memory limit, refresh before each inference and on-demand index → notes → sources. Check literal/fulltext/regex, partial results and cursors. A disabled semantic tool must not be advertised.

A previous-run hint cannot disclose foreign context or transfer consent. Compact a long run while preserving searchable source history and completed tool checkpoints. Compare the hard ceiling, actual provider usage and cost. A target is not a guarantee of output size or a provider cache hit.
