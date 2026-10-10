#!/bin/sh
# Run behavioral tests on the deployment platform. The integration DSN must be
# reachable from the Docker network chosen by CLX_TEST_NETWORK.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
image=golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61
if [ -n "${CLX_TEST_DATABASE_DSN:-}" ]; then
    test_args='-race -tags=integration ./...'
else
    test_args='-race ./...'
fi
docker run --rm --platform linux/amd64 \
    --network "${CLX_TEST_NETWORK:-bridge}" \
    --add-host=host.docker.internal:host-gateway \
    -e GOTOOLCHAIN=local -e CGO_ENABLED=1 -e GOFLAGS=-mod=readonly \
    -e CLX_TEST_DATABASE_DSN="${CLX_TEST_DATABASE_DSN:-}" \
    -v "$root:/src:ro" -v clx-test-modules:/go/pkg/mod -v clx-test-build:/root/.cache/go-build \
    -w /src "$image" go test $test_args
