# QA setup and gates

Use a dedicated Linux/Docker stand, disposable PostgreSQL, test identities and synthetic data. Real IAM contract tests need Zitadel. Ordinary engine tests can use a deterministic provider and HTTP peers. Full integration needs the actual broker and client. Obtain explicit authorization for paid provider calls or destructive production tests.

Before a run, record the source SHA, dirty-source hashes, image IDs, schema version, sanitized settings and resource limits. Identify each owned container and volume. Preserve failures separately from successful repeats. A corrected observer does not make the first failed attempt pass.

Record PASS, FAIL, BLOCKED or SKIPPED with the exact scenario and expected/actual result. Do not infer acceptance from a skipped test or a test on another build. Store results outside source control.

## Disposable PostgreSQL

The commands below use a POSIX shell, Docker and the Go version in [go.mod](../../go.mod).
Race tests also need a C compiler. Run from the repository root. Reserve the
container name, network name and local port for this run; if one is in use, choose another.

```sh
docker network create loom-qa-network
docker run -d --name loom-qa-postgres --network loom-qa-network \
  -p 127.0.0.1:55432:5432 \
  -e POSTGRES_DB=loom_qa -e POSTGRES_USER=loom_qa \
  -e POSTGRES_PASSWORD=disposable-qa-only \
  postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24

for attempt in $(seq 1 30); do
  docker exec loom-qa-postgres pg_isready -U loom_qa -d loom_qa && break
  sleep 1
done
docker exec loom-qa-postgres pg_isready -U loom_qa -d loom_qa
export CLX_TEST_DATABASE_DSN='postgres://loom_qa:disposable-qa-only@localhost:55432/loom_qa?sslmode=disable'
```

Stop if the final readiness command fails. Tests create and drop isolated schemas;
the database role needs permission to create them. Never use a production DSN.
On Windows, run these shell examples in a Linux environment with Docker access,
or use PowerShell assignment (`$env:CLX_TEST_DATABASE_DSN = '...'`) for native Go tests.

## Code checks

Run from the repository root:

```sh
go mod verify
go test -race -parallel=4 -tags=integration -count=1 ./...
sh scripts/install-tools.sh
bin/golangci-lint config verify
bin/golangci-lint run --build-tags=integration ./...
bin/govulncheck ./...
docker build --platform linux/amd64 --target api -t loom:qa .
docker build --platform linux/amd64 --target file-worker -t loom:file-worker-qa .
```

The tool installer defaults to the repository's `bin` directory. Unset an existing
`GOBIN` override before using the paths above, or use the binaries in that override.
On Windows, installed tools have an `.exe` suffix.

For Linux runtime and sandbox paths, use the Docker test wrapper. Its DSN is read
inside the test container: host `localhost` would address that container. For the
database published above, use:

```sh
CLX_TEST_NETWORK=loom-qa-network \
CLX_TEST_DATABASE_DSN='postgres://loom_qa:disposable-qa-only@loom-qa-postgres:5432/loom_qa?sslmode=disable' \
  sh scripts/linux-tests.sh
```

The wrapper defaults to `CLX_TEST_NETWORK=bridge`; the command selects the private
network created above so Docker DNS can resolve the database container. The
wrapper runs race tests and enables the integration tag only when the DSN is set.
It uses shared named module/build cache volumes.

The [CI workflow](../../.github/workflows/ci.yml) runs media tests in the built
worker runtime, using static test binaries and the same media packages and fonts
as `file-runtime`. To run that gate against the disposable database above:

```sh
docker build --platform linux/amd64 --target file-runtime-tests -t loom:file-runtime-tests .
docker run --rm --platform linux/amd64 --network loom-qa-network \
  -e CLX_TEST_DATABASE_DSN='postgres://loom_qa:disposable-qa-only@loom-qa-postgres:5432/loom_qa?sslmode=disable' \
  loom:file-runtime-tests
```

This gate requires the DSN and media CLIs. It runs without the race detector;
the ordinary Linux test job runs race tests separately. Ordinary tests without
these CLIs do not accept the worker boundary. Consult
[integration fixtures](../../integration/README.md) for the fake authority/provider limits.

Inspect verbose test output for skips when verifying a specific boundary. A zero
exit status with missing PostgreSQL or media tools does not prove that boundary.
Use the focused commands in the linked QA pages while iterating; run the complete
relevant gates before completion.

After the run, remove only the container created above and its anonymous volume:

```sh
docker rm -f -v loom-qa-postgres
docker network rm loom-qa-network
unset CLX_TEST_DATABASE_DSN
```

Preserve failure evidence before cleanup. Do not prune shared Docker resources.

For a PR, run an independent review loop before publication, wait for CI, and resolve valid reviewer/bot findings. Use [Contributing](../../CONTRIBUTING.md) for documentation requirements.
