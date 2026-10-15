#!/bin/sh
# Build development tools with the current toolchain from reviewed full commits.
set -eu
export GOTOOLCHAIN=local
export GOBIN="${GOBIN:-$(pwd)/bin}"
mkdir -p "$GOBIN"
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@114493f9b3e7257d29e4130f2b4a4aadefbb6845
go install golang.org/x/vuln/cmd/govulncheck@709015412431dd2b5b28a53c06c70bc02d49074c
