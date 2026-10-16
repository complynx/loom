# Context policy and research

Loom separates the model's advertised context window from its own input ceiling and compaction policy. This keeps admission, reservations and recovery predictable when provider capabilities change.

The preset uses a 272000-token hard input ceiling, a 244800-token compaction trigger and a 204000-token history target. These values are defined in `internal/config/openai.go`. The trigger is 90% of the input ceiling. The target is a Loom headroom heuristic; it is not a provider recommendation or a required final window size.

## What each limit controls

- The model context window bounds combined model input and reserved output.
- `input_token_limit` and the process `context_input_token_budget` bound input. The lower value applies.
- `compaction_threshold_tokens` triggers compaction before the hard input ceiling.
- `compaction_target_tokens` bounds the recent suffix retained by history summarization. It does not set native compaction output size.
- `output_token_limit` limits ordinary model output separately.

Byte bounds are conservative for plain text. They can cause history summarization before the nominal token trigger. Instructions, tool schemas and current input also occupy context; chat text alone is not a sufficient measure. Opaque native compaction items require provider token counting.

## Upstream reference

The [OpenAI compaction guide](https://developers.openai.com/api/docs/guides/compaction) describes a replacement input window with an opaque compaction item. Loom stores that window as a checkpoint and supplies it to later requests. The endpoint chooses the replacement size.

Other harnesses can use different trigger timing, output reserves and retained-history rules. A matching percentage does not imply matching behavior. Local model caches and CLI settings are not a portable source for Loom's defaults.

## Evaluating a policy change

Keep complete tool call/result pairs and preserve durable execution records. Summaries are untrusted context and cannot grant permissions or approval. Pause when a replacement cannot fit or native compaction does not reduce the counted window below the trigger.

Compare retained task facts, unresolved requests, identifiers, source references, tool pairing, latency and billed usage. A smaller context percentage alone does not establish better compaction. See [active-run compaction](active-compaction.md) for recovery behavior and [OpenAI catalog and context](openai.md) for the current preset.
