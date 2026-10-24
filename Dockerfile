FROM golang:1.27.3-alpine3.24@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/loom ./cmd/loom
RUN mkdir -p /out/run/clx-sandbox /out/data/artifacts && chmod 0770 /out/run/clx-sandbox /out/data/artifacts
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS file-runtime-base
ENV XDG_CACHE_HOME=/tmp/loom-font-cache
COPY examples/file-worker.packages /licenses/runtime/packages.requested
RUN tr -d '\r' </licenses/runtime/packages.requested >/tmp/packages.requested \
    && apk add --no-cache $(cat /tmp/packages.requested) \
    && mkdir -p /run/loom-files /licenses/runtime \
    && chmod 0770 /run/loom-files && chown 10001:10001 /run/loom-files \
    && apk info --all >/licenses/runtime/packages.txt \
    && apk query --installed --format json --fields name,version,origin,commit,license '*' >/licenses/runtime/packages.json \
    && apk list --installed --manifest | awk '{print $1 "=" $2}' | sort >/licenses/runtime/packages.lock \
    && ffmpeg -L >/licenses/runtime/ffmpeg-license.txt 2>&1 \
    && ffmpeg -buildconf >/licenses/runtime/ffmpeg-buildconf.txt 2>&1 \
    && rm /tmp/packages.requested

FROM file-runtime-base AS animation-renderer-build
RUN apk add --no-cache build-base pkgconf imagemagick-dev rlottie-dev
COPY native/ /src/
RUN cd /src && sh build-animation-renderer.sh && cp loom-animation /usr/local/bin/loom-animation

FROM file-runtime-base AS file-runtime
COPY --from=animation-renderer-build /usr/local/bin/loom-animation /usr/local/bin/loom-animation
COPY native/ /licenses/runtime/loom-animation/

# Static test binaries run with the same media packages as the worker.
FROM build AS file-runtime-test-build
RUN mkdir -p /out/tests \
    && go test -c -tags=integration -o /out/tests/ ./internal/fileproc ./internal/app ./integration
FROM file-runtime AS file-runtime-tests
COPY --from=file-runtime-test-build /out/tests/ /tests/
COPY examples/manifest.json /tests/examples/manifest.json
RUN mkdir -p /tests/internal/fileproc /tests/internal/app /tests/integration
CMD ["sh", "-ec", ": \"${CLX_TEST_DATABASE_DSN:?Set a disposable PostgreSQL DSN}\"; for cli in ffmpeg ffprobe pdfinfo pdftoppm pdftotext loom-animation; do command -v \"$cli\"; done; for package in internal/fileproc internal/app integration; do (cd /tests/$package && /tests/$(basename $package).test -test.v -test.parallel=4 -test.timeout=10m); done"]

# Verified sources gate worker builds; the export target exposes the same bundle.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS file-worker-source-build
COPY --from=file-runtime-base /licenses/runtime/ /licenses/runtime/
COPY examples/file-worker-sources.sh /usr/local/bin/file-worker-sources
RUN --mount=type=cache,target=/var/cache/loom-sources --mount=type=cache,target=/tmp/aports apk add --no-cache abuild git curl jq \
    && tr -d '\r' </usr/local/bin/file-worker-sources >/tmp/file-worker-sources \
    && sh /tmp/file-worker-sources /licenses/runtime/packages.lock /corresponding-source
COPY native/ /corresponding-source/loom-animation/
RUN cd /corresponding-source && find . -type f ! -name SHA256SUMS -exec sha256sum '{}' + >SHA256SUMS
FROM scratch AS file-worker-sources
COPY --from=file-worker-source-build /corresponding-source/ /

FROM file-runtime AS file-worker
COPY --from=build /out/loom /usr/local/bin/loom
COPY docs/dependency-audit/2026-10-11-mvp/notices /licenses/dependencies/
COPY docs/dependency-audit/2026-10-12-otel/notices /licenses/dependencies-otel/
COPY docs/dependency-audit/current/notices /licenses/dependencies-current/
COPY LICENSE /licenses/loom/LICENSE
COPY docs/file-worker.md /licenses/runtime/DISTRIBUTION.md
COPY --from=file-worker-source-build /corresponding-source/ /licenses/corresponding-source/
USER 10001:10001
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/loom"]
CMD ["file-worker", "--socket", "/run/loom-files/processor.sock"]

# Keep the default build and the API runtime independent of media utilities.
FROM scratch AS api
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/loom /usr/local/bin/loom
COPY --chown=10001:10001 --from=build /out/run/ /run/
COPY --chown=10001:10001 --from=build /out/data/ /data/
COPY docs/dependency-audit/2026-10-11-mvp/notices /licenses/dependencies/
COPY docs/dependency-audit/2026-10-12-otel/notices /licenses/dependencies-otel/
COPY docs/dependency-audit/current/notices /licenses/dependencies-current/
COPY LICENSE /licenses/loom/LICENSE
USER 10001:10001
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/loom"]
CMD ["serve"]
