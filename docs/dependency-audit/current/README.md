# Current distribution delta

The Dockerfile includes this directory's notices together with the historical
notice directories. The historical directories remain evidence for their own
versions; they are not matching source for a later River release.

## River source

`notices/github.com/riverqueue/*-v0.50.0.zip` contains the five unmodified Go
module source archives for River, riverdriver, riverpgxv5, rivershared and
rivertype. Each archive includes that module's upstream license files and source.
The Go module cache verifies these archives against the selected module sums.
`manifest.json` also records each archive's SHA-256 byte hash.

Both shipped images copy these archives into
`/licenses/dependencies-current/github.com/riverqueue/`. Use them for River
0.50.0 source instead of the older extracted source in the historical bundle.
Extract an archive with a ZIP reader; retain its module/version prefix and all
license files. No extraction is needed to run Loom.

## Metadata scope

`manifest.json` records module versions, upstream commits and shipped notice
hashes. `module-root-licenses.json` records root license bytes and classifications.
The Linux CSV files describe the runtime and test import license scopes.

The 0.50.0 update changes 40 selected graph modules, including dependencies of
unused upstream adapters. The 38 existing changed root license records retain
identical license bytes. Two new graph modules use MIT
(`github.com/molecule-man/go-brrr`) and BSD-3-Clause (`golang.org/x/time`);
neither is in Loom's runtime or test import lists. Root review does not certify
every nested file in an unshipped module. Unchanged records retain the scope of
their earlier review.

Follow the [manual review procedure](../README.md) for later updates. Preserve
upstream license bytes and supply matching source whenever required.
