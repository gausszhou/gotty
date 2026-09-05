<#
.SYNOPSIS
    gotty Windows(PowerShell)一键安装脚本
.DESCRIPTION
    与 scripts/install.sh 对齐的 Windows 原生安装流程:

      1. 检测平台 / 架构(发布资产为 gotty-windows-{arch}.exe / .zip)
      2. 取发布信息(默认 latest,可 -Version 指定;支持 -UpdateUrl 私有源)
      3. 下载 zip 与 sha256sums.txt(无 zip 时回退原始二进制)
      4. sha256 校验(失败即退出,不留下任何文件)
      5. 解压 gotty.exe 到 <Prefix>\bin(默认 %USERPROFILE%\.local\bin,无需管理员)
      6. 幂等加入 User PATH(已存在则跳过)——这是原生 Windows 对
         ~/.bashrc 的等价配置,cmd / PowerShell / Git Bash 全部生效
      7. 提示新开终端生效
.PARAMETER Version
    目标版本 tag,如 v0.0.2。默认取最新 release。可用 $env:GOTTY_VERSION。
.PARAMETER Prefix
    安装前缀(默认 %USERPROFILE%\.local)。可用 $env:GOTTY_PREFIX。
.PARAMETER Repo
    GitHub 仓库 owner/name(默认 gausszhou/gotty)。可用 $env:GOTTY_REPO。
.PARAMETER UpdateUrl
    私有部署:指向「GitHub release 同形状」的 JSON 索引地址。
    可用 $env:GOTTY_UPDATE_URL。
.PARAMETER NoPathConfig
    不修改 User PATH(CI/测试或不想动环境变量时使用)。
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/install.ps1
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version v0.0.2 -Prefix "$HOME\.local"
#>
[CmdletBinding()]
param(
    [string]$Version = $env:GOTTY_VERSION,
    [string]$Prefix = "$env:USERPROFILE\.local",
    [string]$Repo = $env:GOTTY_REPO,
    [string]$UpdateUrl = $env:GOTTY_UPDATE_URL,
    [switch]$NoPathConfig,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

function Write-Usage {
    @'
gotty 一键安装脚本(Windows/PowerShell):探测平台 → 下载发布资产 → 校验 →
解压安装到 <Prefix>\bin(默认 %USERPROFILE%\.local\bin,无需管理员)→
幂等加入 User PATH(已存在则跳过)→ 提示新开终端生效。

options:
  -Version <tag>     目标版本 tag,如 v0.0.2(默认:最新 release)
  -Prefix <dir>      安装前缀(默认 $env:USERPROFILE\.local,二进制在 <prefix>\bin\gotty.exe)
  -Repo <owner/name> GitHub 仓库(默认 gausszhou/gotty)
  -UpdateUrl <url>   私有部署:GitHub release 同形状的 JSON 索引地址
  -NoPathConfig      不修改 User PATH(CI/测试或不想动环境变量时使用)
'@ | Write-Host
}

if ($Help) {
    Write-Usage
    exit 0
}

# --- 1. 平台/架构探测 ----------------------------------------------------
# 非 Windows 仅允许模拟模式(-NoPathConfig);Git Bash 用户请用 install.sh。
$IsWin = ($env:OS -eq 'Windows_NT')
if (-not $IsWin) {
    if ($NoPathConfig) {
        Write-Warning '非 Windows 环境 + -NoPathConfig:模拟模式,仅验证下载/校验/解压/安装。'
    } else {
        Write-Error '此脚本仅支持 Windows(使用 Git Bash 请运行 scripts/install.sh)。' -ErrorAction Stop
    }
}
if ($env:GOTTY_PREFIX) { $Prefix = $env:GOTTY_PREFIX }
if (-not $Repo) { $Repo = 'gausszhou/gotty' }

$PROC_ARCH = $env:PROCESSOR_ARCHITECTURE
switch -Regex ($PROC_ARCH) {
    '^(AMD64|x86_64)$' { $GOARCH = 'amd64' }
    '^(ARM64|arm64)$'  { $GOARCH = 'arm64' }
    default {
        if ($NoPathConfig) {
            $GOARCH = 'amd64'
            Write-Warning "架构未知($PROC_ARCH),模拟模式默认 amd64。"
        } else {
            throw "不支持的架构: $PROC_ARCH (发布资产覆盖 amd64/arm64)"
        }
    }
}
$binName = "gotty-windows-$GOARCH.exe"
$zipName = "gotty-windows-$GOARCH.zip"

# --- 2. 取发布信息 -------------------------------------------------------
# Windows PowerShell 5.1 默认 TLS 1.0,GitHub API 会拒绝,强制 TLS 1.2。
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
} catch { }
$headers = @{ 'User-Agent' = 'gotty-install.ps1' }

