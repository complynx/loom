# Fault repairs and default memory access — 2026-10-11

Status: locally accepted. No production deployment or paid model call. Original failed evidence remains unchanged.

- [Cancellation checkpoint](../2026-10-11-cancel-fix/README.md): the owned run/fence survives an interrupted state read; cancellation, pause and shutdown checkpoint through the bounded uncancelled path. Non-context database errors remain observable. The exact post-inference diagnostic now records cancelled/client_cancel, spent 15 and reserved 0.
- [Sandbox health handshake](../2026-10-11-sandbox-health-fix/README.md): a saturated supervisor reads the request before responding and closing. The regression failed before the patch and passed after it; no worker starts for health requests. Overload handshake work is bounded to 100 ms.
- [Shutdown budget](../2026-10-11-shutdown-fix/README.md): parallel stop and cleanup reserve part of the configured deadline for process exit. The completion flag cannot report success after context expiry. Healthy and PostgreSQL-outage process exit are measured separately from delayed Docker event notification.
- [Personal memory](../2026-10-11-user-memory/README.md): every authenticated user can access their own memory without extra roles. User/org isolation, fresh callback authorization and the active lease remain required. Micro memory is reloaded each inference.

Fresh independent source reviews: memory/sandbox clean; cancellation and shutdown each received one P2 finding, corrected it, and passed a fresh review. Reviews did not run tests. Root final broad gates after all code corrections: [Windows integration](windows.txt), [Linux/Docker integration with race detection](linux-race.txt), [Golden](golden.txt), all exit 0; Golden reports 0 issues.

These repairs close the reproduced defects. Disk-full, target deployment, backup/restore, full target issuer queue restart/reauthorize, load envelope, remote CI and production integration remain separate acceptance work. A successful suite does not replace those checks.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
