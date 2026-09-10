<#
.SYNOPSIS
    gotty 本地构建 + 安装脚本(Windows / PowerShell)
.DESCRIPTION
    把「改完源码 → 本机能直接跑」这条链路收敛成一条命令。免去手动找 Go、
    手动拼 ldflags、手动拷贝和配 PATH:

      1. 定位 Go 工具链(PATH → 标准安装目录 → 各盘符探测;可 -GoExe 指定)。
         Go 装在非系统盘且没进 PATH 是常见情况,所以不能只依赖 `go`。
      2. 按需重建前端(internal/api/static 是 go:embed 的产物):
         前端源码比 static\main.js 新才重建;否则复用现有产物,不联网。
      3. 版本号 / 提交号从 git 派生,与 Makefile 的 LDFLAGS 完全一致。
      4. go build -trimpath -ldflags "<version> -s -w" → build\gotty.exe
      5. 安装到 <Prefix>\bin\gotty.exe(默认 %USERPROFILE%\.local\bin,无需
         管理员)。旧二进制正在运行时先改名让位,因此可以在服务运行时重装。
      6. 幂等加入 User PATH(与 scripts/install.ps1 完全相同的约定)。
      7. 自检 `gotty version --json`;-Serve 则直接起服务并打开浏览器。

    与 scripts/install.ps1 的分工:那个脚本从 GitHub release 下载已发布资产,
    这个脚本从当前工作区的源码本地构建 —— 改完代码想立刻用,用这个。
.PARAMETER Prefix
    安装前缀(默认 %USERPROFILE%\.local)。可用 $env:GOTTY_PREFIX。
    二进制落在 <Prefix>\bin\gotty.exe,与 install.ps1 / gotty self update 一致。
.PARAMETER GoExe
    显式指定 go.exe 路径(默认自动探测)。
.PARAMETER RebuildFrontend
    强制重建前端(默认只在源码比产物新时重建)。
.PARAMETER SkipFrontend
    跳过前端构建,直接用 internal/api/static 里现有产物(不联网,最快)。
.PARAMETER NoPathConfig
    不修改 User PATH(CI / 测试用)。
.PARAMETER Serve
    安装完成后启动服务并打开浏览器。
.PARAMETER Port
    -Serve 使用的端口(默认 9049,与服务端默认值一致)。
.PARAMETER Help
    打印用法。
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\build-install.ps1
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\build-install.ps1 -Serve
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\build-install.ps1 -RebuildFrontend -Prefix D:\tools
#>
[CmdletBinding()]
param(
    [string]$Prefix = $(if ($env:GOTTY_PREFIX) { $env:GOTTY_PREFIX } else { "$env:USERPROFILE\.local" }),
    [string]$GoExe,
    [switch]$RebuildFrontend,
    [switch]$SkipFrontend,
    [switch]$NoPathConfig,
    [switch]$Serve,
    [int]$Port = 9049,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

# 调用自身的命令行:5.1(Desktop)没有 pwsh,打印出来的命令必须是这台机器真能跑的。
if ($PSEdition -eq 'Core') { $selfCmd = 'pwsh -File' } else { $selfCmd = 'powershell -ExecutionPolicy Bypass -File' }

function Write-Usage {
    @'
gotty 本地构建 + 安装(Windows/PowerShell):
  定位 Go → 按需重建前端 → 本地构建 → 安装到 <Prefix>\bin → 幂等配 PATH → 自检。

options:
  -Prefix <dir>      安装前缀(默认 $env:USERPROFILE\.local,二进制在 <prefix>\bin\gotty.exe)
  -GoExe <path>      显式指定 go.exe(默认自动探测 PATH 与各盘符标准目录)
  -RebuildFrontend   强制重建前端(Vite 产物 → internal/api/static)
  -SkipFrontend      跳过前端构建,直接用现有产物(不联网)
  -NoPathConfig      不修改 User PATH
  -Serve             安装后启动服务并打开浏览器
  -Port <n>          -Serve 的端口(默认 9049)
  -Help              打印本用法
'@ | Write-Host
}

if ($Help) {
    Write-Usage
    exit 0
}

# ── 0. 定位仓库根 ────────────────────────────────────────────────────────
# 脚本位于 <repo>\scripts\ 下;以脚本自身位置推导,不依赖调用者的 cwd。
$RepoRoot = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path (Join-Path $RepoRoot 'go.mod'))) {
    throw "找不到 go.mod:推导出的仓库根为 $RepoRoot(请从仓库内的 scripts\ 目录运行本脚本)"
}
$StaticDir = Join-Path $RepoRoot 'internal\api\static'
$BuildDir = Join-Path $RepoRoot 'build'

