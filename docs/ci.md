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
secrets, never Docker build arguments. The publication workflow's built-in token
has only `contents: read` permissions. The separate Dependabot metadata workflow
has the scoped write permissions described below.

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
| actions/checkout | v7.0.1 | MIT |
| actions/setup-go | v7.0.0 | MIT |
| docker/setup-buildx-action | v4.4.1 | Apache-2.0 |
| docker/login-action | v4.6.0 | Apache-2.0 |
| docker/build-push-action | v7.4.0 | Apache-2.0 |
| peter-evans/repository-dispatch | v4.0.1 | MIT |

Release tags were resolved through each official GitHub repository. The bundled
Go updates and their license audit are recorded in
[the dependency report](dependency-audit/2026-10-15-bundle/README.md).

## Dependabot metadata refresh

Dependabot groups minor/patch Go updates and minor/patch Actions updates separately.
Major updates remain separate PRs. No automatic merge is enabled. Green checks
remain required before a human merge.

Go PRs need full commit pins in addition to go.mod and go.sum.
`dependabot-refresh.yml` refreshes pins without relaxing the checks:

1. `pull_request_target` runs reviewed scripts from the exact trusted base SHA.
   It accepts only open, same-repository Dependabot Go branches. Only module
   files and tools/dependencies.lock.json may differ. Renames from other paths
   are rejected. The workflow cannot change license reports or notices.
2. It reads go.mod/go.sum as data at the PR head SHA. It rejects replacement,
   tool, toolchain and other unsupported directives. It does not check out or
   execute PR scripts. The Go toolchain stays fixed and dependency caches are not
   uploaded from this privileged workflow.
3. With no token in subprocess environments, it resolves and verifies full commit
   pins. License auditing is a separate manual task and does not block CI or
   Dependabot refreshes.
4. It rechecks the PR head and base, then commits only allowlisted metadata through
   the Git API. A non-force update preserves the original PR parent and refuses a
   concurrent head change. The token has Contents write, Pull requests read and
   Actions write permissions, only in this job. It never receives publishing secrets.
5. GitHub suppresses normal push/PR triggers for GITHUB_TOKEN commits, so the job
   explicitly dispatches the same full CI on the updated branch. This includes
   race/integration tests, lock checks, lint, vulnerability scanning and
   both container builds. Dispatch runs cannot publish or deploy.

The repository must permit the scoped GITHUB_TOKEN writes. The workflows must
first be merged into the default branch. Local behavioral tests cover the GitHub
API flow. The first live refresh on PR #11 resolved 239 commit pins, then stopped
at the former license gate before publication. A complete live pin-only
commit/dispatch still needs verification after this policy reaches main.
A stale base or a concurrent push fails safely: update
the PR branch and rerun the workflow. Failed refreshes leave the PR unmergeable
until its ordinary full CI passes with correct metadata.

For a manual pin refresh, review go.mod/go.sum and run `make lock-update`.

## Manual license audit

Agents/operators review licenses periodically and when selecting new direct
dependencies. CI does not scan licenses or require the latest dependency graph
to match a license snapshot. This policy does not change the approved licenses.

Keep `make audit-update` and `make audit-check` for manual use. Install the pinned
go-licenses tool from tools/dependencies.lock.json before an update. New license
text needs a reviewed dated root-license snapshot first; commit the refreshed
reports and notices after review. See [the audit policy](dependency-audit/README.md).
Historical snapshots stay unchanged. Docker images still ship the historical
notices plus the most recently reviewed current delta; CI does not certify their
coverage of dependency changes since that review. The file-worker source/checksum
packaging checks remain unchanged.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
