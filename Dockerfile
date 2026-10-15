FROM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/loom ./cmd/loom
RUN mkdir -p /out/run/clx-sandbox /out/data/artifacts && chmod 0770 /out/run/clx-sandbox /out/data/artifacts
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS file-runtime
ENV XDG_CACHE_HOME=/tmp/loom-font-cache
COPY examples/file-worker.packages /licenses/runtime/packages.lock
RUN tr -d '\r' </licenses/runtime/packages.lock >/tmp/packages.lock \
    && apk add --no-cache $(cat /tmp/packages.lock) \
    && mkdir -p /run/loom-files /licenses/runtime \
    && chmod 0770 /run/loom-files && chown 10001:10001 /run/loom-files \
    && apk info --all >/licenses/runtime/packages.txt \
    && apk query --installed --format json --fields name,version,origin,commit,license '*' >/licenses/runtime/packages.json \
    && ffmpeg -L >/licenses/runtime/ffmpeg-license.txt 2>&1 \
    && ffmpeg -buildconf >/licenses/runtime/ffmpeg-buildconf.txt 2>&1 \
    && rm /tmp/packages.lock

# Verified sources gate worker builds; the export target exposes the same bundle.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS file-worker-source-build
COPY --from=file-runtime /licenses/runtime/ /licenses/runtime/
COPY examples/file-worker-sources.sh /usr/local/bin/file-worker-sources
RUN --mount=type=cache,target=/var/cache/loom-sources --mount=type=cache,target=/tmp/aports apk add --no-cache abuild git curl jq \
    && tr -d '\r' </usr/local/bin/file-worker-sources >/tmp/file-worker-sources \
    && sh /tmp/file-worker-sources /licenses/runtime/packages.lock /corresponding-source
FROM scratch AS file-worker-sources
COPY --from=file-worker-source-build /corresponding-source/ /

FROM file-runtime AS file-worker
COPY --from=build /out/loom /usr/local/bin/loom
COPY docs/dependency-audit/2026-10-11-mvp/notices /licenses/dependencies/
COPY docs/dependency-audit/2026-10-12-otel/notices /licenses/dependencies-otel/
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
COPY LICENSE /licenses/loom/LICENSE
USER 10001:10001
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/loom"]
CMD ["serve"]