Write-Host "repo:   $RepoRoot"
Write-Host "prefix: $Prefix"

# ── 1. 定位 Go 工具链 ────────────────────────────────────────────────────
# 探测顺序:PATH → 非系统盘/系统盘的标准安装目录 → scoop。Go 装在 D:\Program
# Files\Go 而 PATH 里只有 C:\Users\<u>\go\bin(GOPATH\bin)是常见情况。
function Find-GoExe {
    param([string]$Explicit)

    if ($Explicit) {
        if (Test-Path $Explicit) { return $Explicit }
        throw "-GoExe 指向的文件不存在: $Explicit"
    }

    $onPath = Get-Command go -CommandType Application -ErrorAction SilentlyContinue
    if ($onPath) { return $onPath.Source }

    $roots = @()
    if ($env:ProgramFiles) { $roots += $env:ProgramFiles }
    if (${env:ProgramFiles(x86)}) { $roots += ${env:ProgramFiles(x86)} }
    if ($env:LOCALAPPDATA) { $roots += (Join-Path $env:LOCALAPPDATA 'Programs') }
    # 其他盘符(安装到 D:\Program Files\Go 等非系统盘的情况)
    foreach ($drive in @(Get-PSDrive -PSProvider FileSystem -ErrorAction SilentlyContinue)) {
        if ($drive.Root -and $drive.Root -match '^[A-Za-z]:\\$') { $roots += $drive.Root }
    }

    $candidates = @()
    foreach ($root in $roots) {
        $candidates += (Join-Path $root 'Go\bin\go.exe')
        $candidates += (Join-Path $root 'Program Files\Go\bin\go.exe')
        $candidates += (Join-Path $root 'Program Files (x86)\Go\bin\go.exe')
        $candidates += (Join-Path $root 'Software\Go\bin\go.exe')
    }
    if ($env:USERPROFILE) { $candidates += (Join-Path $env:USERPROFILE 'scoop\shims\go.exe') }

    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) { return $c }
    }

    throw @"
找不到 Go 工具链。请安装 Go(https://go.dev/dl/)后重试,或用 -GoExe 指定:
    pwsh -File scripts\build-install.ps1 -GoExe 'D:\Program Files\Go\bin\go.exe'
"@
}

$go = Find-GoExe -Explicit $GoExe
$goVersion = (& $go version) -join ' '
Write-Host "go:     $go ($goVersion)"

# ── 2. 按需重建前端 ──────────────────────────────────────────────────────
# internal/api/static 是 go:embed 的输入(构建期必须存在),它由 apps/web 的
# Vite 产物拷贝而来。前端源码没动就复用现有产物:既避免联网 pnpm install,
# 也让重装/重构建保持秒级。
function Test-FrontendStale {
    param([string]$StaticJs)

    if (-not (Test-Path $StaticJs)) { return $true }

    $builtAt = (Get-Item $StaticJs).LastWriteTimeUtc
    $sources = @()
    $srcDir = Join-Path $RepoRoot 'apps\web\src'
    if (Test-Path $srcDir) {
        $sources += @(Get-ChildItem -Path $srcDir -Recurse -File -ErrorAction SilentlyContinue)
    }
    foreach ($rel in @('apps\web\index.html', 'apps\web\package.json', 'apps\web\vite.config.ts')) {
        $p = Join-Path $RepoRoot $rel
        if (Test-Path $p) { $sources += @(Get-Item $p) }
    }
    if ($sources.Count -eq 0) { return $true }

    $newest = $sources | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
    return ($newest.LastWriteTimeUtc -gt $builtAt)
}

