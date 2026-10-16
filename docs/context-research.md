# Context policy research

Checked 2026-10-11. Upstream defaults are implementation choices, not universal model recommendations.

| Harness | Trigger / reserve | Retained context |
| --- | --- | --- |
| Codex CLI 0.157.1 | Default limit is 90% of raw window; effective window is separately 95% for the local catalog | Native compacted state; no fixed 75% post-compaction target |
| OpenClaw embedded runtime | Default 20000-token reserve, capped at a quarter of model window | Recent-tail setting defaults to 20000 tokens plus persisted summary |
| Claude Code | Automatic context management near the limit; this documentation does not specify a universal numeric threshold | Old tool outputs cleared first, then conversation summarized |

Local Codex executable reports 0.157.1. Its local model cache reports client_version 0.162.0, fetched 2026-10-11 10:51:25 UTC. Luna, Sol and Astra each report raw context 272000, maximum overridable window 872000 and effective percentage 95. Global config selects Sol/medium and has no explicit window or compaction override. Therefore the catalog-derived default compact limit is 244800 and the effective available window is 258400. Per-thread overrides and future catalog changes can alter these values.

The formula is confirmed in the [pinned Codex source](https://github.com/openai/codex/blob/rust-v0.157.1/codex-rs/protocol/src/openai_models.rs#L484-L506). The [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) exposes model_context_window and model_auto_compact_token_limit. A resolved limit is not a claim that every run compacts at exactly that token count; trigger timing, current turn and scope matter.

[OpenClaw's compaction reference](https://docs.openclaw.ai/reference/session-management-compaction/compaction) separates the hard model window from approximate session counters. It keeps tool calls paired with their results. Its [context policy](https://docs.openclaw.ai/concepts/compaction) accounts for instructions, tool schemas, pending input and output reserve and rejects ineffective replacement histories. Its [pre-compaction housekeeping](https://docs.openclaw.ai/reference/session-management-compaction/housekeeping) defaults to a 4000-token soft margin before compaction for optional durable-memory flushing. This is distinct from summarization and transcript pruning.

[Claude Code](https://code.claude.com/docs/en/how-claude-code-works) clears older tool results before summarizing and protects persistent instructions separately. It stops repeated ineffective compaction instead of looping indefinitely.

Suggested policy for this harness: retain the requested 272000 hard input ceiling; use the agreed configurable early trigger of 244800, a bounded recent tail and explicit room for tools/output. The current 204000 target is our heuristic, not Codex or OpenAI guidance. The user approved fixing 272000 as the hard ceiling and 244800 as the trigger. Improve active-run compaction and token-aware tail selection before claiming the same behavior as native Codex. Benchmark retained task facts, unresolved requests, IDs, tool pairing, latency and billed usage, rather than optimizing a percentage alone.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
