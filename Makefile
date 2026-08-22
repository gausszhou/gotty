OUTPUT_DIR := ./build
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null | cut -c1-7)
VERSION    ?= 2.0.0
LDFLAGS    := -X main.Version=$(VERSION) -X main.CommitID=$(GIT_COMMIT)

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build frontend docs static test vet fmt clean install

all: frontend static release

build: frontend static
	@mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $(OUTPUT_DIR)/gotty .

release: frontend static
	@mkdir -p $(OUTPUT_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(LDFLAGS) -s -w" \
			-o $(OUTPUT_DIR)/gotty-$$os-$$arch$$ext .; \
	done

# Install all frontend workspace dependencies (pnpm workspace: apps/web + apps/docs)
install:
	pnpm install

# Build frontend: Vite + Vue 3 + xterm.js v6 + WebGL (apps/web)
frontend:
	pnpm --filter gotty-frontend build

# Build documentation site: VitePress (apps/docs)
docs:
	pnpm --filter gotty-docs build

# Copy static assets + bundle into internal/server/static for go:embed
static: frontend
	@mkdir -p internal/server/static/js internal/server/static/css
	cp apps/web/static/index.html internal/server/static/index.html
	cp apps/web/static/favicon.png internal/server/static/favicon.png
	cp apps/web/static/css/index.css internal/server/static/css/index.css
	cp apps/web/static/css/xterm_customize.css internal/server/static/css/xterm_customize.css
	cp apps/web/dist/gotty-bundle.js internal/server/static/js/gotty-bundle.js

test: vet fmt
	go test ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt errors:"; gofmt -l .; exit 1)

clean:
	rm -rf $(OUTPUT_DIR)
	rm -rf internal/server/static apps/web/dist apps/docs/.vitepress/dist node_modules apps/web/node_modules apps/docs/node_modules
