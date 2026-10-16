# Dependencies and updates

[go.mod](../go.mod) and [go.sum](../go.sum) define the Go dependency versions and
checksums. Libraries use normal Go module versions, including pseudo-versions
where releases are unavailable. There is no separate application lock file or
JSON graph to synchronize. Do not add an unreviewed `replace` directive.

## Dependency scopes

The runtime uses pgx, Goose, River core, Huma/ServeMux, JSON Schema, Goja, go-jose,
YAML and the OpenTelemetry SDK. The Responses adapter uses standard HTTP. Tests
use Testify and standard Go test facilities. Check imports and the selected module
graph when assessing an update; a module in the graph is not necessarily linked
into the shipped executable.

River provides PostgreSQL-backed jobs. It does not guarantee exactly-once external
effects. Goja runs in a restricted child process; the JavaScript engine itself is
not an operating-system isolation boundary.

Build versions and pins are in [go.mod](../go.mod), [Dockerfile](../Dockerfile),
[tool installer](../scripts/install-tools.sh) and [CI](../.github/workflows/ci.yml).
The tool installer uses full commits. Base images use digests and CI actions use
commit SHAs. Operator-facing image tags are described in [CI delivery](ci.md).
Native media packages have a separate [package list](../examples/file-worker.packages).

## License policy

Approved licenses are MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, MPL-2.0, ISC,
Unlicense and Goja's specific MIT/Lucent notice. Preserve required notices and
review MPL changes at the covered-file level.

GPL golangci-lint is approved as a development/CI tool, outside the application
binary. Isolated Poppler/FFmpeg CLIs are separately approved for the media
container. Their licenses and corresponding-source requirements are described in
[file-worker distribution](file-worker.md). These exceptions do not approve
arbitrary GPL/AGPL Go libraries.

Review licenses manually when selecting direct dependencies and periodically for
transitive changes. This review is not a CI merge gate. Follow the
[license review procedure](dependency-audit/README.md); preserve required notices
in the repository and shipped images. An earlier audit does not certify a new graph.

## Update procedure

1. Read the diff to `go.mod` and `go.sum`. Inspect changed direct dependencies and
   the selected transitive graph with `go list -m all`.
2. Check upstream release notes, API changes and license changes. Review native
   packages and development tools separately from runtime imports.
3. Run the relevant behavioral/race tests, lint, govulncheck and both Docker builds
   in [QA setup](qa/setup.md). Include the PostgreSQL, identity or sandbox boundary
   affected by the update. Check for skipped tests.
4. Update redistributed notices and corresponding-source material when required.
   Verify the image contents using the [license review procedure](dependency-audit/README.md).
5. Complete the review and PR checks in [Contributing](../CONTRIBUTING.md).

[Dependabot](../.github/dependabot.yml) opens weekly Go, Docker, Compose and Actions
updates. Go minor/patch updates are grouped; major updates are separate.

`govulncheck` checks known Go vulnerability advisories and applicable call paths;
it does not prove that all code or native packages are free of vulnerabilities.
The lint configuration bans deprecated `golang.org/x/crypto/openpgp`. Do not
suppress all `x/crypto` checks to work around that ban.
