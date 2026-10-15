# CI publication and server deployment trigger

Deployment receiver and collector configuration are maintained outside this repository. The receiver must accept `updated-loom`.

The existing CI runs tests, dependency verification, lint and vulnerability checks
on pushes and pull requests. Once these pass, a push to `main` builds and publishes
`linux/amd64` images to Docker Hub. Merging a pull request into `main` produces the
same push event. Other branches and pull requests build locally without publication
or deployment dispatch.

Published tags:

- `complynx/loom:latest`
- `complynx/loom:sha-<full Git commit SHA>`
- `complynx/loom:file-worker-latest` and `complynx/loom:file-worker-sha-<full Git commit SHA>`

Publishing jobs share a concurrency group and do not cancel a running publication.
Before publishing, the job checks the current remote `main` head; an older run is
skipped if a newer commit already exists. This also prevents rerunning an old CI
run from replacing `latest`. Buildx uses the GitHub Actions cache.

## Required GitHub secrets

Set these in `complynx/loom`:

- `DOCKER_HUB_ACCESS_TOKEN`: token for the `complynx` Docker Hub account with write
  access to `complynx/loom`.
- `SERVER_REPO_TOKEN`: token allowed to create repository dispatches in
  `complynx/server_configs`. A fine-grained token needs Contents read/write for
  that target repository; the built-in source repository token cannot dispatch
  across repositories. See the [dispatch action documentation](https://github.com/peter-evans/repository-dispatch#token).

Both secrets are checked before login or publication. Credentials are passed as
secrets, never Docker build arguments. The publication workflow's built-in token
has only `contents: read` permissions. The separate Dependabot metadata workflow
has the scoped write permissions described below.

## Server contract

After the image push succeeds, CI sends `updated-loom` to
`complynx/server_configs`, following the existing `zns-chatbot` pattern. The payload
contains the source SHA, latest image tags and informational published digests. The receiver selects latest from its own operator-managed Compose profile. Both images are published before dispatch. See [startup](startup.md) for serialized rollout and schema requirements. The receiver workflow and production configuration are maintained outside this repository. The trigger does not provision infrastructure or prove readiness.

A successful sender job proves that GitHub accepted the event. Deployment success
must be checked in the receiving server workflow and application readiness. If
publication succeeds and dispatch fails, the image remains published and this job
fails; rerunning it for the current `main` repeats the dispatch after publication.

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

Release tags were resolved through each official GitHub repository. The bundled
Go updates and their license audit are recorded in
[the dependency report](dependency-audit/2026-10-15-bundle/README.md).

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

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
