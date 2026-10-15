# MVP dependency audit, 2026-10-11

This audit covers the implemented Go service, not the earlier dependency candidate. Target: Linux amd64, CGO=0, Go 1.27.2. The local Windows Go tool runs a Linux-target package scan. The separate Linux/Docker behavioral and confinement tests establish runtime behavior; this audit does not replace them.

## Pins and integrity

`go mod verify` passed; see `module-verify.txt`.

Many graph modules belong only to upstream dependency tests, adapters or tools and are not linked into this service. `runtime-imports.txt` records the actual binary dependency packages. An explicit `golang.org/x/crypto v0.56.0` constraint prevents the unused upstream graph from selecting the known vulnerable v0.55.0.

Cached old module metadata can lack VCS origins. Missing hashes were recovered from upstream peeled tags or the GitHub commit API and then checked with `go list -m module@FULL_SHA`. `origin-recovery.json` records a stale decimal v1.4.0 origin: a fresh direct download at the full pinned SHA produced the exact same module and go.mod hashes as the proxy archive.

## Licenses and distribution

`licenses.csv` has 33 runtime/project license entries. `licenses-with-tests.csv` has 36 entries when this project's tests are included. All imported licenses are within the accepted policy: MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, MPL-2.0, ISC and the explicitly accepted Goja MIT/Lucent notice. The counts include nested packages with separate licenses and the project itself.

`notices/` contains runtime license and copyright notices, unchanged MPL-covered River source files required by the scanner's distribution policy, the Go standard library BSD notice, and manually preserved Goja Lucent and V8 notices. `notice-manifest.json` records the files and SHA-256 hashes. Goja ftoa was excluded only from automatic `go-licenses save` because its known, explicitly permitted Lucent notice is classified as unknown by that command; both ftoa notices were copied directly. They remain in the license report.

`module-root-licenses.json` additionally checks root license files for all 171 selected modules with the same Google license classifier. It is a root-file check, not a scan of every nested/native file in unused modules. `manual-license-review.json` records three unshipped graph cases: MIT-0 in segmentio/asm; MIT plus PostgreSQL underlying data in pgerrcode; Apache code plus separately licensed CC-BY-SA documentation in go-digest. Those modules are absent from this project's runtime and test import graphs. The documentation is not redistributed.

The four development tools are pinned and their root licenses were checked in the previous audit. Full transitive/native license audit of the tools remains outside this completed runtime audit. The explicit GPL-3.0 exception applies only to golangci-lint, never to the service binary.

## Known vulnerabilities

`govulncheck v1.8.0 -json ./...` completed successfully with zero module/package/symbol findings for the loaded implemented package graph, after the x/crypto override. `vulnerabilities.jsonl` retains scanner configuration and findings (none); unrelated advisory records are omitted. The database reported last modification 2026-10-08T22:31:09Z.

A separate module-level scan from `cmd/clx-scripter` also emitted zero findings; `vulnerabilities-module.jsonl` retains its configuration. This scanner result concerns the loaded dependency graph. It does not claim that every unused upstream test/tool module in Go's full MVS graph, development-tool binary or native asset has no advisories. The former candidate's deprecated OpenPGP package is not imported; the linter still bans importing it.

No package is declared safe from popularity or scanner output alone. Upstream activity/popularity evidence remains in the previous audit. Pinned updates require commit/content checks, license review, vulnerability scan, behavioral tests and review; there is no automatic merge.

## Commands

Set `GOROOT` to the project-selected `go env GOROOT` before running the previously built go-licenses tool. This avoids treating the newer standard library as external unversioned packages.

```sh
go mod verify
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 govulncheck -json ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go-licenses report ./cmd/clx-scripter/... ./internal/... ./integration/...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go-licenses report --include_tests ./...
```
