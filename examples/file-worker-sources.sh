#!/bin/sh
# Exact installed runtime recipes and verified upstream sources, including notices.
set -eu
lock=$1
destination=$2
mkdir -p "$destination/packages" "$destination/recipes" "$destination/sources"
tr -d '\r' <"$lock" >"$destination/packages.lock"
mkdir -p /tmp/aports
git -C /tmp/aports init -q
git -C /tmp/aports config remote.origin.url https://github.com/alpinelinux/aports.git
git -C /tmp/aports config remote.origin.promisor true
git -C /tmp/aports config remote.origin.partialclonefilter blob:none
jq -r '.[] | [.name, .version, .origin, .commit, .license] | join("|")' /licenses/runtime/packages.json |
while IFS='|' read -r name version origin commit license; do
    [ -n "$origin" ] && [ -n "$commit" ]
    printf '%s\n' "$name=$version" "$origin" "$commit" "$license" >"$destination/packages/$name.txt"
    directory="$destination/recipes/$origin-$commit"
    [ ! -d "$directory" ] || continue
    git -C /tmp/aports fetch -q --filter=blob:none --depth=1 origin "$commit" </dev/null
    recipe=
    for repository in main community testing; do
        if git -C /tmp/aports cat-file -e "$commit:$repository/$origin/APKBUILD" 2>/dev/null; then
            recipe=$repository/$origin
            break
        fi
    done
    [ -n "$recipe" ]
    mkdir -p "$directory"
    git -C /tmp/aports archive "$commit:$recipe" | tar -x -C "$directory"
    printf '%s\n' "$commit $recipe" >"$directory/PROVENANCE"
    cached_source="/var/cache/loom-sources/$origin-$commit"
    source_directory="$destination/sources/$origin-$commit"
    mkdir -p "$cached_source" "$source_directory"
    (cd "$directory" && SRCDEST="$cached_source" DISTFILES_MIRROR=https://distfiles.alpinelinux.org/distfiles/v3.24 abuild -F fetch verify </dev/null)
    cp -a "$cached_source/." "$source_directory/"
done
cp /licenses/runtime/*.txt /licenses/runtime/packages.json "$destination/"
cp /licenses/runtime/packages.lock "$destination/runtime-packages.lock"
(cd "$destination" && find . -type f ! -name SHA256SUMS -exec sha256sum '{}' + >SHA256SUMS)