if ($UpdateUrl) {
    $indexUrl = $UpdateUrl
    Write-Host "Resolving release info from $indexUrl ..."
    $release = Invoke-RestMethod -Uri $UpdateUrl -Headers $headers
} elseif ($Version) {
    $indexUrl = "https://api.github.com/repos/$Repo/releases/tags/$Version"
    Write-Host "Resolving release info from $indexUrl ..."
    $release = Invoke-RestMethod -Uri $indexUrl -Headers $headers
} else {
    $indexUrl = "https://api.github.com/repos/$Repo/releases/latest"
    Write-Host "Resolving release info from $indexUrl ..."
    $release = Invoke-RestMethod -Uri $indexUrl -Headers $headers
}
$tag = $release.tag_name
if (-not $tag) { throw "无法从发布信息解析 tag_name: $indexUrl" }

# --- 3. 下载地址(与 install.sh 相同的资产配对规则) -----------------------
function Get-AssetUrl {
    param([string]$name)
    $asset = @($release.assets) | Where-Object { $_.name -eq $name } | Select-Object -First 1
    if ($asset) { return $asset.browser_download_url }
    return $null
}
$exeUrl = Get-AssetUrl $binName
$zipUrl = Get-AssetUrl $zipName
$sumUrl = Get-AssetUrl 'sha256sums.txt'
if (-not $exeUrl) { throw "release $tag 中没有资产 $binName" }
if (-not $sumUrl) { throw "release $tag 中没有 sha256sums.txt" }

# --- 4. 下载 + 校验 ------------------------------------------------------
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('gotty-install-' + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
$zipPath = Join-Path $tmp $zipName
$sumPath = Join-Path $tmp 'sha256sums.txt'
$dlName = $zipName
$downloadedArchive = $false

try {
    # 优先下载压缩包(慢网络下更小);失败回退原始二进制。
    if ($zipUrl) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $zipUrl -OutFile $zipPath
            Write-Host "Downloaded $zipName (release $tag)."
            $downloadedArchive = $true
        } catch {
            Write-Host "Archive $zipName unavailable, falling back to raw binary ..."
        }
    }
    if (-not $downloadedArchive) {
        $dlName = $binName
        Invoke-WebRequest -UseBasicParsing -Uri $exeUrl -OutFile (Join-Path $tmp $binName)
        Write-Host "Downloaded $binName (release $tag)."
    }
    Invoke-WebRequest -UseBasicParsing -Uri $sumUrl -OutFile $sumPath

    $entries = @{}
    Get-Content $sumPath | ForEach-Object {
        if ($_ -match '^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$') {
            $entries[$matches[2]] = $matches[1].ToLower()
        }
    }
    $expected = $entries[$dlName]
    if (-not $expected) { throw "sha256sums.txt 中没有 $dlName 的条目" }
    $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $dlName)).Hash.ToLower()
    if ($actual -ne $expected) {
        throw "checksum mismatch for $dlName — download aborted, nothing installed (got $actual, expected $expected)"
    }
    Write-Host "Checksum OK (sha256 $expected)."

    # --- 5. 解压出二进制 -------------------------------------------------
    if ($downloadedArchive) {
        Write-Host "Extracting $binName ..."
        Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
    }
    $src = Join-Path $tmp $binName
    if (-not (Test-Path $src)) {
        throw "archive 中没有 $binName"
    }

    # --- 6. 安装到 <Prefix>\bin ------------------------------------------
    $binDir = Join-Path $Prefix 'bin'
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    $bin = Join-Path $binDir 'gotty.exe'
    try {
        Copy-Item -Path $src -Destination $bin -Force
    } catch {
        throw "无法写入 $bin (gotty.exe 可能正在运行,请先关闭再重试): $($_.Exception.Message)"
    }
    Write-Host ''
    Write-Host "Installed to $bin"

    # --- 7. User PATH 幂等配置(原生 Windows 对 ~/.bashrc 的等价物) -------
    if ($NoPathConfig) {
        Write-Host 'Skipped User PATH configuration (-NoPathConfig).'
    } elseif (-not $IsWin) {
        Write-Warning '非 Windows 环境:跳过 User PATH 配置(仅模拟模式)。'
    } else {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $parts = @()
        if ($userPath) { $parts = @($userPath -split ';' | Where-Object { $_ }) }
        if ($parts -contains $binDir) {
            Write-Host "PATH entry already present in User PATH, skipping."
        } else {
            $newPath = $userPath
            if ($newPath) { $newPath = $newPath.TrimEnd(';') + ';' + $binDir } else { $newPath = $binDir }
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            Write-Host "Added $binDir to your User PATH."
        }
    }

    Write-Host ''
    Write-Host 'Installed. Open a NEW terminal — cmd / PowerShell / Git Bash all pick it up.'
    Write-Host ''
    if (Test-Path $bin) {
        & $bin version
        if ($LASTEXITCODE -ne 0) { Write-Warning "无法运行 $bin 验证版本(退出码 $LASTEXITCODE)" }
    }
    Write-Host ''
    Write-Host 'next steps:'
    Write-Host '  - 打开终端:         gotty serve top'
    Write-Host '  - 升级到新版本:     gotty self update'
    Write-Host "  - systemd 部署指引: https://github.com/$Repo#run-as-a-systemd-user-service"
}
finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}