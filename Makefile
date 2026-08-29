OUTPUT_DIR := ./build
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null | cut -c1-7)
VERSION    ?= 2.0.0
LDFLAGS    := -X github.com/gausszhou/gotty/cmd.Version=$(VERSION) -X github.com/gausszhou/gotty/cmd.CommitID=$(GIT_COMMIT)
UPX        ?= upx

PLATFORMS := linux/amd64 linux/arm64

# Default target: 单平台开发构建;多平台发布请显式执行 `make release`
.DEFAULT_GOAL := build

.PHONY: all build release frontend docs static test vet fmt clean install

all: frontend static release

build: frontend static
	@mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $(OUTPUT_DIR)/gotty .

release: frontend static
	@mkdir -p $(OUTPUT_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(LDFLAGS) -s -w" \
			-o $(OUTPUT_DIR)/gotty-$$os-$$arch .; \
		echo "Compressing with UPX..."; \
		$(UPX) --best --lzma $(OUTPUT_DIR)/gotty-$$os-$$arch; \
	done

# Install all frontend workspace dependencies (pnpm workspace: apps/web + apps/docs)
install:
	pnpm install

# Build frontend: Vite + Vue 3 + xterm.js v5 + WebGL (apps/web)
frontend:
	pnpm --filter gotty-frontend build

# Build documentation site: VitePress (apps/docs)
docs:
	pnpm --filter gotty-docs build

# Copy vite build 产物(含 public/favicon.png)into internal/api/static for go:embed
static: frontend
	@mkdir -p internal/api/static
	cp apps/web/dist/index.html internal/api/static/index.html
	cp apps/web/dist/main.js internal/api/static/main.js
	cp apps/web/dist/favicon.png internal/api/static/favicon.png

test: vet fmt
	go test ./...

# 浏览器引擎端到端测试(需本机有 Chrome/Chromium;CI 不跑,见 ci.yml)
test-browser:
	go test -tags browser_e2e ./internal/capture/

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt errors:"; gofmt -l .; exit 1)

clean:
	rm -rf $(OUTPUT_DIR)
	rm -rf internal/api/static apps/web/dist apps/docs/.vitepress/dist node_modules apps/web/node_modules apps/docs/node_modules
