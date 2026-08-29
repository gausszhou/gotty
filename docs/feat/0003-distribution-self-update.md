# 优化 3:分发与自更新 —— install.sh + gotty self update

> 状态:**待实施**(对标 `tu` 的安装体验调研结论)
>
> 背景:`tu` 的分发做得极省心——`curl .../install.sh | sh` 一键装、
> `tu self update` 自更新、crates 兜底 `cargo install`。我们目前只有
> Makefile 手动构建:`make release` 产出 `gotty-linux-amd64` /
> `gotty-linux-arm64`(UPX 压缩),无校验和、无安装脚本、无自更新、版本号
> 硬编码在 Makefile(`VERSION ?= 2.0.0`)。单二进制 + go:embed 是很好的分发
> 基础,缺的只是最后一公里。

## 1. 现状盘点

| 项 | 现状 | 缺口 |
|---|---|---|
| 版本注入 | Makefile `LDFLAGS` 用 `-X cmd.Version` / `CommitID` 注入,`gotty -v` 可看 | 版本号硬编码 `VERSION ?= 2.0.0`,与 git tag 脱节 |
| 构建矩阵 | `make release` 只出 linux/amd64 + arm64 | 无 macOS/Windows |
| 校验 | 无 sha256sums 资产 | 安装无法验真 |
| 安装 | README 手把手(下载 Releases 页二进制) | 无一键脚本 |
| 更新 | 无 | 无自更新,升级 = 手动重下 |
| CI | `.github/workflows/ci.yml` 只跑测试 | 无 tag 触发的发布工作流 |

## 2. 设计

### 2.1 版本单一来源

- 版本号改为 `git describe --tags` 派生:`VERSION ?= $(shell git describe
  --tags --always --dirty 2>/dev/null || echo 2.0.0)`;发布时打
  `v2.1.0` 格式 tag,构建产物版本即 tag。
- `gotty -v` 输出保持 `Version+CommitID`;新增 `gotty version --json`
  (name/version/commit/go_version/os/arch),供自更新与排查用。

### 2.2 构建矩阵 + 校验和

- Makefile `PLATFORMS` 扩展为 `linux/amd64 linux/arm64 darwin/amd64
  darwin/arm64 windows/amd64`(windows 输出 `.exe`);UPX 仅对 linux 执行
  (macOS 上 UPX 会破坏签名,Windows 上可选)。
- `release` 目标末尾生成 `sha256sums.txt`(与二进制同目录),资产命名沿用
  `gotty-{os}-{arch}[.exe]`,与 install.sh 的映射保持一致。
- 新增 `.github/workflows/release.yml`:tag `v*` 推送触发,矩阵构建上述
  平台、上传资产 + `sha256sums.txt`、用 `git log` 生成草稿发布说明。

### 2.3 `scripts/install.sh`(对齐 `tu` 一键装)

- 用法:
  ```sh
  curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/main/scripts/install.sh | sh
  # 或指定版本/前缀:
  #   sh install.sh --version v2.1.0 --prefix ~/.local
  ```
- 流程:
  1. 探测平台/架构(`uname -s`/`uname -m`,映射 amd64/arm64、.exe);
  2. 取最新 tag(GitHub API `releases/latest`,`--version` 则跳过查询);
  3. 下载二进制与 `sha256sums.txt` 到临时目录;
  4. `sha256sum -c` 校验,失败即退出并清理(防投毒/损坏);
  5. 安装到 `--prefix`(默认 `$HOME/.local/bin`,不存在则回退提示
     `sudo /usr/local/bin`),`chmod +x`;
  6. 打印 `gotty --version` 与 systemd 部署指引链接。
- 权限模型:默认装到用户目录,**不请求 sudo**(对齐 tu 与我们的
  `systemctl --user` 部署文档);装系统目录由用户显式指定。

### 2.4 `gotty self update`(对齐 `tu self update`)

- 新增子命令(挂在 root 下),行为:
  1. 查询最新版本(默认 GitHub `releases/latest`,支持
     `--repo owner/name` 与 `GOTTY_UPDATE_URL` 覆盖,私有部署可指向
     自建静态站点);
  2. `semver` 比较:本地已最新 → `already up to date` 退出 0;落后 → 提示
     变更,`--yes` 跳过确认;
  3. 下载新二进制 + 校验和到**当前可执行文件同目录**的临时文件,校验
     sha256;
  4. 原子替换:`os.Rename(temp, exePath)`(同目录保证同文件系统),
     成功后打印 `updated to vX → 重启服务生效`;替换失败(目录不可写)
     → 报错并提示 `install.sh` 路线,保留旧二进制;
  5. `--dry-run` 只查不装;`--check` 只报告版本差。
- 约束:不自动重启正在运行的进程(会话存活是 GoTTY 的卖点,杀掉服务会丢
  全部会话)——只替换二进制,由用户/systemd 重启;文档明确这一点。

### 2.5 README 与文档

- README「Installation」一节替换为:`curl ... install.sh | sh` 一键安装 +
  `gotty self update` 升级 + `make release` 出资产三行说明;保留源码构建
  路径。
- docs 增加"发布清单"(打 tag → CI 出资产 → 校验和 → 更新 README 版本号)。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `Makefile` | `VERSION` 改 git describe;`PLATFORMS` 扩展;release 生成 `sha256sums.txt`;windows `.exe` |
| `scripts/install.sh` | 新增(探测/下载/校验/安装) |
| `cmd/root.go` | 注册 `self update`、`version --json` |
| `cmd/selfupdate.go` | 新增(查询/比较/下载/校验/原子替换) |
| `internal/update/` | 新增包:GitHub API 客户端、semver 比较、原子替换(可单测) |
| `.github/workflows/release.yml` | 新增(tag 触发发布) |
| `README.md` / `README.zh-CN.md` | 安装/升级一节重写 |

## 4. 测试与验收

- 单测(`internal/update`):semver 比较边界(前缀 v、pre-release)、
  校验和失败中止、原子替换失败保留旧二进制(用临时目录模拟)。
- 手工验收:
  1. 全新容器里 `curl install.sh | sh` 后 `gotty -v` 可用;
  2. 本地装 2.0.0,`gotty self update --repo <fork> --version v2.1.0 --dry-run`
     显示版本差;实际更新后二进制替换成功、旧二进制不残留;
  3. 篡改下载内容 → 校验失败退出且不落盘;
  4. 二进制所在目录不可写 → 报错并提示 install.sh,进程仍可用。
- CI:release.yml 在 tag 上跑通并产出资产 + 校验和(先在 fork 仓库试一次)。

## 5. 不做的事(范围外)

- 不做包管理器发行(Homebrew/deb/rpm)——社区有此需求再议,install.sh 已
  覆盖 90% 场景。
- 不做 Windows 安装脚本(PowerShell)——README 保留手动下载指引。
- 不自带守护/常驻升级检查——只有用户显式运行 `self update` 才联网,保持
  离线安装可用与零后台流量。
