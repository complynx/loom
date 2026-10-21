# Isolated file worker

The API streams source files to a private Unix socket. The worker returns bounded
JSON and streams derived files back to the API. JavaScript receives handles and
bounded excerpts. Provider calls and user credentials stay in the API process.
See [File tools](file-tools.md) for the agent-facing interface.

## Build and connect

Build the API with the default Docker target. Build the worker separately:

```sh
docker build --target file-worker -t loom:file-worker .
```

Merge [the worker config fragment](../examples/file-worker.config.yaml) into the
API configuration. Overlay [the Compose fragment](../examples/file-worker.compose.yaml)
on the deployment Compose file and set `LOOM_FILE_WORKER_IMAGE` to the immutable
digest of your worker image. The example API service name is `loom`; rename it
in the overlay if your service has another name. Transcription remains disabled
until you configure and enable it separately.

Both containers mount the socket volume at `/run/loom-files`; only the API mount
is read-only. The volume must belong to UID/GID 10001. A new Docker volume inherits
this ownership from the worker image. Check ownership when reusing a volume:
a mismatched owner prevents socket creation or access.

The supplied Compose fragment gives the worker no network, secrets, Docker socket
or artifact-volume mount. It runs as UID/GID 10001 with a read-only root filesystem,
no capabilities, 128 PIDs, 512 MiB memory, one CPU and a 128 MiB `/tmp` filesystem.
These are deployment controls supplied by the example; preserve them when adapting
it. The worker streams one source file per request and uses temporary local files
only for that job. Jobs also have bounded input, output, concurrency and timeouts;
see [Bounds and visibility](file-tools/transcription-limits.md#bounds-and-visibility).
A worker failure must not trigger an automatic paid transcription retry.

## PDF rendering and media packages

The worker installs Poppler, FFmpeg and fallback fonts from the repositories of
the pinned stable Alpine image. The [requested package list](../examples/file-worker.packages)
contains package names without version pins. Dependabot updates the Alpine image
tag and digest. The API image is separate and contains no media CLI.

Roboto and Roboto Mono (Apache-2.0) provide fallback fonts for
unembedded PDF text. Fontconfig substitutes installed fonts; the result can have
different metrics from the original. This set does not provide complete CJK,
emoji or specialist-font coverage. Embed required fonts when exact appearance
matters. Runtime font caches use `/tmp/loom-font-cache` inside the bounded temporary
filesystem; a writable root filesystem is unnecessary.

PDF rendering rejects Poppler's unresolved-font diagnostic even if the CLI exits
zero. That failure returns no rendered artifact. Blank and image-only pages remain
valid. If rendering fails, check the source fonts and worker limits before retrying
with a smaller page range.

## Public image distribution

FFmpeg's Alpine build enables GPL and version-3 components; the image is not
LGPL-only. Process isolation does not remove third-party distribution obligations.
Keep Loom's license separate from third-party notices. The runtime image records
installed package metadata, FFmpeg's license and build configuration under
`/licenses/runtime`, with Loom and dependency notices elsewhere under `/licenses`.

The worker image includes a matching source bundle at
`/licenses/corresponding-source`. Building `file-worker` requires successful source
collection and checksum verification. Distribute that bundle with the image; a
changing upstream URL or a promise to supply source later is not this build's
source distribution procedure.

The build reads exact installed package metadata. For each package origin, it
fetches the APKBUILD and patches at the recorded full aports commit. It downloads
source archives using Alpine's official distfiles mirror with upstream fallback
and verifies the APKBUILD checksums. The bundle retains upstream archives with
their license and copyright material, package metadata, lock files, recipes,
patches and a `SHA256SUMS` manifest. Sources can be large, including compiler and
runtime library sources. Source-collection tools stay in a separate build stage.

To inspect or mirror the same bundle, export it from the repository root:

```sh
docker build --target file-worker-sources --output type=tar,dest=worker-sources.tar .
```

Before a release, check that the bundle contains the complete source, license
texts, patches and build instructions required for the distributed runtime.
The automated bundle is release input, not legal certification.

The build records the complete installed package set with exact versions in
`/licenses/runtime/packages.lock`, plus package metadata and the matching source
bundle. This lock is build output, not an installation constraint. An uncached
build can select newer packages from the same Alpine branch; use `--no-cache` to
refresh cached installation layers. Retain image digests for exact rollback.

CI runs media behavior tests in the `file-runtime-tests` target, which inherits
the worker's `file-runtime` layer. Static Go test binaries are added without
installing a compiler or changing the runtime package set. The ordinary Linux
test job separately runs the race detector.

Changes to the requested package list or installed package set require worker
behavior tests, a fresh source export and a license review before distribution.
