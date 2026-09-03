OUTPUT_DIR := ./build
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null | cut -c1-7)
# 版本单一来源:git describe --tags 派生(v2.1.0 格式 tag);非 git 环境兜底 2.0.0。
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 2.0.0)
LDFLAGS    := -X github.com/gausszhou/gotty/cmd.Version=$(VERSION) -X github.com/gausszhou/gotty/cmd.CommitID=$(GIT_COMMIT)
UPX        ?= upx

# 构建矩阵:资产命名 gotty-{os}-{arch}[.exe],与 install.sh / self update 的
# 映射保持一致;windows 输出 .exe;UPX 仅对 linux 执行(macOS 会破坏签名)。
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

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
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		out=$(OUTPUT_DIR)/gotty-$$os-$$arch$$ext; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(LDFLAGS) -s -w" \
			-o $$out .; \
		if [ "$$os" = "linux" ] && command -v $(UPX) >/dev/null 2>&1; then \
			echo "Compressing with UPX..."; \
			$(UPX) --best --lzma $$out; \
		fi; \
	done
	@echo "Writing sha256sums.txt..."; \
	cd $(OUTPUT_DIR) && sha256sum gotty-* > sha256sums.txt
	@echo "Release assets:"; ls -la $(OUTPUT_DIR)

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
	go test -tags browser_e2e ./internal/browser/

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt errors:"; gofmt -l .; exit 1)

clean:
	rm -rf $(OUTPUT_DIR)
	rm -rf internal/api/static apps/web/dist apps/docs/.vitepress/dist node_modules apps/web/node_modules apps/docs/node_modules
