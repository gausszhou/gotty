#!/bin/sh
#
# gotty 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh
#   # 指定版本 / 安装前缀 / 镜像仓库:
#   #   sh install.sh --version v2.1.0 --prefix ~/.local --repo owner/gotty
#
# 流程:探测平台架构 → 取发布信息(latest 或 --version)→ 下载对应平台的
# 压缩包(build 资产 gotty-{os}-{arch}.tar.gz)与 sha256sums.txt → 校验
# (失败即退出并清理,防投毒/损坏)→ 本地解压出二进制 → 安装到 --prefix
# (默认 $HOME/.local/bin,用户目录,不请求 sudo)。
# 兼容:老版本发布或自定义源没有压缩包时,自动回退下载原始二进制。
#
# 私有部署:GOTTY_UPDATE_URL 指向一个「GitHub release 同形状」的 JSON 索引
# (静态站点可托管),脚本改从该地址取版本与资产 URL。
set -eu

VERSION="${GOTTY_VERSION:-}"
PREFIX="${GOTTY_PREFIX:-$HOME/.local}"
REPO="${GOTTY_REPO:-gausszhou/gotty}"

usage() {
    sed -n '2,12p' "$0"
    echo
    echo "options:"
    echo "  --version <tag>    Target version tag, e.g. v2.1.0 (default: latest release)"
    echo "  --prefix <dir>     Install prefix (default: \$HOME/.local, binary at <prefix>/bin/gotty)"
    echo "  --repo <owner/name> GitHub repository to fetch from (default: gausszhou/gotty)"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --prefix) PREFIX="$2"; shift 2 ;;
        --repo) REPO="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage; exit 1 ;;
    esac
done

# --- 平台/架构探测(与 Makefile 矩阵命名对齐 gotty-{os}-{arch}) ----------
OS="$(uname -s 2>/dev/null || true)"
case "$OS" in
    Linux) GOOS=linux ;;
    Darwin) GOOS=darwin ;;
    MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
    *) echo "unsupported OS: $OS (install.sh covers Linux/macOS; Windows: download the .exe from Releases)" >&2; exit 1 ;;
esac
if [ "$GOOS" = windows ]; then
    echo "Windows 请手动下载 Releases 页面二进制: https://github.com/$REPO/releases (install.sh 仅覆盖 Linux/macOS)" >&2
    exit 1
fi
MACH="$(uname -m 2>/dev/null || true)"
case "$MACH" in
    x86_64|amd64) GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) echo "unsupported architecture: $MACH (releases cover amd64/arm64)" >&2; exit 1 ;;
esac
ASSET="gotty-$GOOS-$GOARCH"
ARCHIVE_ASSET="$ASSET.tar.gz"

# --- 取发布信息 ----------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -n "${GOTTY_UPDATE_URL:-}" ]; then
    INDEX_URL="$GOTTY_UPDATE_URL"
else
    if [ -n "$VERSION" ]; then
        INDEX_URL="https://api.github.com/repos/$REPO/releases/tags/$VERSION"
    else
        INDEX_URL="https://api.github.com/repos/$REPO/releases/latest"
    fi
fi
echo "Resolving release info from $INDEX_URL ..."
curl -fsSL "$INDEX_URL" > "$TMP/release.json"
TAG="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -1)"
if [ -z "$TAG" ]; then
    echo "failed to resolve a release tag_name from $INDEX_URL" >&2
    exit 1
fi

