# Standard-font PDF render acceptance (issue #4)

## Cause and correction

The old locked worker contained fontconfig/freetype but no actual fonts.
Poppler extracted text correctly and rendered a PNG with zero dark pixels,
while reporting an unresolved Helvetica font on stderr and exiting zero.

The lock now includes font-roboto=3.005-r0 and font-roboto-mono=3.000-r0,
plus their exact MIT dependencies libfontenc=1.1.9-r0 and mkfontscale=1.2.4-r0.
Both font packages declare Apache-2.0 in authoritative Alpine installed metadata.
The existing source gate includes each package's exact aports recipe, verified
source archive, patches and notices. Roboto's verified archive includes
LICENSE.txt; font files and upstream archives retain their embedded notices.
Fontconfig runtime cache uses /tmp/loom-font-cache within the bounded tmpfs.

pdftoppm's observed missing-font diagnostic now returns a constant error even
when the process exits zero. pdfRender returns no derived outputs on that error.
Raw stderr and document-derived font names are not included in that error.

## Verified

- Baseline regression: synthetic unembedded Helvetica PDF text extraction passed,
  but rendered PNG had 0 dark pixels and the new glyph-content assertion failed.
- Corrected PDF tests passed in a read-only/offline runtime; PNG decoding and
  at least 100 dark pixels are required for the text-only fixture.
- Empty fontconfig: unresolved-font rendering failed with no returned artifact.
- An actually blank page still rendered successfully with empty fontconfig.
- Full internal/fileproc Linux behavioral suite with race detector passed.
- Golden lint for internal/fileproc reported 0 issues.
- Independent textual review: three fresh passes, no actionable findings.
  Staging remained empty until all reviews completed.
- Full worker target built through the unchanged source/checksum gate.
- Final native worker image: sha256:b4d50cdcf9671cb9ad445f186ba2b05ec0405dc55aad36b49c1574ca0bf4fe18.
- Native Unix-socket client rendered Helvetica, Times-Roman and Courier:
  3415, 3026 and 2453 dark pixels respectively. Helvetica PNG was visually checked.
- Native worker ran as UID 10001, no network, read-only rootfs, no capabilities,
  no-new-privileges, 128 PIDs, 512 MiB memory, one CPU and 128 MiB temporary storage.
  Fontconfig worked without writable-cache warnings.
- Final image also passed PDF happy paths, unresolved-font rejection, valid blank
  page, Unicode pagination and boundary-search tests under those runtime limits.

## Boundaries

This is font substitution, not a guarantee of the original font metrics or every
glyph. No complete CJK/emoji/specialist-font set was added. Embed fonts for exact
appearance. Pixel inspection is a synthetic acceptance assertion, not a blanket
runtime rejection of white pages. Only the observed Poppler diagnostic is checked;
this is not a general detector for every malformed PDF or missing glyph.

The actual Zinusya caller flow was not rerun. Native socket/CLI processing was
verified without production mutations, real personal documents or paid providers.
Owned QA containers use --rm and are stopped/removed after acceptance; the sibling
Shuttle/Zitadel stand is retained.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
