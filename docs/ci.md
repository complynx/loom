# CI publication and server deployment trigger

Repository rename: use the coordinated [Loom receiver/Alloy patch](validation/2026-10-12-loom-rename-server.patch). Historical QA patches preserve their original names. Deploy the updated receiver before enabling `updated-loom` dispatch.

The existing CI runs tests, dependency verification, lint and vulnerability checks
on pushes and pull requests. Once these pass, a push to `main` builds and publishes
`linux/amd64` images to Docker Hub. Merging a pull request into `main` produces the
same push event. Other branches and pull requests build locally without publication
or deployment dispatch.

Published tags:

- `complynx/loom:latest`
- `complynx/loom:sha-<full Git commit SHA>`

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
secrets, never Docker build arguments. The workflow's built-in token has only
`contents: read` permissions.

## Server contract

After the image push succeeds, CI sends `updated-loom` to
`complynx/server_configs`, following the existing `zns-chatbot` pattern. The payload
contains the source SHA, immutable commit tag and published image digest. The local
server receiver maps this event to `loom` and invokes its existing rollout
script. That script uses the Compose profile's configured image; it does not consume
the payload digest or commit tag to select an image.

The receiver change must reach `server_configs`' default branch before GitHub
accepts this event type. Server rollout also requires `configs/loom` to be
configured on the server with its Compose files, production settings, secrets,
volumes, migrations and networks. That profile does not yet exist in the local
server repository. The CI trigger alone does not create it or establish production
readiness. The monitoring network from `docs/observability.md` is another deployment
prerequisite. No dispatch or production deployment was executed during this change.

A successful sender job proves that GitHub accepted the event. Deployment success
must be checked in the receiving server workflow and application readiness. If
publication succeeds and dispatch fails, the image remains published and this job
fails; rerunning it for the current `main` repeats the dispatch after publication.

## Pinned actions

All actions use full Git commit SHAs. The existing Dependabot `github-actions`
configuration checks for updates weekly.

| Action | Release | License |
| --- | --- | --- |
| docker/setup-buildx-action | v4.4.1 | Apache-2.0 |
| docker/login-action | v4.6.0 | Apache-2.0 |
| docker/build-push-action | v7.4.0 | Apache-2.0 |
| peter-evans/repository-dispatch | v4.0.1 | MIT |

Release tags were resolved through each official GitHub repository when preparing
this change. No application Go dependencies were added.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
