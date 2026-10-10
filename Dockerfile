FROM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/clx-scripter ./cmd/clx-scripter
RUN mkdir -p /out/run/clx-sandbox /out/data/artifacts && chmod 0770 /out/run/clx-sandbox /out/data/artifacts
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/clx-scripter /usr/local/bin/clx-scripter
COPY --chown=10001:10001 --from=build /out/run/ /run/
COPY --chown=10001:10001 --from=build /out/data/ /data/
COPY docs/dependency-audit/2026-10-11-mvp/notices /licenses/dependencies/
COPY LICENSE /licenses/clx-scripter/LICENSE
USER 10001:10001
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/clx-scripter"]
CMD ["serve"]
