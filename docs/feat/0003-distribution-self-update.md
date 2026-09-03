# 优化 3:分发与自更新 —— install.sh + gotty self update

> 状态:**已实施(2026-08-29)**
>
> 实施摘要:Makefile 版本改 `git describe --tags` 派生,构建矩阵扩到 5 平台
> (linux/darwin/windows × amd64/arm64),release 末尾生成 `sha256sums.txt`;
> 新增 `scripts/install.sh`(探测→取发布→下载→校验→装到用户目录,零 sudo);
> 新增 `gotty self update`(semver 比较、sha256 校验、同目录临时文件 + rename
> 原子替换、`--dry-run`/`--check`/`--yes`/`--version`/`--repo`/`GOTTY_UPDATE_URL`
> 覆盖)与 `gotty version --json`;新增 `internal/update` 包(纯函数可单测);
> release.yml 上传 5 平台资产 + 校验和。不做包管理器发行(Homebrew/deb/rpm)
> 与常驻升级检查——见 §5 范围外。
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
  curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh
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

## 3. 涉及文件(已实施)

| 文件 | 改动 |
|---|---|
| `Makefile` | `VERSION` 改 `git describe --tags --always --dirty`;`PLATFORMS` 扩到 5 平台;windows 出 `.exe`;UPX 仅 linux(缺 upx 时跳过);release 末尾生成 `sha256sums.txt` |
| `scripts/install.sh` | 新增(探测/取发布/下载/`sha256sum`/`shasum -a 256` 校验/安装到 `--prefix`,默认 `$HOME/.local/bin`,零 sudo;`--version`/`--prefix`/`--repo`/`GOTTY_UPDATE_URL`) |
| `cmd/root.go` | 注册 `self update`、`version` |
| `cmd/version.go` | 新增:`gotty version`(人类可读)`--json`(name/version/commit/go_version/os/arch) |
| `cmd/selfupdate.go` | 新增:`--repo`/`--version`/`--yes`/`--dry-run`/`--check`;`GOTTY_UPDATE_URL` 覆盖索引地址 |
| `internal/update/semver.go` | 新增:semver 解析与比较(v 前缀、pre-release、build 元数据忽略) |
| `internal/update/github.go` | 新增:release 索引客户端(`releases/latest`、`releases/tags/{tag}`、自定义 URL)、资产查找、下载 |
| `internal/update/checksum.go` | 新增:`sha256sums.txt` 解析与摘要校验(篡改中止) |
| `internal/update/replace.go` | 新增:同目录临时文件 + fsync + rename 原子替换,失败保留旧二进制 |
| `internal/update/update.go` | 新增:端到端流程编排(比较 → 提示变更 → 确认 → 下载校验 → 替换) |
| `internal/update/update_test.go` | 新增:semver 边界、校验失败中止、原子替换失败保留旧二进制 |
| `.github/workflows/release.yml` | 从 2 平台扩到 5 平台 + `sha256sums.txt` 上传;新增资产校验回归步骤 |
| `README.md` / `README.zh-CN.md` | 安装/升级一节重写(一键装 + self update + make release) |

实现说明(与 §2 的偏差与细化):

- `GOTTY_UPDATE_URL` 语义:指向一个 **GitHub release 对象同形状的 JSON 索引**
  (自建静态站点托管 `latest.json` 即可),替代 GitHub API——`--repo` 在该
  模式下被忽略;资产 URL 从索引内 `assets[].browser_download_url` 解析。
- `--check` 的"发现新版本"以退出码 1 表达(main 打印 `Error: ...` 到
  stderr),供脚本判断;`--check`/`--dry-run` 均不落盘。
- 本地版本非 semver(git describe 无 tag 的 hash、开发构建)时视为落后于
  任何发布版,但会如实提示;`already up to date` 比较用 release tag 与本地
  `cmd.Version`,build 元数据忽略。

## 3.5 发布清单(每次发版)

1. `git tag v2.1.0` && `git push origin v2.1.0`;
2. CI(release.yml)在 tag 上构建 5 平台矩阵 + `sha256sums.txt`,上传资产,
   `git log` 自动生成发布说明;
3. 校验:安装脚本回归 `cd build && sha256sum -c --ignore-missing sha256sums.txt`
   已内置于工作流;抽查 `gotty version --json` 的 version 字段等于 tag;
4. 更新 README:示例命令中的版本号(install.sh / self update 默认取 latest,
   一般无需改,仅在变更示例时)。

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
