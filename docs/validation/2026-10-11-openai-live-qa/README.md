# Prepared minimal OpenAI live stand, 2026-10-11

Daniel requested a separate stand for the smallest real-provider connectivity test. The stand is prepared with isolated PostgreSQL, HTTPS test identity, acceptance fixture, actual application runtime/River, Docker discovery and sandbox supervisor. The explicitly authorized single live check has now completed `LIVE_OK` on `gpt-6-luna/medium`, with one completed model step and reported usage 664 input / 6 output tokens. [Safe live result and initial disclosure gate](LIVE-RESULT.md). Further paid checks remain separate explicit operator actions.

The local token field is `providers.openai.api_key` in:

`C:\Users\ddriz\Projects\clx-scripter\management.local\openai-live-qa\config.yaml`

It was created empty and verified excluded by `.gitignore:10` (`*.local`). After handoff, only the retry ceilings and local diagnostic permission were edited while preserving the provider token bytes. No user token was printed or copied into evidence. [config.token-blank.yaml.txt](config.token-blank.yaml.txt) is the sanitized original, with no live token. Strict startup rejects an empty provider key.

The catalog is omitted so the current reviewed OpenAI defaults seed `gpt-6-luna` with `medium` effort. The stand limits output to 1024 tokens, context input to 8192, steps to 2, and each run to 10000 microcredits. Sponsored recovery attempts, short retries and durable provider retries are disabled. The local QA operator role includes `diagnostics.read` for the scoped operator panel. Semantic search is disabled; there is no old history, attachments or active compaction load. [config-validation.txt](config-validation.txt) proves these defaults from a substituted test key without any provider request.

## Start and single paid test

Run from PowerShell. Add the OpenAI token to the local config first. Wait for the application's current-source image/build marker before starting; `start.ps1` refuses if that build is not marked ready.

```powershell
& 'C:\Users\ddriz\Projects\clx-scripter\management.local\openai-live-qa\start.ps1'
```

This starts the API, migration CLI, restricted Docker proxy and isolated supervisor, then checks `/readyz`. It admits no run and performs no live inference.

When Daniel explicitly decides to perform the paid check:

```powershell
& 'C:\Users\ddriz\Projects\clx-scripter\management.local\openai-live-qa\minimal-paid-test.ps1'
```

This creates one fresh chat and admits one short request: `Reply with exactly: LIVE_OK. Do not use tools or ask questions.` It polls the existing run and saves result, persisted step counts and reported usage in the ignored directory. A created-once admission file prevents a second invocation. It does not automatically retry, resume, or create an additional run after any failure or timeout.

One application run is not an exact HTTP request quota: provider-accurate `/responses/input_tokens` preflight requests may accompany Responses calls. The default prompt aims for one Responses inference; the two-step ceiling bounds all persisted steps, including tools. Evidence reports persisted model steps separately from HTTP preflight calls.

Only if a second, deliberate paid decision is needed:

```powershell
& 'C:\Users\ddriz\Projects\clx-scripter\management.local\openai-live-qa\minimal-paid-test.ps1' -AllowAdditionalRun
```

A real read-only tool cycle would need at least three persisted steps (model call, tool call, final model call). The two-step stand deliberately does not offer that paid action. It requires a separate explicit configuration/test decision. Remaining orchestration mechanics can be tested with manual/codex-shim providers instead of adding real-provider calls.

## Ownership and cleanup

Compose project: `clx-openai-live-qa-20261011`; API: `http://127.0.0.1:58280`; acceptance: `http://127.0.0.1:58290`; explicit network subnet: `10.254.247.0/24`. PostgreSQL and HTTPS issuer have no published host port. The issuer receives only its disposable certificate/key files, not the local token config. API trusts both the disposable issuer CA and the normal system HTTPS roots used for OpenAI.

All owned stand services are now prepared: API readiness 200, scoped diagnostics 200, operator shell `/admin` 200, and the freshly migrated database had zero runs and zero model steps before the paid test. The current runtime image manifest is `sha256:e4bacdc26946cc7942a3254900e4f6972d22d519ee3ee554cfc5205627339f68`. [image.json](image.json), [before-paid-test-db.txt](before-paid-test-db.txt), [prepared-resources.jsonl](prepared-resources.jsonl). The services are left running and idle for Daniel. Model invocation remains a separate guarded action. Disposable issuer certificates expire after 24 hours; if the stand is used later, regenerate only its TLS/auth fixture files with `management.local/openai-live-qa/prepare-peer.ps1` and restart the issuer before starting API. Do not overwrite the user-edited config during regeneration.

```powershell
& 'C:\Users\ddriz\Projects\clx-scripter\management.local\openai-live-qa\cleanup.ps1'
```

Cleanup removes only this project's containers, network and volumes. It leaves the ignored local config and any user-inserted token in place. No automatic watcher, scheduler, paid test or production deployment was installed.

The compiled issuer helper is already present. `prepare-peer.ps1` can rebuild it using a temporary `.go` file outside the repository; the scratch source remains `.go.txt` so it does not enter `go test ./...` or lint package discovery. It never reads or overwrites the token config.

Replay sources: [compose.yaml.txt](compose.yaml.txt), [peers.go.txt](peers.go.txt), [start.ps1.txt](start.ps1.txt), [minimal-paid-test.ps1.txt](minimal-paid-test.ps1.txt), [cleanup.ps1.txt](cleanup.ps1.txt). The minimal live inference response and reported usage are verified in [LIVE-RESULT.md](LIVE-RESULT.md). Full Zitadel signing behavior, broad inference quality, provider invoice reconciliation and production readiness remain outside this check. Script syntax and substituted config parse were checked without network inference.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk






