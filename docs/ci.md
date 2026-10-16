# CI and image publication

Installation-specific deployment workflows and collector configuration belong to the operator.

The existing CI runs tests, dependency verification, lint and vulnerability checks
on pushes, pull requests and manual workflow dispatch. Once these pass, a push to `main` builds and publishes
`linux/amd64` images to Docker Hub. Merging a pull request into `main` produces the
same push event. Other branches and pull requests build locally without publication
or deployment dispatch.

Published tags:

- `complynx/loom:latest`
- `complynx/loom:sha-<full Git commit SHA>`
- `complynx/loom:file-worker-latest` and `complynx/loom:file-worker-sha-<full Git commit SHA>`

Publishing jobs share a concurrency group and do not cancel a running publication.
Before publishing, the job checks the current remote `main` head; an older run is
skipped if a newer commit already exists at that check. A newer commit can arrive
while a build is running; the check does not cancel an in-progress publication.
Buildx uses the GitHub Actions cache.

## Maintainer publication workflow

The [upstream workflow](../.github/workflows/ci.yml) is maintainer infrastructure, not a required installation step. It uses `DOCKER_HUB_ACCESS_TOKEN` for image publication and `SERVER_REPO_TOKEN` for a repository-dispatch hook after both images are published. Both secrets are checked before publication; credentials are not Docker build arguments. The workflow's GitHub token has `contents: read`. The registry and dispatch secrets have separate permissions configured by the maintainer.

To publish from a fork, select your own registry account/tags and either replace the hook target and credentials or remove the hook and its secret check. Editing documentation alone does not reconfigure the workflow. To install published images, no GitHub token or deployment repository is required.

## Deployment integration

Any deployment system can pull the published API and file-worker images. Keep their source revisions aligned, preserve the database/session keys and follow [startup](startup.md) for migrations and readiness. Choose latest or commit tags according to the installation's release policy; record the resolved image IDs for rollback.

For a repository-dispatch integration, the upstream hook event is updated-loom. Its JSON payload contains sha, image, digest, file_worker_image and file_worker_digest. A receiving workflow must validate the trigger and select the desired images in its own deployment profile. The hook does not provision a host or prove readiness. Verify the receiver result and application health separately.

The images publish in sequence. If the file-worker build fails after the API image publishes, the API tags can already have changed and no hook is sent. If both images publish but the hook fails, both remain published and the job fails. Check both digests before rollout. Re-running publication for the current main may send the event again; receivers should tolerate duplicates. Older main runs are skipped as described above.

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
| peter-evans/repository-dispatch | v4.0.1 | MIT |

Version comments identify reviewed releases; the workflow SHA is authoritative. Review new dependencies with the manual procedure below.

## Dependency updates

Dependabot groups minor/patch Go updates and minor/patch Actions updates separately.
Major updates remain separate PRs. It updates `go.mod` and `go.sum` directly;
normal CI verifies module checksums, runs behavioral/race tests, lint, vulnerability
scanning and both container builds. Green checks are required before a human merge.
No automatic merge is enabled.

## Manual license review

Agents/operators review licenses periodically and before selecting new direct
dependencies. License review is not a CI gate. Keep the approved license policy
and retain required notices and corresponding-source material in distributed images.
See [the review procedure](dependency-audit/README.md).
