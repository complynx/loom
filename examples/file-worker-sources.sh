#!/bin/sh
# Exact installed runtime recipes and verified upstream sources, including notices.
set -eu
lock=$1
destination=$2
alpine_branch=v$(cut -d. -f1,2 /etc/alpine-release)
mkdir -p "$destination/packages" "$destination/recipes" "$destination/sources"
tr -d '\r' <"$lock" >"$destination/packages.lock"
mkdir -p /tmp/aports
git -C /tmp/aports init -q
git -C /tmp/aports config remote.origin.url https://github.com/alpinelinux/aports.git
git -C /tmp/aports config remote.origin.promisor true
git -C /tmp/aports config remote.origin.partialclonefilter blob:none

# Wait for every source job, including when another job fails verification.
source_pids=
wait_sources() {
    failed=0
    for pid in $source_pids; do
        wait "$pid" || failed=1
    done
    source_pids=
    return "$failed"
}

# Each origin has its own recipe, download cache and output directory.
collect_source() (
    set -eu
    directory=$1
    origin=$2
    commit=$3
    cached_source="/var/cache/loom-sources/$origin-$commit"
    source_directory="$destination/sources/$origin-$commit"
    mkdir -p "$cached_source" "$source_directory"
    (cd "$directory" && SRCDEST="$cached_source" DISTFILES_MIRROR=https://distfiles.alpinelinux.org/distfiles/$alpine_branch abuild -F fetch verify </dev/null)
    cp -a "$cached_source/." "$source_directory/"
)

jq -r '.[] | [.name, .version, .origin, .commit, .license] | join("|")' /licenses/runtime/packages.json |
(
source_count=0
trap 'wait_sources' EXIT
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
    collect_source "$directory" "$origin" "$commit" &
    source_pids="$source_pids $!"
    source_count=$((source_count + 1))
    if [ "$source_count" -eq 20 ]; then
        wait_sources
        source_count=0
    fi
done
wait_sources
)
cp /licenses/runtime/*.txt /licenses/runtime/packages.json "$destination/"
cp /licenses/runtime/packages.lock "$destination/runtime-packages.lock"
(cd "$destination" && find . -type f ! -name SHA256SUMS -exec sha256sum '{}' + >SHA256SUMS)
