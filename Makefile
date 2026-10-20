.PHONY: build test test-integration vet lint fmt vuln up down

build:
	go build ./...

test:
	go test ./...

test-integration:
	go test -tags integration -count=1 ./internal/...

# E2E: compose stand runs follow the test/e2e/E2E.md instruction (scenario
# E2E_Quote_LockQuote; artifacts go to test/e2e/.runs/<UTC mark>/); there are
# no PowerShell run scripts.

vet:
	go vet ./...

# lint mounts the repository; the modules come from the GitHub origin (Go proxy)
# resolves inside the container.
lint:
	docker run --rm -v "$(CURDIR)":/src -w /src golangci/golangci-lint:latest-alpine golangci-lint run ./...

fmt:
	gofmt -l .

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

up:
	docker compose -f deploy/docker-compose.yml --profile app up -d

down:
	docker compose -f deploy/docker-compose.yml down -v
