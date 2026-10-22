# CI and image publication

Installation-specific deployment workflows and collector configuration belong to the operator.

The existing CI runs tests, dependency verification, lint and vulnerability checks
on pushes to `main`, pull requests and manual workflow dispatch. Feature-branch
pushes do not start a second run; open a pull request or use manual dispatch to
check a branch. Once these pass, a push to `main` builds and publishes
`linux/amd64` images to Docker Hub. Merging a pull request into `main` produces the
same push event. Manual runs on other branches and pull requests build locally without publication
or deployment dispatch.

Four independent jobs run in parallel:

- `lint`: module verification, lint configuration, Go lint and vulnerability checks.
- `test`: race tests for `cmd`, `internal` and `migrations`, without PostgreSQL.
- `build`: the Linux binary and, outside a push to `main`, both container images.
- `integration`: the full race suite with PostgreSQL and media tests in the built worker runtime.

Some internal packages contain both unit and database tests. The integration job
runs all packages so these database tests remain covered; unit tests in these
packages also run there.

Publication waits for all four jobs.

Published tags:

- `complynx/loom:latest`
- `complynx/loom:sha-<full Git commit SHA>`
- `complynx/loom:file-worker-latest` and `complynx/loom:file-worker-sha-<full Git commit SHA>`

Publishing jobs share a concurrency group and do not cancel a running publication.
Before publishing, the job checks the current remote `main` head; an older run is
skipped if a newer commit already exists at that check. A newer commit can arrive
while a build is running; the check does not cancel an in-progress publication.
Buildx uses the GitHub Actions cache.

The integration job exports the `file-runtime-tests` build cache under the
`media-tests` scope. Publication imports that cache on its own runner, loads the
test image and runs the media tests again before it pushes images. The cache
avoids recompiling unchanged test binaries; it does not replace test execution.
API and worker caches keep separate scopes (`buildkit` and `file-worker`).

Both container targets use [docker-bake.hcl](../docker-bake.hcl). Bake builds and
publishes them in parallel, sharing common build stages within one builder. PR
checks use the same targets with `--load` and do not push images. To build both
images locally, run `docker buildx bake --file docker-bake.hcl --load`. The default
image name is `loom` and revision is `local`; set `IMAGE` and `REVISION` to change
the tags. CI publication sets them to the registry image name and full commit SHA.

## Maintainer publication workflow

The [upstream workflow](../.github/workflows/ci.yml) is maintainer infrastructure, not a required installation step. It uses `DOCKER_HUB_ACCESS_TOKEN` for image publication and `SERVER_REPO_TOKEN` for a repository-dispatch hook after both images are published. Both secrets are checked before publication; credentials are not Docker build arguments. The workflow's GitHub token has `contents: read`. The registry and dispatch secrets have separate permissions configured by the maintainer.

To publish from a fork, select your own registry account/tags and either replace the hook target and credentials or remove the hook and its secret check. Editing documentation alone does not reconfigure the workflow. To install published images, no GitHub token or deployment repository is required.

## Deployment integration

Any deployment system can pull the published API and file-worker images. Keep their source revisions aligned, preserve the database/session keys and follow [startup](startup.md) for migrations and readiness. Choose latest or commit tags according to the installation's release policy; record the resolved image IDs for rollback.

For a repository-dispatch integration, the upstream hook event is updated-loom. Its JSON payload contains sha, image, digest, file_worker_image and file_worker_digest. A receiving workflow must validate the trigger and select the desired images in its own deployment profile. The hook does not provision a host or prove readiness. Verify the receiver result and application health separately.

Parallel publication is not atomic: one image can publish before the other build
fails. A build failure prevents the deployment hook. The hook runs only after
both targets succeed and both image digests are present in the Bake metadata.
If both images publish but the hook fails, both remain published and the job
fails. Check both digests before rollout. Re-running publication for the current
main may send the event again; receivers should tolerate duplicates. Older main
runs are skipped as described above.

## Pinned actions

All actions use full Git commit SHAs. The existing Dependabot `github-actions`
configuration checks for updates weekly.

| Action | Release | License |
| --- | --- | --- |
| actions/checkout | v7.0.1 | MIT |
| actions/setup-go | v7.0.0 | MIT |
| docker/setup-buildx-action | v4.4.1 | Apache-2.0 |
| docker/login-action | v4.6.0 | Apache-2.0 |
| docker/build-push-action | v7.4.0 | Apache-2.0 |
| docker/bake-action | v7.5.0 | Apache-2.0 |
| peter-evans/repository-dispatch | v4.0.1 | MIT |

Version comments identify reviewed releases; the workflow SHA is authoritative. Review new dependencies with the manual procedure below.

## Dependency updates

Dependabot groups minor/patch Go updates and minor/patch Actions updates separately.
Major updates remain separate PRs. It updates `go.mod` and `go.sum` directly;
normal CI verifies module checksums, runs behavioral/race tests, lint, vulnerability
scanning and both container builds. Green checks are required before a human merge.
No automatic merge is enabled.

Alpine base images remain pinned by tag and digest under Dependabot. APK packages
resolve without version pins from that Alpine branch. The media test target
inherits the built worker runtime instead of installing a second package set in
a Go image. Installed versions and matching sources are recorded in the worker;
see [file-worker distribution](file-worker.md).

The publication job repeats the media gate against its own resolved runtime
before either image is pushed. It builds the test target and worker with the
same Buildx builder and reuses the tested `file-runtime` layer. This prevents a
different runner's package resolution or cache from bypassing the media tests.

## Manual license review

Agents/operators review licenses periodically and before selecting new direct
dependencies. License review is not a CI gate. Keep the approved license policy
and retain required notices and corresponding-source material in distributed images.
See [the review procedure](dependency-audit/README.md).
