# Manual dependency license review

Review licenses before selecting a direct dependency and periodically for
transitive updates. This is a manual agent/operator task, not a CI or Dependabot
gate. The approved [license policy](../dependencies.md) applies to new imported
code; historical exceptions do not expand it.

## Inspect the selected graph

Run from the repository root with the Go version in [go.mod](../../go.mod):

```sh
go mod verify
go list -m all
go list -deps ./cmd/loom
go list -deps -test ./...
```

The module graph includes dependencies that may not be linked into the runtime.
Compare it with the previous reviewed graph, then inspect the exact selected
versions of new or changed modules. Review runtime imports, test imports and
separately installed development tools under their distribution scopes. Include
license and notice files, covered source headers and required attribution; a
repository's top-level license label alone is insufficient.

Native media packages are separate from Go modules. Review
[examples/file-worker.packages](../../examples/file-worker.packages), the installed
package metadata and the corresponding-source requirements in
[file-worker distribution](../file-worker.md).

## Preserve distribution material

Keep review logs and command output in private CI artifacts or ignored local
storage. Commit required redistribution notices and source material. Do not
reformat archived upstream source or rewrite license bytes. A nested archive
`go.mod` keeps archived Go source outside the application's package traversal;
retain that boundary.

Dated directories and `current/manifest.json` record earlier reviewed graphs and
file hashes. They do not certify later updates. When the selected graph changes,
review the delta and update the distribution material and byte manifest as needed.
The `current` directory supplies a notice delta, so it does not replace historical
notice directories still copied by the Dockerfile.

## Verify shipped images

Build both targets using [QA setup](../qa/setup.md). Then inspect the actual image
contents. Neither image needs to start or connect to a service for this check:

```sh
docker create --name loom-qa-license-api loom:qa
docker cp loom-qa-license-api:/licenses ./licenses-api.local
docker rm -v loom-qa-license-api
docker create --name loom-qa-license-worker loom:file-worker-qa
docker cp loom-qa-license-worker:/licenses ./licenses-worker.local
docker rm -v loom-qa-license-worker
```

Use unused container names and output paths. The `.local` output directories are
ignored by this repository; preserve or remove only the copies you created.
Compare the three dependency notice directories and Loom license with the
Dockerfile's source paths. The worker must also contain runtime package metadata,
FFmpeg license/build configuration, distribution instructions and
`/licenses/corresponding-source`.

The worker build runs [file-worker-sources.sh](../../examples/file-worker-sources.sh):
it fetches each installed package's Alpine recipe at its recorded commit and
verifies source downloads using `abuild fetch verify`. Check the exported source
bundle's integrity in a Linux shell with `sha256sum`:

```sh
(cd licenses-worker.local/corresponding-source && sha256sum -c SHA256SUMS)
```

A successful checksum check proves bundle integrity against that manifest. It does
not replace review of license obligations or establish that a later dependency
graph has been reviewed.