# --- 下载地址 ------------------------------------------------------------
# GitHub:资产地址是确定性的 github.com/{repo}/releases/download/{tag}/{asset};
# 自定义源:从 JSON 里按 name 配对 browser_download_url。
asset_url_of() { # $1 = asset name
    if [ -n "${GOTTY_UPDATE_URL:-}" ]; then
        awk -v want="$1" '
            /"name"[[:space:]]*:/ {
                n=$0; sub(/.*"name"[[:space:]]*:[[:space:]]*"/, "", n); sub(/".*/, "", n)
            }
            /"browser_download_url"[[:space:]]*:/ {
                u=$0; sub(/.*"browser_download_url"[[:space:]]*:[[:space:]]*"/, "", u); sub(/".*/, "", u)
                if (n == want) { print u; exit }
            }
        ' "$TMP/release.json"
    else
        echo "https://github.com/$REPO/releases/download/$TAG/$1"
    fi
}
BASE_ASSET="$(asset_url_of "$ASSET")"
SUM_URL="$(asset_url_of sha256sums.txt)"
if [ -z "$BASE_ASSET" ]; then
    echo "asset $ASSET not found in release $TAG" >&2
    exit 1
fi
if [ -z "$SUM_URL" ]; then
    echo "sha256sums.txt not found in release $TAG" >&2
    exit 1
fi

# --- 下载 + 校验 ----------------------------------------------------------
# 优先下载压缩包(GitHub 慢网络下更小)。老版本发布或自定义源没有压缩包
# 资产时,下载会失败,自动回退到原始二进制(两者在 sha256sums.txt 都有
# 对应的校验和条目)。
ARCHIVE_URL="$(asset_url_of "$ARCHIVE_ASSET")"
if [ -n "$ARCHIVE_URL" ] && curl -fsSL --retry 3 -o "$TMP/$ARCHIVE_ASSET" "$ARCHIVE_URL"; then
    echo "Downloaded $ARCHIVE_ASSET (release $TAG)."
    DL_ASSET="$ARCHIVE_ASSET"
else
    echo "Archive $ARCHIVE_ASSET unavailable, falling back to raw binary ..."
    curl -fsSL --retry 3 -o "$TMP/$ASSET" "$BASE_ASSET"
    DL_ASSET="$ASSET"
fi
curl -fsSL --retry 3 -o "$TMP/sha256sums.txt" "$SUM_URL"

if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$TMP/$DL_ASSET" | awk '{print $1}')"
else
    actual="$(shasum -a 256 "$TMP/$DL_ASSET" | awk '{print $1}')"
fi
expected="$(awk -v n="$DL_ASSET" '$2 == n || $2 == "*"n {print $1}' "$TMP/sha256sums.txt")"
if [ -z "$expected" ]; then
    echo "no sha256 entry for $DL_ASSET in sha256sums.txt" >&2
    exit 1
fi
if [ "$actual" != "$expected" ]; then
    echo "checksum mismatch for $DL_ASSET — download aborted, nothing installed" >&2
    echo "  got:      $actual" >&2
    echo "  expected: $expected" >&2
    exit 1
fi
echo "Checksum OK (sha256 $expected)."

# 压缩包方式:本地解压出二进制(压缩包内以 gotty-{os}-{arch} 命名)。
if [ "$DL_ASSET" = "$ARCHIVE_ASSET" ]; then
    echo "Extracting $ASSET ..."
    tar -xzf "$TMP/$ARCHIVE_ASSET" -C "$TMP"
    if [ ! -f "$TMP/$ASSET" ]; then
        echo "archive $ARCHIVE_ASSET did not contain $ASSET" >&2
        exit 1
    fi
fi

# --- 安装 ----------------------------------------------------------------
BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR"
BIN="$BIN_DIR/gotty"
install -m 0755 "$TMP/$ASSET" "$BIN"
echo
echo "Installed to $BIN"
echo

# shellcheck disable=SC2015
case ":$PATH:" in
    *":$BIN_DIR:"*) : ;;
    *) echo "NOTE: $BIN_DIR is not on your PATH — add it, e.g."; echo "  export PATH=\"$BIN_DIR:\$PATH\"  # ~/.bashrc / ~/.zshrc" ;;
esac

"$BIN" version
echo
echo "next steps:"
echo "  - serve 一个终端:    gotty serve top"
echo "  - 升级到新版本:      gotty self update"
echo "  - systemd 部署指引:  https://github.com/$REPO#run-as-a-systemd-user-service"