function Invoke-FrontendBuild {
    $pnpm = Get-Command pnpm -ErrorAction SilentlyContinue
    if (-not $pnpm) {
        throw @'
需要 pnpm 才能重建前端,但 PATH 里没有 pnpm。
选择一:安装 Node + pnpm 后重试(winget install OpenJS.NodeJS 或 corepack enable pnpm)
选择二:加 -SkipFrontend,直接用 internal/api/static 里已有的构建产物
'@
    }
    if (-not (Test-Path (Join-Path $RepoRoot 'node_modules'))) {
        Write-Host 'frontend deps missing → pnpm install (首次会联网,较慢) ...'
        & pnpm install
        if ($LASTEXITCODE -ne 0) { throw "pnpm install 失败(退出码 $LASTEXITCODE)" }
    }

    Write-Host 'building frontend (vite) ...'
    & pnpm --filter gotty-frontend build
    if ($LASTEXITCODE -ne 0) { throw "前端构建失败(退出码 $LASTEXITCODE)" }

    # 与 Makefile 的 static 目标一致:把 Vite 产物拷进 go:embed 目录
    $dist = Join-Path $RepoRoot 'apps\web\dist'
    foreach ($f in @('index.html', 'main.js', 'favicon.png')) {
        $src = Join-Path $dist $f
        if (-not (Test-Path $src)) { throw "前端产物缺少 $f($dist)" }
        New-Item -ItemType Directory -Force -Path $StaticDir | Out-Null
        Copy-Item -Path $src -Destination (Join-Path $StaticDir $f) -Force
    }
    Write-Host 'frontend assets refreshed (internal/api/static).'
}

$staticIndex = Join-Path $StaticDir 'index.html'
$staticJs = Join-Path $StaticDir 'main.js'
if ($SkipFrontend) {
    if (-not (Test-Path $staticIndex)) {
        throw "-SkipFrontend 要求 $StaticDir 已存在前端产物(index.html/main.js)"
    }
    Write-Host 'frontend: skipped (-SkipFrontend), reusing existing assets.'
} elseif ($RebuildFrontend -or (Test-FrontendStale -StaticJs $staticJs)) {
    Invoke-FrontendBuild
} else {
    Write-Host 'frontend: up to date, reusing internal/api/static (no network).'
}
if (-not (Test-Path $staticIndex)) {
    throw "缺少 $staticIndex:go:embed 需要它,请先构建前端(去掉 -SkipFrontend 重试)"
}

# ── 3. 版本号 / 提交号(与 Makefile 的 LDFLAGS 口径一致) ──────────────────
$version = ''
$commit = ''
if (Get-Command git -CommandType Application -ErrorAction SilentlyContinue) {
    $version = (& git -C $RepoRoot describe --tags --always --dirty 2>$null | Select-Object -First 1)
    $commit = (& git -C $RepoRoot rev-parse HEAD 2>$null | Select-Object -First 1)
}
if (-not $version) { $version = '2.0.0' }        # Makefile 的兜底值
if ($commit) { $commit = $commit.Substring(0, 7) } else { $commit = 'unknown' }
$version = $version.Trim()
Write-Host "version: $version ($commit)"

# ── 4. 本地构建 ──────────────────────────────────────────────────────────
$env:CGO_ENABLED = '0'   # 纯静态单二进制:与 Makefile 的构建口径一致
$ldflags = "-X github.com/gausszhou/gotty/cmd.Version=$version -X github.com/gausszhou/gotty/cmd.CommitID=$commit -s -w"
New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
$outBin = Join-Path $BuildDir 'gotty.exe'

Write-Host 'building gotty.exe ...'
Push-Location $RepoRoot
try {
    & $go build '-trimpath' '-ldflags' $ldflags '-o' $outBin '.'
    if ($LASTEXITCODE -ne 0) { throw "go build 失败(退出码 $LASTEXITCODE)" }
} finally {
    Pop-Location
}
if (-not (Test-Path $outBin)) { throw "构建结束但没找到 $outBin" }
Write-Host ("built:  {0} ({1:N0} bytes)" -f $outBin, (Get-Item $outBin).Length)

# ── 5. 安装到 <Prefix>\bin ───────────────────────────────────────────────
# 目标可能正在被运行中的服务占用:Windows 不允许覆盖运行中的 exe,但允许改名,
# 因此先改名让位再写入 —— 这样"服务还开着"也能重新构建安装。
$binDir = Join-Path $Prefix 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$bin = Join-Path $binDir 'gotty.exe'

