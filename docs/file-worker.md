# Isolated file worker

The API streams source blobs to the worker over a private Unix socket. The worker
returns bounded JSON and streams derived files back to the API. JavaScript sees
artifact handles and bounded excerpts, not complete source files. Provider calls
and user credentials remain in the API process.

Build the API with the default Docker target. Build the media worker separately:

```sh
docker build --target file-worker -t loom:file-worker .
```

The worker contains Poppler 25.12.0-r1 and FFmpeg 8.1.2-r0. The pinned Alpine image
and `examples/file-worker.packages` fix the complete runtime package set.
The runtime includes Roboto 3.005-r0 and Roboto Mono 3.000-r0 (Apache-2.0)
for unembedded PDF text. Fontconfig substitutes these installed fonts; this
preserves visible text but does not promise the original font's exact metrics.
This is a bounded fallback set, not complete CJK, emoji or specialist-font
coverage. Embed required fonts when exact appearance matters.
Fontconfig writes any runtime cache under `/tmp/loom-font-cache`, inside the
worker's bounded temporary filesystem; it does not require a writable rootfs.

`pdf.render` rejects Poppler's unresolved-font diagnostic even when the CLI exits
zero. A missing-font result produces no derived artifact. Actually blank and
image-only pages remain valid. CI decodes a synthetic Helvetica PDF's PNG and
checks glyph pixels, rather than only checking PNG/MIME metadata.
FFmpeg's Alpine build enables GPL and version-3 components. Do not describe this
image as LGPL-only. The API image remains separate and contains no media CLI.

Merge `examples/file-worker.config.yaml` into the API configuration. Overlay
`examples/file-worker.compose.yaml` on the deployment compose file and supply an
immutable `LOOM_FILE_WORKER_IMAGE` digest. Its API service name is `loom`; adjust
that name to the actual deployment. The socket volume must belong to UID/GID
10001. A newly created Docker volume inherits this ownership from the worker
image. Check ownership explicitly when reusing an existing volume.

The worker has no network, secrets, Docker socket, or artifact-volume mount. It
runs as UID 10001 with a read-only root filesystem, no capabilities, 128 PIDs,
512 MiB memory, one CPU, and 128 MiB temporary storage. These limits can reject
large or malformed documents. Requests have their own bounded timeout and
output limits. A worker failure must not trigger an automatic paid transcription
retry.

## Public image distribution

An isolated CLI is not a license exemption. Public distribution of the worker
must satisfy the licenses of Poppler, FFmpeg, and their dependencies. Keep Loom's
license separate from third-party notices. The runtime image records installed
package notices, FFmpeg's license and build configuration under `/licenses`.

The worker image includes its matching source bundle under
`/licenses/corresponding-source`. Building `file-worker` requires successful
source collection and checksum verification. Existing CI can publish the image
without a separate source hosting service. A changing upstream URL or a promise
to provide source later is not this release procedure.

The build reads the exact installed package metadata and fetches each origin's
APKBUILD and patches at its recorded full aports commit. It downloads and verifies
source archives from the official Alpine distfiles mirror first, with upstream
fallback. APKBUILD checksum verification is unchanged for every runtime origin.
The bundle preserves upstream license
and copyright material inside those archives. The bundle includes the package
lock, recipes, patches, upstream archives, and checksums. It can be large,
including compiler/runtime library sources. Build tools remain in a separate
stage and are not installed into the worker image.

For inspection or a separate mirror, export the same bundle:

```sh
docker build --target file-worker-sources --output type=tar,dest=management.local/worker-sources.tar .
```

Review the bundle for complete source, license text, patches, and build
instructions before the first public release and after changing runtime
packages. This automated bundle is release input, not legal certification.

Changing `packages.lock` requires rerunning the worker behavior tests, source
export, and license review. If an old pinned APK disappears from Alpine's mirror,
use an archived verified package/source snapshot or make an explicit reviewed
upgrade; do not silently remove version pins.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
