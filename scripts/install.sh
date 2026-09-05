#!/bin/sh
#
# gotty 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh
#   # 指定版本 / 安装前缀 / 镜像仓库:
#   #   sh install.sh --version v0.0.2 --prefix ~/.local --repo owner/gotty
#
# 流程:探测平台架构 → 取发布信息(latest 或 --version)→ 下载对应平台的
# 压缩包(gotty-{os}-{arch}.tar.gz / .zip)与 sha256sums.txt → 校验(失败即
# 退出并清理,防投毒/损坏)→ 本地解压出二进制(Windows/Git Bash 下探测
# unzip / bsdtar / 系统 tar.exe 解 zip)→ 安装到 --prefix(默认
# $HOME/.local/bin,用户目录,不请求 sudo)→ 幂等写入 PATH 到 ~/.bashrc
# (已存在则跳过)→ 提示 source ~/.bashrc 生效。
# 覆盖:Linux / macOS / Windows 的 Git Bash(MSYS);原生 Windows
# (cmd/PowerShell)请使用 scripts/install.ps1。旧版发布没有压缩包时自动
# 回退下载原始二进制。
#
# 私有部署:GOTTY_UPDATE_URL 指向一个「GitHub release 同形状」的 JSON 索引
# (静态站点可托管),脚本改从该地址取版本与资产 URL。
set -eu

VERSION="${GOTTY_VERSION:-}"
PREFIX="${GOTTY_PREFIX:-$HOME/.local}"
REPO="${GOTTY_REPO:-gausszhou/gotty}"

usage() {
    cat <<'EOF'
gotty 一键安装脚本:探测平台 → 下载发布资产 → 校验 → 解压安装到
--prefix(默认 $HOME/.local/bin,不请求 sudo)→ 幂等写入 ~/.bashrc。

options:
  --version <tag>    Target version tag, e.g. v0.0.2 (default: latest release)
  --prefix <dir>     Install prefix (default: $HOME/.local, binary at <prefix>/bin/gotty)
  --repo <owner/name> GitHub repository to fetch from (default: gausszhou/gotty)
EOF
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

# --- 平台/架构探测(与 Makefile 矩阵命名对齐 gotty-{os}-{arch}[.exe]) ----
OS="$(uname -s 2>/dev/null || true)"
case "$OS" in
    Linux) GOOS=linux ;;
    Darwin) GOOS=darwin ;;
    MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
    *) echo "unsupported OS: $OS (install.sh covers Linux/macOS and Git Bash; native Windows: use scripts/install.ps1)" >&2; exit 1 ;;
esac
MACH="$(uname -m 2>/dev/null || true)"
case "$MACH" in
    x86_64|amd64) GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) echo "unsupported architecture: $MACH (releases cover amd64/arm64)" >&2; exit 1 ;;
esac
BIN_SUFFIX=""
ARCHIVE_SUFFIX=".tar.gz"
if [ "$GOOS" = windows ]; then
    # Windows 资产:原始二进制与压缩包都带 .exe / .zip 后缀。
    BIN_SUFFIX=".exe"
    ARCHIVE_SUFFIX=".zip"
fi
ASSET="gotty-$GOOS-$GOARCH$BIN_SUFFIX"
ARCHIVE_ASSET="gotty-$GOOS-$GOARCH$ARCHIVE_SUFFIX"

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

# 压缩包方式:本地解压出二进制(压缩包内与资产同名,如 gotty-linux-amd64
# 或 gotty-windows-amd64.exe)。zip 先探测可用的解压工具:unzip、bsdtar
# (Git for Windows 自带 tar 即 bsdtar)或 Windows 系统自带的 tar.exe。
if [ "$DL_ASSET" = "$ARCHIVE_ASSET" ]; then
    echo "Extracting $ASSET ..."
    if [ "$ARCHIVE_SUFFIX" = ".zip" ]; then
        if command -v unzip >/dev/null 2>&1; then
            (cd "$TMP" && unzip -oq "$ARCHIVE_ASSET")
        elif tar --version 2>/dev/null | grep -qi bsdtar; then
            tar -xf "$TMP/$ARCHIVE_ASSET" -C "$TMP"
        elif [ -x "$(command -v tar.exe 2>/dev/null || printf '%s' "${WINDIR:-}/System32/tar.exe")" ]; then
            tar.exe -xf "$(cygpath -w "$TMP/$ARCHIVE_ASSET")" -C "$(cygpath -w "$TMP")"
        else
            echo "cannot extract $ARCHIVE_ASSET: need unzip or bsdtar (install unzip, or use Git for Windows)" >&2
            exit 1
        fi
    else
        tar -xzf "$TMP/$ARCHIVE_ASSET" -C "$TMP"
    fi
    if [ ! -f "$TMP/$ASSET" ]; then
        echo "archive $ARCHIVE_ASSET did not contain $ASSET" >&2
        exit 1
    fi
fi

# --- 安装 ----------------------------------------------------------------
BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR"
BIN="$BIN_DIR/gotty$BIN_SUFFIX"
install -m 0755 "$TMP/$ASSET" "$BIN"

# --- PATH 配置:幂等写入 ~/.bashrc ----------------------------------------
# 只在 .bashrc 中缺少该行时追加(附一行标记注释便于识别/删除);重复安装
# 不会产生重复条目。文件不存在时自动创建。Git Bash 的 $HOME 即 Windows
# 用户目录,MSYS bash 的交互会话会读取 ~/.bashrc,所以同样适用。
RC_FILE="$HOME/.bashrc"
RC_LINE="export PATH=\"$BIN_DIR:\$PATH\""
RC_MARK="# added by gotty install.sh ($REPO)"
if [ -f "$RC_FILE" ] && grep -qF -- "$RC_LINE" "$RC_FILE"; then
    RC_ADDED=0
    echo "PATH line already present in $RC_FILE, skipping."
else
    printf '\n%s\n%s\n' "$RC_MARK" "$RC_LINE" >> "$RC_FILE"
    RC_ADDED=1
    echo "Appended to $RC_FILE:"
    echo "  $RC_LINE"
fi

echo
echo "Installed to $BIN"
echo
echo "Activate the PATH change now, or open a new terminal:"
echo "  source $RC_FILE"
if [ "$GOOS" = windows ]; then
    echo "Note: this configures PATH for Git Bash (MSYS) sessions only."
    echo "      cmd/PowerShell: add \"$(cygpath -w "$BIN_DIR" 2>/dev/null || echo "$BIN_DIR")\" to your user PATH instead."
fi
case "$SHELL" in
    *zsh*)
        echo "Note: your shell is zsh — if you use zsh interactively, add the same line to ~/.zshrc."
        ;;
esac

"$BIN" version
echo
echo "next steps:"
echo "  - serve 一个终端:    gotty serve top"
echo "  - 升级到新版本:      gotty self update"
echo "  - systemd 部署指引:  https://github.com/$REPO#run-as-a-systemd-user-service"