function Install-Binary {
    param([string]$Source, [string]$Target)

    try {
        Copy-Item -Path $Source -Destination $Target -Force -ErrorAction Stop
        return
    } catch {
        $firstError = $_
    }
    if (Test-Path $Target) {
        # 目标被运行中的进程锁住(Windows 不允许覆盖运行中的 exe,但允许改名),
        # 于是先改名让位再写入 —— 服务开着也能重新构建安装。
        $old = "$Target.old"
        if (Test-Path $old) {
            # 上一次让位留下的文件可能还被那个进程锁着:换个唯一名字,避免撞名。
            Remove-Item -Path $old -Force -ErrorAction SilentlyContinue
            if (Test-Path $old) { $old = "$Target.old-" + (Get-Date -Format 'yyyyMMddHHmmss') }
        }
        try {
            Move-Item -Path $Target -Destination $old -Force -ErrorAction Stop
            Copy-Item -Path $Source -Destination $Target -Force -ErrorAction Stop
            Write-Host "previous binary was in use → moved aside to $(Split-Path -Leaf $old)"
            return
        } catch {
            Move-Item -Path $old -Destination $Target -Force -ErrorAction SilentlyContinue
        }
    }
    throw "无法写入 $Target :$($firstError.Exception.Message)`n(如果 gotty 服务正在运行,先停掉它再重试)"
}

Install-Binary -Source $outBin -Target $bin
Write-Host "installed: $bin"

# 清理历史让位文件:仍被运行中的旧进程锁住的那个留给下次运行(届时已解锁)。
$staleLeft = @()
foreach ($f in @(Get-ChildItem -Path $binDir -Filter 'gotty.exe.old*' -ErrorAction SilentlyContinue)) {
    Remove-Item -Path $f.FullName -Force -ErrorAction SilentlyContinue
    if (Test-Path $f.FullName) { $staleLeft += $f.Name }
}
if ($staleLeft.Count -gt 0) {
    Write-Host "note:   $($staleLeft -join ', ') 仍被运行中的旧进程占用,停掉服务后再跑一次本脚本即可清理"
}

# ── 6. 幂等配置 User PATH(与 install.ps1 相同约定) ─────────────────────
if ($NoPathConfig) {
    Write-Host 'PATH:   skipped (-NoPathConfig)'
} else {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @()
    if ($userPath) { $parts = @($userPath -split ';' | Where-Object { $_ }) }
    if ($parts -contains $binDir) {
        Write-Host 'PATH:   entry already present, skipping'
    } else {
        if ($userPath) { $newPath = $userPath.TrimEnd(';') + ';' + $binDir } else { $newPath = $binDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "PATH:   added $binDir to User PATH(新开终端生效)"
    }
}

# ── 7. 自检 ──────────────────────────────────────────────────────────────
Write-Host ''
Write-Host 'self-check:'
& $bin version --json
if ($LASTEXITCODE -ne 0) { Write-Warning "gotty 自检失败:运行 $bin version 看具体错误(退出码 $LASTEXITCODE)" }

# ── 8. 可选:直接起服务 ──────────────────────────────────────────────────
if ($Serve) {
    Write-Host ''
    $busy = $false
    $probe = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $probe.BeginConnect('127.0.0.1', $Port, $null, $null)
        if ($iar.AsyncWaitHandle.WaitOne(300)) { $probe.EndConnect($iar); $busy = $true }
    } catch { $busy = $false } finally { $probe.Close() }

    if ($busy) {
        Write-Warning "127.0.0.1:$Port 已被占用:可能已有一个 gotty 在跑。用 -Port 换端口,或直接用 http://127.0.0.1:$Port/"
    } else {
        Start-Process -FilePath $bin -ArgumentList @('serve', '--port', "$Port") | Out-Null
        Start-Sleep -Seconds 2
        Write-Host "server started on http://127.0.0.1:$Port/ (opening browser)"
        Start-Process "http://127.0.0.1:$Port/"
    }
}

Write-Host ''
Write-Host 'next steps:'
Write-Host "  - 新开一个终端后可直接用:  gotty serve --port $Port"
Write-Host '  - 指定命令启动:            gotty serve -- bash   (Windows 默认取 $SHELL / %COMSPEC%)'
Write-Host "  - 改完源码再来一次:        $selfCmd scripts\build-install.ps1"
Write-Host "  - 只装前端改动:            $selfCmd scripts\build-install.ps1 -RebuildFrontend"
Write-Host "  - 从 release 装/升级:      $selfCmd scripts\install.ps1  /  gotty self update"
