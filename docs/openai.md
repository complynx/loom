# OpenAI catalog and context

The `openai` provider uses the Responses API. When its bootstrap catalog is omitted, Loom seeds `gpt-6-luna` with effort `medium` as the default, plus `gpt-6.1-sol` and `gpt-6-astra`. Explicit catalogs and settings already stored in PostgreSQL take precedence.

## Preset tariffs

These rates are the repository snapshot dated 2026-10-11 in `internal/config/openai.go`. They are not a live pricing feed. Check the [official OpenAI pricing](https://developers.openai.com/api/docs/pricing) before changing an installed catalog.

USD per million tokens:

| Model | Input | Cached input | Cache write | Output |
| --- | ---: | ---: | ---: | ---: |
| `gpt-6-luna` | 0.10 | 0.01 | 0.125 | 0.50 |
| `gpt-6.1-sol` | 2.00 | 0.10 | 2.50 | 10.00 |
| `gpt-6-astra` | 10.00 | 1.00 | 12.50 | 50.00 |

The preset uses micro-USD: 1000000 microcredits represent one USD. Cached reads and cache writes are disjoint parts of total input. Ordinary input equals total input minus both cache counts. Settlement rounds the combined input charge and the output charge up separately to whole microcredits. A missing required cache-write tariff prevents settlement and leaves the reservation for reconciliation.

For input above 272000 tokens, the preset applies 2x input/cache rates and 1.5x output rates to the whole request. Exactly 272000 uses the short-context rates. The transport requests Standard service with `service_tier=default`. Regional and Fast-mode premiums are outside this preset. See [history and billing](data/history-billing.md) for the ledger lifecycle.

## Input limits

The default model policy is:

| Setting | Tokens | Purpose |
| --- | ---: | --- |
| `input_token_limit` | 272000 | Hard input ceiling |
| `compaction_threshold_tokens` | 244800 | Start compaction before the ceiling |
| `compaction_target_tokens` | 204000 | Bound the recent history retained by history summarization |

These are Loom settings, not upstream model limits. The process `context_input_token_budget` adds a hard ceiling. The lower ceiling applies; raising the model limit alone cannot raise the process limit. For older catalogs without `input_token_limit`, the compaction threshold also acts as the model input ceiling. Live settings reductions apply before the next provider request.

Instructions, tools and all input items count toward the ceiling. Ordinary text uses conservative byte bounds, so history summarization can start before the nominal token threshold. Provider token counting is used when needed, and is required for opaque compaction items. Output has a separate limit: the deployment template sets `output_token_limit: 4000`.

History summarization keeps complete recent events and a summary of earlier events. [Active-run compaction](active-compaction.md) replaces the model window between turns, after outstanding tool results are saved. The native endpoint chooses its replacement size; the 204000 target does not control it. If the complete replacement input cannot fit, the run pauses. Stored history and execution checkpoints remain available.

Before a new run, more than four passive actions in a history gap become the first action, a count/provenance placeholder and the last three actions in the model view. Before the first request, the placeholder gives the total count and only the last three actions follow. Up to four actions remain complete. This compression happens before pagination; full stored events remain searchable. Passive actions do not start runs.

Change an installed catalog and tariffs through the authorized settings API. See [context policy](context-research.md) for the rationale and [development providers](test-providers.md) for testing without direct Responses inference.
