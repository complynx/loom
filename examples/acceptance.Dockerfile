FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/clx-acceptance ./cmd/clx-acceptance
RUN mkdir -p /out/data && chmod 0770 /out/data
FROM scratch
COPY --from=build /out/clx-acceptance /usr/local/bin/clx-acceptance
COPY --chown=10001:10001 --from=build /out/data /data
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/clx-acceptance"]
