# Full local manual acceptance — 2026-10-11

Follow-up 2026-10-12: [current fixture browser acceptance passed](../2026-10-12-browser-qa/README.md) after user-installed CA trust and Codex restart. The BLOCKED entries below preserve the original October 11 result; production acceptance remains open.

Acceptance used the reviewed Linux image `clx-scripter:full-review-qa-20261011`, image ID `sha256:cef24f773800be422bcbeed05deb4456afe95edc865a416affbf48947f8ad1c4`. All lanes used real PostgreSQL, API/River and separate sandbox processes. This is local acceptance; production was not changed. No paid OpenAI calls were made.

| Lane | Result and scope | Evidence |
| --- | --- | --- |
| General runtime | PASS: 40 corrected scenario groups covering public HTTP commands, durable queue, JS, discovery, history/search, personal memory, artifacts, questions/approvals, recovery, accounting, diagnostics and CA-trusting PKCE protocol with deterministic HTTPS fixtures. Corrected assertions and original driver failures are recorded separately. | [General report](general/README.md) |
| Real identity and restart | PASS: actual Zitadel revocation, `waiting_auth`, API/River restart, fresh credentials, CAS reauthorize and completion of the same run. Two settlements of 110, zero reserve; same-role foreign user/org cannot access it. | [Reauthorization report](reauthorize/README.md) |
| Disk-full and read-only | PASS in measured scope: actual artifact and PostgreSQL ENOSPC, rejected writes, retained data, recovery, read-only rejection/recovery and 437 ms shutdown without active inference. | [Fault report](disk-full/README.md) |
| Current browser login | BLOCKED: logged-out panel rendered, but clicking Sign in reached `ERR_CERT_AUTHORITY_INVALID` on the private self-signed fixture. The browser warning was not bypassed and system trust was not modified. HTTP PKCE protocol verification is a separate result, not authenticated browser acceptance. | General report; [earlier real-Zitadel browser acceptance](../2026-10-11-panel-login-qa/README.md) is historical |

## Interpreting the evidence

The broad lane checks implementation behavior, not model intelligence or quality. The real-identity lane uses actual Zitadel tokens, but deterministic inference and no external writes. TTL expiry was not used for its restart scenario; revocation was. The disk lane fills bounded container tmpfs, not the host disk, and runs as root for fixture mount access; production UID permissions still need acceptance.

A full filesystem need not reject every write: allocated pages may still accept a durable run and settlement. A large-input run remained `context_unavailable` after freeing disk and resumed without another summary charge; its pause is not attributed to ENOSPC. `/readyz` remains 200 under write ENOSPC and read-only roles: it verifies connectivity, not writable capacity. Storage alerts remain necessary.

First-pass driver mistakes and invalid fixture expectations remain in each lane. In-flight inference pause can require reconciliation of unknown usage. The broad lane resolves that through an audited HTTP operator receipt, not a direct database bypass. A successful rerun does not erase the first failure or prove every possible concurrency schedule.

## Remaining acceptance boundaries

- Current authenticated browser flow requires a trusted TLS fixture or user-controlled browser handoff. Prior browser proof is explicitly a different snapshot/stand.
- Live model tools, images, native compaction, encrypted continuation, cache accounting and quality are not established by deterministic providers. The earlier single paid connectivity/accounting call remains its own limited result.
- Host OOM, inode exhaustion, physical filesystem power-loss durability, WAL PANIC recovery, active-inference shutdown during ENOSPC, sustained load/fairness and multi-replica operation were not exercised here.
- Production issuer/client setup, service adapter, remote CI, monitoring delivery, permissions, backup/restore/PITR and pilot are separate gates. There is no global recovery barrier for restored database state behind external effects.

The [whole-code review evidence](../2026-10-11-full-review/README.md) records final Windows integration, Linux race, Golden and image build passes. Reviewed source hashes remain unchanged except documentation. Final documentation link check covered 179 relative links with no missing files; whitespace check passed and the Git index remained empty. [Limitations](../../mvp-limitations.md), [track](../../development-track.md), and [server rollout](../../server-rollout.md) describe the remaining work.

## Retained isolated stands

The three new QA projects are retained for inspection, alongside the existing user demos. They are not production deployments. API ports: general 58680, real reauthorization 58480, disk-full 58580. Credentials are only in ignored `management.local/`; public evidence contains no detected JWT signatures, OpenAI keys or private keys.

After inspection, remove only the corresponding owned project with its private Compose arguments and `down --volumes`; general additionally requires `rotation.compose.yaml` after its session-key rotation. Do not run a global Docker prune to clean these lanes. Cleanup commands are in the lane reports/private drivers. Preserve public evidence and any desired private fixture data first.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
