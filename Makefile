GO ?= go

.PHONY: build test test-race run seed

build:
	$(GO) build ./...

test:
	$(GO) test ./... -count=1

test-race:
	$(GO) test ./test/ -race -count=3 -timeout 10m

run:
	SEED=1 RUN_MIGRATIONS=1 $(GO) run ./cmd/server
