# Single real OpenAI connectivity check, 2026-10-11

The real-provider check completed successfully: the existing isolated run returned **`LIVE_OK`** using **`gpt-6-luna`, `medium` effort**. Exactly one persisted model step completed, with one provider response ID. There was no additional admission or automatic retry. The stand remains running at `http://127.0.0.1:58280`.

Reported usage: **664 input tokens, 6 output tokens, 0 cached input tokens**. Application credit settlement: **70 microcredits**, with **0 reserved** afterward. The usage is provider-reported and the settlement uses the application's configured catalog; this is not an independently checked OpenAI invoice. [live-result-summary.json](live-result-summary.json), [live-usage.txt](live-usage.txt).

Run ID: `8d7a9f3c-e8e2-4733-accc-81397842864a`.
Provider response ID: `resp_0379b04e8611e428016acb9f4852b087d28f15951002999e44`.

## Initial gate and explicit continuation

The first single-run script admitted the run but stopped at `paused/disclosure_denied`, with zero model steps, zero usage and zero reserved credits. The stand's initial `external_disclosure_default:false` prevented sending even the short LIVE_OK prompt. That fixture setting was too restrictive for the authorized live check. No inference was dispatched at that stage: runtime authorization rejects before the model-step reservation/provider path. [initial-disclosure-blocked.json](initial-disclosure-blocked.json).

After explicit authorization to correct only this QA stand and continue the same run, a transaction required zero existing model steps and the exact old disclosure flags. It enabled current QA settings and this existing run's admitted disclosure flag. The ignored local configuration was updated consistently while preserving the user-entered OpenAI key. The transaction result again showed zero model steps before resume. [disclosure-settings-correction.sql.txt](disclosure-settings-correction.sql.txt), [disclosure-settings-correction.txt](disclosure-settings-correction.txt).

Exactly one explicit resume was then submitted. It completed LIVE_OK. Sponsored recovery, short provider retries and durable provider retries all remained zero. No second run, repeat resume or tool-cycle request was submitted. The number reported here is **one completed Responses inference/model step**; provider token-count preflight HTTP requests are a separate protocol concern and were not independently counted on the wire.

The check used the actual current-image API, River scheduling, PostgreSQL and runtime provider client, plus the isolated HTTPS test issuer. It verifies live model admission, request/response translation and usage settlement. It does not expand the earlier deterministic QA into a real-provider test of tools, compaction, inference quality, concurrency or production readiness.

The API key stays in the ignored local config, is excluded from Docker build context, and was not printed or copied into committed evidence. The local test identity is disposable; no production issuer, deployment or server was touched. [Stand runbook](README.md).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
