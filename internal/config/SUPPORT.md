# Configuration support

This is the runtime support checklist. Strict YAML decoding rejects unknown keys. Environment expansion changes scalar values and cannot inject YAML structure. Bootstrap settings seed PostgreSQL once; later edits use the settings API. Deployment settings remain outside that API.

Each public value is enforced, rejected as unsupported, or explicitly assigned to deployment. External container limits and supervisor flags must be kept aligned with YAML.

| Settings | Enforcement |
|---|---|
| HTTP listen, request limit, synchronous wait, disconnect grace | Application HTTP/API options. |
| HTTP SSE buffer size | Database-backed replay batch size, wired from YAML and bounded to at most 200 events. |
| Database DSN, connection count, statement/lock deadlines | PostgreSQL pool startup. |
| Operator client_id / redirect_uri | Separate public PKCE client for `/admin`; both empty disables login, partial configuration is rejected. Redirect is fixed to `/admin`, HTTPS except literal loopback. Deployment settings; no client secret. See [operator login](../../docs/operator-login.md). |
| Operator proxy_origin | Reuse OIDC ingress at a fixed origin; require admin_organization_id and diagnostics.read. Mutually exclusive with direct PKCE. No access token is returned to browser JavaScript. |
| Identity issuer, audience, client credentials, JWKS TTL, role permissions | Authenticator: fixed algorithms, signature checks, trusted discovery, online introspection and organization-scoped role mapping. |
| Identity permission-cache TTL | Positive claims cache: default 120 seconds, allowed 0..300 and capped by token expiry. Zero disables reuse. Sensitive boundaries require fresh authority; no stale-on-error. Database generation permits a shared purge. See [cache contract](../../docs/identity-cache.md). |
| Identity encryption file/key ID and previous_encryption_keys | Startup loads the active AES256 write key and previous read keys. Envelopes authenticate principal, expiry, purpose, OAuth client and audience. Startup rejects missing keys referenced by stored sessions; rotate-session-keys commits bounded authenticated re-encryption batches. See [rotation runbook](../../docs/session-key-rotation.md). |
| Identity refreshable exchange | Rejected while the installed Zitadel integration gate remains open. |
| Identity admin organization | API administrative scope check. |
| Identity service subjects | Optional seed. Validated caller identities are persisted during successful Registry.Refresh before the trusted snapshot is published. Persistence/discovery failures deny trust. Durable markers keep removed actors classified as services across restarts. |
| Discovery source, label, networks, poll/freshness, manifest limit | Registry and pinned discovery sources. A fresh successful registration snapshot is required for dynamic caller classification. |
| Discovery request timeout and dynamic listing TTL ceiling | Registry request deadline and dynamic listing cache ceiling, wired from application YAML. |
| Provider endpoints/keys and model/provider routing | Provider clients. Access credentials never enter scripts or model prompts. |
| Model ID, provider model, efforts/caps, context size, image capability, output size | Bootstrap maps these to current model policy and provider limits. |
| Model parallel-tools, steering, question flags | MVP admission requires all three capabilities; unsupported catalog entries fail bootstrap. Request interaction controls remain independent. |
| Input/output tariff | Current model policy and credit reservations/settlement. |
| Cached-input tariff | Provider cached-token usage and shared integer tariff calculation; cached tokens are charged at the configured cached-input rate. |
| Artifact root, upload limit, per-user quota | Immutable artifact service. Per-user quota includes tombstoned blobs until physical unlink, directory synchronization and the collected marker succeed. Four concurrent upload slots bound temporary upload bytes to 4 × (max_upload_bytes + 1). |
| Sandbox socket and script timeout | Process client and per-script deadlines. |
| Sandbox parallel RPC limits | Script bridge limits, clamped to deployment ceiling. |
| Live execution limits | DB total_active_run_limit, script_timeout_ms and parallel_rpc_limit cannot exceed startup ceilings. Serialized admission uses the live total; next scripts apply reductions without interrupting current work. |
| File tools | Optional private Unix worker, default disabled; 30-second maximum job timeout, bounded streams and scoped artifact handles. Poppler/FFmpeg stay outside the API/JS process. |
| Transcription | Optional, default disabled. Default openai/gpt-transcribe, 75 microcredits/second; API key inherits providers.openai.api_key. Duration-only billing, frozen reserve/tariff, no automatic retry, reconciliation for uncertain dispatch. whisper-1 needs an explicit correct rate. |
| Sandbox engine | Goja only. Other engine names fail validation. |
| Sandbox memory and PIDs | External container enforcement. Compose sets a shared 128 MiB memory ceiling and 128 PIDs for the supervisor and its child processes, matching deployment YAML. This is not a per-worker memory allowance. |
| Sandbox maximum workers | Supervisor CLI owns the process count. Deployment must supply the matching value; broker configuration alone does not set supervisor capacity. |
| Sandbox cleanup timeout | Supervisor --cleanup-timeout flag, set to 100 ms in Compose to match deployment YAML. Other deployments must keep the flag and YAML value aligned. |
| Execution lease and total worker ceiling | Fenced run engine and River worker pool; the total active-run admission cap is also enforced by the store. |
| Execution provider_retry_attempt_limit / provider_retry_window_seconds / provider_retry_short_limit / provider_retry_short_budget_ms | Known output-free provider rejection policy: defaults 6 attempts, 300 seconds, 2 short retries, 3000 ms local budget. Persisted long waits survive workers; Retry-After is a minimum. This budget limits waiting/dispatch, not accepted inference duration. See [hybrid retry](../../docs/hybrid-retry.md). |
| Execution sponsored_attempt_limit | Deployment setting: default 2 uncertain paid attempts per run, then pause; 0 disables sponsorship. Fresh authorization and fenced checkpoints release user reserves once and preserve unknown platform exposure. Client pause/cancel and uncertain writes are excluded. |
| Execution heartbeat | Run engine heartbeat option, wired from the deployment setting. |
| Execution shutdown deadline | Shared graceful-shutdown context, at most five seconds. |
| Execution shutdown target | Operational latency target; measure it. It does not override the hard deadline. |
| Execution per-user ceiling | Immutable store admission ceiling, wired at startup. Administrative settings and user policies cannot raise active-run limits above the deployment ceiling. |
| Recovery retry window | Not a public YAML field. Unknown outcomes require status proof or privileged reconciliation; automatic write replay is not enabled. |
| Default model/effort, initial credits, run budget, active runs, history count, maximum steps | Bootstrap database settings and admission/execution policy. Zero initial credits is allowed and does not imply free inference. |
| Input/approval waits | Question policy and operation approval expiry. |
| Run active time | The sole River worker disables its static job timeout. The engine enforces the admitted snapshot cumulative active-time budget and absolute lifetime, with durable accounting across restarts and lifecycle sweeps. |
| Run lifetime, paused waits, history/artifact retention | Durable lifecycle sweeps apply run lifetime and paused deadlines. History events/summaries expire when no live chat run needs them; expired artifacts are tombstoned after live-reference checks. The application runs collection at startup and once per minute in bounded batches. CollectDeleted removes tombstoned files, synchronizes the directory and then records collection; orphan reconciliation follows with a one-hour grace period. Recovery, financial, credential and audit records remain retained. |
| Provider kind / mailbox / executable / codex_home | Explicit development transport selection. Empty/responses uses HTTP; mock requires an existing private absolute mailbox; codex requires an executable and a dedicated authenticated home. Transport selection preserves the model tariff. See docs/test-providers.md. |
| OpenAI omitted catalog | Seeds the reviewed three-model catalog and Standard global rates only when the catalog, default ID and tariffs are omitted. Existing database settings remain intact. |
| Model input_token_limit / compaction threshold / target | Validated per-model context bounds, conservative history assembly and complete-input hard limit. Target is a history budget, not a provider token guarantee. |
| Cache-write and long-context tariffs | Separate disjoint input categories; threshold selects whole-request rates. Reservation covers worst supported rates. |
| Global input-token budget | Hard startup ceiling plus live DB context_input_token_budget. Next model turn uses the stricter live/admitted/hard bound; oversized context pauses before dispatch. Input bytes are bounded separately. |
| Global output-token limit | Bootstrap clamps each model output limit to this global ceiling. |
| Search semantic_enabled | Deployment switch, default false: hides semantic agent tool and omits vector-indexing HTTP route. Enabled queries require externally supplied matching vectors and installed pgvector; no paid embeddings call or automatic index. |
| Personal user memory | Available to authenticated users without additional role mappings. Fixed bounded layout and CAS versions; micro profile included in every inference. No public YAML memory limit or automatic extraction. See [memory](../../docs/user-memory.md). |
| Regex pattern/scan/deadline limits | Current database settings control maximum pattern characters, scanned bytes and context deadline for RE2 searches. |
| External disclosure default | All provider endpoints are treated as external, including loopback and self-hosted endpoints. Both current policy and the admitted run snapshot must allow disclosure before inference, summaries, existing-checkpoint/history forwarding, module discovery or RPC. Denial pauses execution with disclosure_denied; owner/provider/MIME artifact checks also apply. The default is false, so an operator must enable disclosure before agent use. |
| Trace retention | No separate persisted trace stream is emitted. Only forever/unspecified policy is supported; duration fails validation. Operational Docker log retention is a deployment responsibility; recovery checkpoints are not trace records. |
| Observability log level | DEBUG/INFO/WARN/ERROR filter structured JSON logs. Invalid levels fail validation; DEBUG includes authenticated identity diagnostics. |
| Observability payload logging | Optional debug-only request/model diagnostics, bounded to 4 KiB. JWT signatures and opaque credentials are masked; arbitrary free-text secrets cannot be recognized. Default false; enabling outside debug fails validation. |
| Observability OTel | Optional scoped OTLP/HTTP trace and operation-metric export; default disabled, 10% root sampling (explicit zero preserved), bounded queue/timeout, safe route attributes and trace-correlated JSON logs. Collector failure does not gate API readiness. No durable trace-parent restoration across queue restarts. |
| Observability metrics_listen | Optional separate private Prometheus listener. Cached aggregates refresh every 15 seconds with a two-second database budget; no SQL or JS runs on scrape. Empty disables metrics. No public API metrics route. |

An access token belonging to a never-registered machine cannot be identified as a machine from an invented claim. It cannot delegate another subject: delegation requires a current trusted caller registration. Its own-subject scope is determined by explicit Zitadel role mappings. Assign machine roles no human permissions unless that behavior is intended.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
