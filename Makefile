.PHONY: build test test-integration vet lint fmt vuln up down e2e

build:
	go build ./...

test:
	go test ./...

test-integration:
	go test -tags integration -count=1 ./internal/...

# e2e поднимает compose-стенд и проверяет LockQuote/GetRate через grpcurl (CBL-6.2).
e2e:
	powershell -NoProfile -File test/e2e/lock-quote.ps1

vet:
	go vet ./...

# lint монтирует каталог services/ целиком: replace на ../cbl-kit резолвится в контейнере.
lint:
	docker run --rm -v "$(CURDIR)/..":/src -w /src/$(notdir $(CURDIR)) golangci/golangci-lint:latest-alpine golangci-lint run ./...

fmt:
	gofmt -l .

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

up:
	docker compose -f deploy/docker-compose.yml --profile app up -d

down:
	docker compose -f deploy/docker-compose.yml down -v
