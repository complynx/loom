# Dependency audit 2026-10-11

Target: Linux amd64 / Docker; server uname returned Linux x86_64. Windows amd64 was checked for development only.

This is a synthetic import graph, not the application module. Copy candidate.go.mod to go.mod, candidate.go.sum to go.sum, and imports.go.txt to main.go in a temporary directory. Do not activate it as the repository root module.

Use Go 1.27.2, GOWORK=off, GOOS=linux, GOARCH=amd64 and CGO_ENABLED=0. Tool source commits and artifact checksums are in ../../../tools/dependencies.lock.json.

Checks completed:

- go mod verify: all modules verified.
- go build ./...: Linux import graph compiled on Go 1.27.2.
- govulncheck -scan package -json ./...: Linux and Windows. One module-level finding, GO-2026-5932; no openpgp packages in go list -deps ./....
- go-licenses report --ignore clx-license-audit ./...: 92 Linux and 93 Windows license entries. Export GOROOT from go env GOROOT when using the separately installed scanner with the automatic Go toolchain, to avoid stdlib misclassification.
- golangci-lint 2.14.0 config verify: exit 0. The Windows release archive SHA-256 matched the official release checksum file.
- Full pgx commit query resolved to canonical v5.11.0, confirming tagged commit canonicalization by Go.

The JSONL files retain scanner config, the relevant OpenPGP advisory and findings; unrelated OSV database records were omitted. No application call-site reachability claim is made from an empty main. The go-licenses scanner reports limits for non-Go/assembly code. Complete tool transitive/native licensing and actual Docker sandbox acceptance remain separate checks.

GitHub repository evidence includes exact counters and activity dates. Moby popularity describes the entire repository, not the split client module. A recent repository push is not proof every release branch is maintained.

Upgrades applied before final audit: Go 1.27.0 to 1.27.2, x/crypto v0.55.0 to v0.56.0, moby/go-archive v0.2.0 to v0.3.0, and its selected moby/sys/user dependency to v0.4.1. All 86 modules, including indirect requirements, have full commit SHAs in the lock file; 4 development tools and Go are pinned there too.
