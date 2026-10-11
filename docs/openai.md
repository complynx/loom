# OpenAI catalog and context

Reviewed on 2026-10-11 against the [official model catalog](https://developers.openai.com/api/docs/models) and model pages. These are bootstrap defaults, not a live pricing feed. Explicit YAML catalogs and existing PostgreSQL settings are not replaced.

The default provider alias is `openai`; the default model is `gpt-6-luna`, effort `medium`. The preset contains the three latest general-purpose agent models supported through Responses. Specialized audio, image-generation and restricted research models require other APIs and are outside this catalog.

Standard global prices, USD per million tokens:

| Model | Input | Cached input | Cache write | Output |
| --- | ---: | ---: | ---: | ---: |
| [GPT-6 Luna](https://developers.openai.com/api/docs/models/gpt-6-luna) | 0.10 | 0.01 | 0.125 | 0.50 |
| [GPT-6.1 Sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol) | 2.00 | 0.10 | 2.50 | 10.00 |
| [GPT-6 Astra](https://developers.openai.com/api/docs/models/gpt-6-astra) | 10.00 | 1.00 | 12.50 | 50.00 |

Preset tariff units are micro-USD: one USD is 1000000 microcredits. Cached reads and cache writes are disjoint subsets of total input; neither is charged again as ordinary input. Above 272000 input tokens, the entire request uses 2x input/cache rates and 1.5x output rates. Exactly 272000 uses the short-context rate. The OpenAI transport explicitly requests Standard (`service_tier=default`). Regional and Fast-mode premiums are not enabled by this preset. Usage without a required write tariff fails closed and retains the reservation.

See [upstream context research](context-research.md) for Codex/OpenClaw comparison.

Each model has `compaction_threshold_tokens` and `compaction_target_tokens`. Current defaults are 244800 and 204000, with a separate per-model `input_token_limit` of 272000. The trigger follows the verified Codex default (90% of 272000); the target remains our earlier headroom heuristic. These are harness settings, not a published training constraint. The process `context_input_token_budget` is an additional hard ceiling. A model can lower the hard ceiling through `input_token_limit`; raising it also requires raising the process ceiling. Older catalogs without this field retain the compaction threshold as their hard bound. All instructions, tools and input items count toward the ceiling, not only chat text.

Current history compaction uses conservative byte bounds, so it may summarize earlier than the nominal token threshold. It summarizes complete earlier events, preserving a recent suffix and full stored history. Between turns, [active-run compaction](active-compaction.md) replaces the model input window after outstanding tool results are saved. Durable tool/model checkpoints remain intact. If the resulting complete input exceeds its ceiling, the run pauses. The trigger is applied before compaction. The target bounds the retained recent suffix after compaction; it is not a guarantee that provider usage will equal that number. Output has its own limit, 4000 by default in the deployment template.

Before a new run, more than four passive actions since the previous active start are reduced in the model view to the first action, a count/provenance placeholder and the last three actions. With no prior dialogue, the placeholder states the total count and precedes the last three actions and first request. Up to four actions stay complete. Compression happens before pagination; full events remain searchable, including by regexp. Passive events never start a run.

Existing installations change model and tariff settings through the authorized settings API. Review rates and capabilities before changing an existing catalog. Per-user selections remain intact. Preset and tariff tests use fixtures. A separate [minimal live QA](validation/2026-10-11-openai-live-qa/LIVE-RESULT.md) completed one paid text call; it does not validate every preset, live tools, images or compaction quality.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk