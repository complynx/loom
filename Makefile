.PHONY: test race integration build lint fmt lock-check lock-update
test:
	go test ./...
race:
	CGO_ENABLED=1 go test -race ./...
integration:
	test -n "$(CLX_TEST_DATABASE_DSN)"
	go test -parallel=4 -tags=integration ./...
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/loom ./cmd/loom
lint:
	golangci-lint run --build-tags=integration ./...
fmt:
	golangci-lint fmt ./cmd/... ./internal/... ./integration/...
lock-check:
	python3 scripts/dependency-lock.py --check
lock-update:
	python3 scripts/dependency-lock.py --update
