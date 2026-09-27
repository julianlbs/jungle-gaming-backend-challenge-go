GO        ?= go
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0
COMPOSE   ?= docker compose
TEST_TAGS := integration,e2e
K6_IMAGE  ?= grafana/k6:1.3.0
LOAD_RATE ?= 200
LOAD_DURATION ?= 60s
LOAD_DRAIN_TIMEOUT_S ?= 120
LOAD_WALLETS ?= 200
LOAD_HOT_SHARE ?= 0.1
LOAD_REPLAY_SHARE ?= 0.05

.PHONY: fmt fmt-check vet lint test test-race test-integration test-e2e \
        build up down deps-up deps-down migrate-up migrate-down migrate-version load-test

fmt:
	gofmt -s -w .

fmt-check:
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...
	$(GO) vet -tags=$(TEST_TAGS) ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags=integration -count=1 ./test/integration/...

test-e2e:
	$(GO) test -race -tags=e2e -count=1 -timeout=20m ./test/e2e/...

load-test:
	$(COMPOSE) up --build -d --wait app-1 app-2 app-3
	docker run --rm --network host -v "$(CURDIR)/test/load:/scripts:ro" \
		-e LOAD_RATE=$(LOAD_RATE) -e LOAD_DURATION=$(LOAD_DURATION) -e LOAD_DRAIN_TIMEOUT_S=$(LOAD_DRAIN_TIMEOUT_S) \
		-e LOAD_WALLETS=$(LOAD_WALLETS) -e LOAD_HOT_SHARE=$(LOAD_HOT_SHARE) -e LOAD_REPLAY_SHARE=$(LOAD_REPLAY_SHARE) \
		$(K6_IMAGE) run --quiet /scripts/wager.js

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
