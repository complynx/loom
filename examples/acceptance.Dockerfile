FROM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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
