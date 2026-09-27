GO        ?= go
COMPOSE   ?= docker compose
TEST_TAGS := integration,e2e

.PHONY: fmt fmt-check vet lint test test-race test-integration test-e2e \
        build up down deps-up deps-down migrate-up migrate-down migrate-version

fmt:
	gofmt -s -w .

fmt-check:
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...
	$(GO) vet -tags=$(TEST_TAGS) ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags=integration -count=1 ./test/integration/...

test-e2e:
	$(GO) test -race -tags=e2e -count=1 -timeout=20m ./test/e2e/...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/wallet ./cmd/wallet

up:
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down -v

deps-up:
	$(COMPOSE) up -d --wait postgres keycloak localstack
	$(COMPOSE) up aws-init migrate

deps-down:
	$(COMPOSE) down -v

migrate-up:
	$(GO) run ./cmd/wallet migrate up

migrate-down:
	$(GO) run ./cmd/wallet migrate down 1

migrate-version:
	$(GO) run ./cmd/wallet migrate version
