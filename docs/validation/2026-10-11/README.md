# MVP validation, 2026-10-11

The implemented Linux/Docker MVP passes the local acceptance gates below. This is not a production deployment or validation against the installed Zitadel and a live paid model provider.

## Verified behavior

The full Go 1.27.2 Linux amd64 `go test -race -tags=integration ./...` gate passed with real isolated PostgreSQL 17 and no race findings. The preserved package output is in [linux-race.txt](linux-race.txt). Some unchanged package results came from the Go test cache; changed packages and the final production runtime/integration paths ran in this gate.

The 22 HTTP integration cases cover chat ownership and admission, idempotency, history/SSE/search, questions and steering, credit settlement, paid context compaction, explicit approved writes, lost replies and status-only recovery, privacy after permission revocation/deletion, incomplete model responses, and cancellation races. Store/lifecycle tests additionally cover interaction publication during pause/cancel, unknown-billing reconciliation after pause, queue exhaustion, and retention progress past protected artifacts.

The final scratch image was built successfully; [image.json](image.json) identifies the local image. It was exercised in a separate actual Compose stack with PostgreSQL, migration CLI, restricted Docker proxy, API and isolated sandbox. Only issuer/provider HTTPS peers were test servers, with a disposable CA mounted in API.

An authenticated queued request executed JavaScript through the real sidecar and completed. A second run was observed executing JavaScript; the sandbox was stopped and restarted. Readiness changed from 503 to 200, and that same run completed. [compose.json](compose.json) records the result and container confinement: UID 10001, no network, read-only root, no capabilities, no new privileges, 128 MiB and 32 PIDs, with only the Unix socket volume.

Measured SIGTERM: sandbox 493 ms; API 622 ms, exit 0, no OOM. These are local measurements for this fixture, not a guarantee under a lost database or blocked kernel I/O. Unknown inference behavior and the five-second shutdown bound are also covered by dedicated behavioral tests.

`go vet ./...` passed. Pinned golangci-lint 2.14.0 reported zero issues. A Linux-target govulncheck scan reported no known vulnerabilities in the loaded application graph. `go mod verify` and all 362 redistributed notice SHA-256 hashes passed. License and scanner scope are documented in [the dependency audit](../../dependency-audit/2026-10-11-mvp/README.md).

The independent review loops completed in three scopes: durable API/storage/auth (6 passes), engine/modules/sandbox/artifacts (5), and runtime/deployment (4). Findings were verified, fixed and behavior-tested; each final fresh reviewer returned no actionable findings. Staging remained empty throughout the loops.

## Release boundary

Source is ready for operator configuration and installed-system validation. Follow [the runbook](../../running.md) and [configuration support](../../../internal/config/SUPPORT.md). The operator must supply real issuer/audience/roles, encryption key, provider model/capabilities and credit tariffs, explicit disclosure policy, and backup/TLS routing. CI is configured but has not run on GitHub. No branch or image was published and no production server was changed.

The disposable Compose containers, network, volumes and TLS credentials were removed. No production credentials are present in these evidence files.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
