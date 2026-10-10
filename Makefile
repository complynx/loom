.PHONY: test race integration build lint lock-check lock-update
test:
	go test ./...
race:
	CGO_ENABLED=1 go test -race ./...
integration:
	test -n "$(CLX_TEST_DATABASE_DSN)"
	go test -tags=integration ./...
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/clx-scripter ./cmd/clx-scripter
lint:
	golangci-lint run ./...
lock-check:
	python3 scripts/dependency-lock.py --check
lock-update:
	python3 scripts/dependency-lock.py --update
