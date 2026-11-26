GO ?= go
PROJECT_NAME := rua
OS_INFO := $(shell $(GO) env GOOS)
ARCH_INFO := $(shell $(GO) env GOARCH)
BINARY := dist/$(PROJECT_NAME)_$(OS_INFO)_$(ARCH_INFO)/$(PROJECT_NAME)
TMP_DIR := ./tmp

.PHONY: help web web/dev dev build build/local build/all build/docker build/release build/release/all build/release/docker run tidy lint test test/go test/web test/e2e test/cover audit release release-check new_version

help:
	@echo "Targets: web web/dev dev build run tidy lint test test/go test/web test/e2e test/cover audit release release-check new_version"

## web: install frontend dependencies and build the web UI into web/dist
web:
	sh .github/scripts/install_build_deps.sh
	npm --prefix web run build

## web/dev: Vite dev server on :5173 (proxies /api to :8080)
web/dev:
	npm --prefix web run dev

## dev: run the Go server against the local database (build web first)
dev: web
	$(GO) run ./cmd/rua serve

build: web tidy build/local

build/local:
	goreleaser build --clean --id all --snapshot --single-target

build/all: web
	goreleaser build --clean --id all --snapshot

build/docker: web
	goreleaser build --clean --id docker --snapshot

build/release: build/release/all

build/release/all: web
	goreleaser build --clean --id all --skip=validate

build/release/docker: web
	goreleaser build --clean --id docker --skip=validate

run: build
	./$(BINARY)

tidy:
	$(GO) fmt ./...
	$(GO) mod tidy -v
	@if [ -d web/node_modules ]; then \
		npm --prefix web run typecheck; \
	else \
		echo "skip: web typecheck (run 'make web' first)"; \
	fi

lint: tidy
	golangci-lint fmt
	golangci-lint run --fix > /dev/null 2>&1 || true
	golangci-lint run

test: test/go test/web

test/go:
	$(GO) test -count=1 -buildvcs -vet=all ./...
	$(GO) test -race -count=1 -buildvcs -vet=all ./...

test/web:
	npm --prefix web test

## test/e2e: Playwright against `rua demo`
test/e2e:
	GO="$(GO)" ./e2e/run.sh

test/cover:
	mkdir -p $(TMP_DIR)
	$(GO) test -race -coverprofile=$(TMP_DIR)/coverage.out ./...
	$(GO) tool cover -html=$(TMP_DIR)/coverage.out

audit: tidy
	$(GO) mod verify
	$(GO) vet ./...
	golangci-lint run --enable-only=staticcheck
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

release-check:
	goreleaser check

release: web
	goreleaser release --clean --skip=publish,announce

new_version:
	./scripts/new_version.sh $(VERSION)
