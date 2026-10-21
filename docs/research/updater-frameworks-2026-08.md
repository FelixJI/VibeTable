# Windows 桌面应用更新框架调研（截至 2026-08-12）

> 历史调研归档：本文来自 2026-08 的本地调研草稿，于 2026-10-21 整理入库。下文的“当前”“最新”“推荐”和六仓现状均指原调研时点；本次未重新核验外部版本、接口及维护状态，也未执行框架 PoC。本文不是已批准的迁移方案或当前依赖选型依据。若启动实施，须重新读取官方资料、当前代码与发布契约，并验证下文列出的生命周期和数据保全边界。
>
> VibeTable 当前更新行为请以代码、[自我更新能力与安全边界](../self-update-assessment.md)和[更新健康回退 ADR](../adr/0010-process-out-update-health-rollback.md)为准。

## 执行摘要

本调研只采纳项目官方仓库、官方文档、GitHub Releases、PyPI、NuGet、pkg.go.dev 和 Microsoft Learn 等一手资料；“截至”表示在 2026-08-12 检索到的公开版本/状态，而不是对未来维护的承诺。

* **不应新接入**：PyUpdater（已归档、PyPI 最新 4.0 为 2021-04-19）、Squirrel.Windows（官方仓库征集维护者、最新 release 为 2020-09-27）、Clowd.Squirrel（NuGet 明示 legacy/not maintained），以及 `sanbornm/go-selfupdate`、`rhysd/go-github-selfupdate`（旧生态，后者模块最新 v1.2.3 为 2020-07-02）。它们可仅作为存量兼容/一次迁移跳板。
* **Windows/.NET 主推荐**：优先评估 **Velopack 1.2.0**。它有现行发布、MIT 许可证、Windows 包/增量包、`SimpleWebSource` 与 `GithubSource`、可传 downloader 的构造器，以及可控的退出、安装、重启路径。它是 Squirrel/Clowd 的迁移方向，但不是 TUF：仍须由发布流水线提供不可变资产、哈希/签名和签名验证策略。
* **需要 Windows 平台原生部署与受管更新时**：评估 **MSIX + App Installer**。Windows 强制签名，App Installer 支持启动时检查、静默/阻断启动和多 `UpdateURI` 回退；但它不是 GitHub Releases 客户端，也不是通用 Python/Go 单文件自更新器。
* **本组 Python 桌面应用**：File Toolbox 与 VibeOCR Classic 都是 PyInstaller `onedir` 的 Windows 整包应用，先做 **Velopack Python 1.2.0** PoC；官方已明确支持 Python、PyInstaller `onedir` 和目录级打包/更新。若 Python SDK 无法满足用户可配置 GitHub 前缀代理，或产品决定继续只发便携 ZIP并愿意运营独立签名仓库，则改用 **tufup 0.10.0 + python-tuf/TUF**。这是一项项目适配结论，不要求其他 Python 项目也选 Velopack。
* **仅限独立 Go CLI**：若未来有独立分发的 Go 可执行程序，且必须从 GitHub/Gitea/GitLab Release 发现更新，可用仍在发布的 **`github.com/creativeprojects/go-selfupdate` v1.6.0**，并启用 SHA-256 或 ECDSA validator。VibeTable 的 PocketBase sidecar 必须随宿主整包原子更新，不使用任何 Go self-update 库。

## 评价维度与判定规则

| 维度 | 本调研的判定方式 |
|---|---|
| 维护性 | 官方最新 release/包发布时间、仓库或包页是否明确 legacy/archived；不以 star 数代替维护。 |
| 更新模型 | 更新的是安装目录树、MSIX 包还是当前单一可执行文件；是否支持后台下载、退出后替换、重启。 |
| 信任边界 | Authenticode/MSIX 签名、TUF 签名元数据、内置 checksum/ECDSA validator 分别记载；没有证据就标“未见”。TLS、GitHub tag 和下载 URL 本身不等于资产身份。 |
| 增量/回滚 | 明确区分 delta/patch、下载失败时的二进制回滚、以及“可回退到旧业务版本”（后者通常需产品自行设计）。 |
| 来源与可扩展性 | 是否明确支持 GitHub Releases、静态 HTTP feed、可配置 base URL、注入 downloader/requester/provider；没有接口证据的 URL 改写一律标为 adapter。 |
| 产品适配 | VibeTable 为 Windows 10/11 x64、WPF + Python BFF + Go sidecar；不能因为某库可更新 CLI 就推断能安全替换被运行中/被宿主锁定的 sidecar。 |

## 六仓现状与更新所有权

| 仓库 | 当前交付/更新形态 | 应拥有应用更新器吗 | 本轮结论 |
|---|---|---|---|
| **VibeTable** | .NET 10 WPF/WebView2 宿主整包携带 Python BFF 与 Go/PocketBase sidecar；自研 GitHub Release client、ZIP 事务、代理和真实 self-update smoke | **是，由 WPF 宿主拥有整个产品闭包** | 首选 Velopack C# PoC；若退 NetSparkle，仍需另建并签名 installer，不能直接沿用 ZIP 作为 Windows 安装动作。Go sidecar 禁止单独 self-update。[现有边界](../self-update-assessment.md) |
| **File Toolbox** | PySide6 + PyInstaller `onedir` 便携 ZIP；自研 versions/downloader/BAT replacer，已有 GitHub 前缀代理 | **是，由 GUI/主入口拥有整个 onedir** | 首选 Velopack Python PoC；若 Python binding 的代理 seam 不达标，则用 tufup/TUF。不要接 PyUpdater。[updater](https://github.com/FelixJI/file-toolbox/tree/main/file_toolbox/updater) · [发布契约](https://github.com/FelixJI/file-toolbox/blob/main/.ci/project.json) |
| **VibeOCR Classic** | PySide6 + PyInstaller；新包内 staged `updater.exe` 接管，ready/health 握手、备份回滚，整包绑定 Backend/Protocol | **是，由 Classic 壳拥有整个绑定产品** | 首选 Velopack Python PoC，重点验证 200–400 MB 量级 delta、健康失败回退和用户数据保留；tufup/TUF 为安全优先备选。[现有 updater](https://github.com/FelixJI/vibeocr-classic/blob/main/scripts/update_replacer.py) · [发布契约](https://github.com/FelixJI/vibeocr-classic/blob/main/.ci/project.json) |
| **VibeOCR Next** | unpackaged WinUI (`WindowsPackageType=None`) + Bootstrapper；.NET GitHub source 下载 ZIP，再抽取 Python `updater.exe` 执行替换 | **是，由 WinUI/Bootstrapper 拥有整个绑定产品** | 四仓中最优先做 Velopack C# PoC，目标是消除 .NET 发现层 + Python 替换器的跨语言更新切面。[现有 source](https://github.com/FelixJI/vibeocr-next/blob/main/src/dotnet/VibeOCR.App/Features/Update/GitHubUpdateSource.cs) · [发布契约](https://github.com/FelixJI/vibeocr-next/blob/main/.ci/project.json) |
| **VibeOCR Backend** | UI-free runtime component；发布 wheels、Python runtime、profiles、runtime manifest 与 installer ZIP，由前端绑定精确版本 | **否** | 继续使用 manifest/installer/内容身份，由 Classic/Next 整体候选选择与安装；不要套 PyUpdater/Squirrel。[发布契约](https://github.com/FelixJI/vibeocr-backend/blob/main/.ci/project.json) |
| **VibeOCR Protocol** | 生成器与 Python wheels/.NET NuGet/OpenAPI/schema 制品 | **否** | 继续通过依赖锁和消费方 release binding 升级；不要内置应用 self-updater。[发布契约](https://github.com/FelixJI/vibeocr-protocol/blob/main/.ci/project.json) |

这里的关键不是让六仓统一，而是让每个**可安装桌面产品**只有一个更新生命周期所有者；组件仓和 sidecar 不自行追逐“latest release”。

## 候选矩阵

| 方案（状态/最近公开版本） | 许可证、平台 | 更新与安装模型 | 信任、全量/差分、回滚 | GitHub Releases / source 扩展 | 新接入结论 |
|---|---|---|---|---|---|
| **Velopack**（NuGet 1.2.0；2026-07-16 有 1.2.110 prerelease） | MIT；Windows/macOS/Linux，C#、C++、JS、Rust、Python 等绑定 | 安装器/portable；下载后 `ApplyUpdatesAndRestart/Exit` 或等待退出；Windows 以 `current` 目录切换 | Windows 生成 delta；非 Windows 首次更新为 full；建议代码签名。未找到“自动回退到任意旧业务版本”的官方承诺 | `GithubSource`、静态 `SimpleWebSource`；后者构造器含 downloader/timeout | **首选 .NET/WPF**；Python 绑定另做 PoC |
| **MSIX + App Installer**（Microsoft 平台能力） | Windows 10 2004+ / Windows 11；许可证随 Windows 工具链 | App Installer URI 更新；可每次启动检查、静默/提示、阻断激活；`UpdateURI` fallback | MSIX 部署必须由受信证书签名；差分/回滚不作为本调研中 App Installer 的通用承诺 | 只证明 URI/update hosting 与 fallback，不证明 GitHub Releases 专属客户端或 URL 改写 | **受管 Windows 部署首选备选**；需接受 MSIX 身份/签名约束 |
| **tufup 0.10.0**（2025-10-04） | MIT、Python >=3.9；client 主要 Windows/macOS | 任意 bundle（含 PyInstaller）；默认 Windows 新进程安装，也可注入 install script | 基于 python-tuf/TUF；archive + `.patch`，patch 总量不划算则 full；TUF 保护元数据/目标，业务回滚需自设 | 不是 GitHub Releases client；部署到更新服务器由项目负责，需 adapter/发布流程 | **Python 强信任/ZIP-only 备选** |
| **PyUpdater 4.0**（2021-04-19） | MIT；面向 PyInstaller，PyPI 标 OS independent | PyInstaller 紧耦合的 app/asset 包与插件上传 | 自定义密钥/元数据机制；文档有 patch 依赖，但不把它等同 TUF；未核实当前可用的 GitHub source | 官方安装页只列 S3/SCP upload plugins；GitHub Release/URL 重写没有本调研认可的接口证据 | **淘汰：仅存量迁移** |
| **creativeprojects/go-selfupdate v1.6.0**（2026-07-08） | MIT；Windows/Linux/macOS | 发现 release、下载匹配 OS/arch asset、替换单 binary；失败 rollback | SHA-256/ECDSA validator、可自定义验证器；完整或压缩 asset，未主张 binary diff | GitHub/Gitea/GitLab providers + HTTP based repository；provider 抽象 | **Go 直接 Release 更新首选**；必须启用 validator |
| **sanbornm/go-selfupdate**（仓库无 Releases） | MIT；Mac/Linux/ARM/Windows | 静态 HTTP JSON manifest + binary/diff URL；异步 run，重启由 hook 自管 | SHA-256；bsdiff，差分不匹配退 full；无签名字段的实现承诺 | `ApiURL`/`BinURL`/`DiffURL` 及 `Requester` 注入 | **不建议新接入**：老协议，可作为自建静态 feed 的历史参考 |
| **rhysd/go-github-selfupdate v1.2.3**（2020-07-02） | MIT；Linux/macOS/Windows | GitHub Releases API 发现、按 OS/arch 下载并替换 | hash/ECDSA validator、失败 rollback | GitHub/GHE，`Updater` 可配 token/API base URL | **不建议新接入**：由 creativeprojects 后继取代 |
| **Squirrel.Windows 2.0.1**（2020-09-27） | MIT；Windows、C#/native C++ | NuGet 包、HTTP channels、后台更新、不重启系统 | 支持 delta；官方 README 未给出 TUF/下载资产签名验证承诺 | 简单 HTTP；没有本调研认可的 GitHub mirror rewrite API | **淘汰：仅迁移存量** |
| **Clowd.Squirrel 2.11.1** | MIT；.NET | Squirrel 后继实现 | NuGet 已标 legacy/not maintained、建议 Velopack | 应迁移 Velopack | **淘汰：不新接入** |
| **NetSparkle 3.0.4** | MIT；.NET 6+/Framework，WPF/WinForms/Avalonia | appcast 发现、下载并运行 `.exe/.msi/.msp`；不构建 installer | 严格 Ed25519；安装、版本目录和业务回滚由外部 installer/产品负责 | `IAppCastDataDownloader`、`IUpdateDownloader`、可覆盖 HttpClient | **低迁移备选**：替换发现/下载层，但不能单独消灭自研安装事务 |
| **WinSparkle** | MIT；Windows C API，含 C#、Python、Go bindings | Sparkle appcast + 下载/运行 updater/installer | Ed25519 签名；安装与业务回退依赖外部 updater | appcast 可自托管；代理需按 binding/HTTP 层验证 | **多语言兼容备选**，不如 Velopack 适配本组整包产品 |
| **AutoUpdater.NET 1.9.3** | MIT；.NET desktop | XML feed + 下载/启动 installer | 可选 checksum；无 TUF/一体化版本目录承诺 | 可自托管 XML，GitHub 仅作为普通 URL | **不进入决选**：能力过轻，仍留下大部分自研安装/回滚责任 |

## 逐项一手证据

### 1. PyUpdater：存量兼容，不再选择

PyPI 的发布历史显示 4.0 于 2021-04-19 上传，许可证为 MIT；项目说明仍将其定位为 PyInstaller 的 auto-update library。[PyPI PyUpdater 4.0](https://pypi.org/project/PyUpdater/4.0/)

官方文档要求生成 keypack，且明确 app 内置的密钥用于验证 update metadata；其安装页列出 S3、SCP upload plugins。该自定义安全设计不能替代 TUF 的角色/阈值/过期元数据模型，且本调研没有找到官方 GitHub Releases source、可注入 downloader 或安全 URL rewrite 的接口证据。[PyUpdater CLI usage](https://www.pyupdater.org/usage-cli/) [PyUpdater installation](https://www.pyupdater.org/installation/)

**判定**：不要为新功能接入。已在现场运行的客户端可先用 PyUpdater 发布一个带 tufup 或 Velopack client 的过渡版本；tufup 官方文档也给出了保留旧 repository 直到旧客户端迁移完成的步骤。[tufup migration](https://pypi.org/project/tufup/)

### 2. tufup：Python 的安全优先替代

tufup 官方 PyPI 说明其为 PyUpdater replacement，基于 python-tuf；0.10.0 发布于 2025-10-04，MIT、Python >=3.9。它不依赖具体打包器，PyInstaller、PEX 或任意目录 bundle 都可用。[tufup PyPI](https://pypi.org/project/tufup/)

发布端为 archive 生成 patch，把 archive/patch 哈希写入 TUF metadata 并签名；客户端优先 patch，patch 总量大于 full archive 时改用 full。Windows 默认 installer 在新进程启动后等待当前进程退出，并允许 `Client.update()` 的 `install` 参数改为自定义安装器。[tufup PyPI—archives/installation](https://pypi.org/project/tufup/)

TUF 客户端应随应用携带 trusted root，并使用 `metadata_base_url`；官方 python-tuf 文档强调 bootstrap root 与可写 metadata cache 的分离。这为“镜像可替换、身份不变”提供了正确边界：镜像提供 bytes，TUF 签名 metadata 与 target hash 才给出身份。[python-tuf installation/security guidance](https://theupdateframework.readthedocs.io/en/stable/INSTALLATION.html)

**限制**：它不是 GitHub Releases API 客户端；GitHub 只能作为发布流水线或静态对象托管的一部分，必须将 TUF metadata/targets 的路径、缓存控制、原子上传和密钥隔离落实到 adapter/部署设计。没有官方承诺自动回滚已经成功安装的业务版本，升级前的备份、健康检查、失败回切须由自定义 install script/launcher 负责。

### 3. Go：区分旧库、后继库与底层替换库

`sanbornm/go-selfupdate` 的 README 证明它支持 Windows，使用 HTTP(S) manifest，具有 `ApiURL`、`BinURL`、`DiffURL`、`Requester` 和 `OnSuccessfulUpdate`；使用 bsdiff，diff 的 SHA 不匹配时回落 full binary，重启由调用者 hook 决定。其 manifest 当前只定义 `Sha256`，README 只说将来“可能”加 signatures，故不得把它描述成签名更新框架。[sanbornm/go-selfupdate README](https://github.com/sanbornm/go-selfupdate)

`rhysd/go-github-selfupdate` 直接调用 GitHub Releases API，支持 GitHub Enterprise、私库、按 OS/arch 选 asset、失败 rollback、SHA-256/ECDSA validator；但 pkg.go.dev 显示 v1.2.3 的发布时间为 2020-07-02。它的可配 API base URL 是 GHE 支持，不是“任意下载 URL mirror”声明。[rhysd README](https://github.com/rhysd/go-github-selfupdate) [pkg.go.dev v1.2.3](https://pkg.go.dev/github.com/rhysd/go-github-selfupdate)

`creativeprojects/go-selfupdate` v1.6.0 于 2026-07-08 发布，MIT，明确是从 rhysd fork 演进；官方 API/README 列出 GitHub、Gitea、GitLab 与 HTTP based repository provider，Windows/Linux/macOS、rollback、SHA-256/ECDSA 和自定义 validator。选择 GitHub provider 时 release tag/asset 命名仍只是**发现**机制；应让 validator 以项目内置公钥校验 `.sig`，或校验由受控 release manifest 绑定的 SHA-256。[pkg.go.dev creativeprojects/go-selfupdate](https://pkg.go.dev/github.com/creativeprojects/go-selfupdate)

`inconshreveable/go-update`/`minio/selfupdate` 是“替换单文件”的底层能力，不做 release 发现。后者 README 明示源自前者；可用作自建 TUF/GitHub adapter 的原子替换实现，但不应被误称为完整 auto-update 产品。[minio/selfupdate README](https://github.com/minio/selfupdate) [pkg.go.dev update API](https://pkg.go.dev/github.com/creativeprojects/go-selfupdate/update)

### 4. Squirrel.Windows、Clowd.Squirrel 与 Velopack

Squirrel.Windows 官方 README 仍描述 NuGet packaging、simple HTTP channel、delta package、后台更新与无 reboot；同页却公开“Contributors Needed”，GitHub latest release 为 2.0.1（2020-09-27）。它适合解释旧客户端协议，**不适合**新产品依赖；README 也未提供 TUF 或下载包签名验证的承诺。[Squirrel.Windows repository](https://github.com/Squirrel/Squirrel.Windows)

Clowd.Squirrel 的 NuGet 页直接标为 deprecated：legacy/not maintained，并给出 Velopack 作为 suggested alternative 和 rename 说明，因此不能把 Clowd 当作“仍维护的 Squirrel”。[NuGet Clowd.Squirrel](https://www.nuget.org/packages/Clowd.Squirrel/)

Velopack 官方仓库为 MIT，latest stable 1.2.0；它声明一次 `pack` 可产生 installer、updates、delta package 与 portable package，且支持从 Squirrel/Clowd 迁移。[Velopack repository](https://github.com/velopack/velopack) [NuGet Velopack](https://www.nuget.org/packages/Velopack)

其更新 API 官方支持静态 HTTP `SimpleWebSource` 和 `GithubSource`；`SimpleWebSource(baseUrl, downloader, timeout)` 是明确的自定义 downloader 注入点。Windows `Update.exe apply` 有 `--waitPid`、`--norestart`；SDK 的 `ApplyUpdatesAndRestart/Exit` 会退出、安装并选择性重启，未调用时下次启动可应用 pending update。[Velopack update sources](https://docs.velopack.io/integrating/update-sources) [SimpleWebSource API](https://docs.velopack.io/reference/cs/Velopack.Sources/SimpleWebSource) [apply lifecycle](https://docs.velopack.io/integrating/overview) [Update.exe](https://docs.velopack.io/reference/cli/content/update-windows)

Windows 打包文档建议 distribution 前 code-sign；更新替换 `current`，锁定时 updater 会尝试处理相关进程或向用户提供终止/取消。故产品必须避免把数据库、工作区或用户数据放进 `current`，并把 BFF/sidecar 的退出协议纳入更新编排。[Velopack Windows packaging](https://docs.velopack.io/packaging/operating-systems/windows)

特别是，官方文档说明 Velopack 会尝试终止 `current` 内仍运行的进程。对 VibeTable/Next，这不是可直接接受的默认行为：宿主必须先通过现有协议优雅停止 BFF/sidecar、flush 状态并确认 PID/句柄退出，再交给 Velopack；若 PoC 证明仍可能在产品 shutdown gate 之前强制结束业务进程，应退出 Velopack 方案。

### 5. MSIX + App Installer：平台签名与多 URI 回退

Microsoft 文档规定部署 MSIX 需要有效代码签名且设备必须信任证书链；生产建议使用受信 CA/managed signing 或 Store 签名。[Microsoft—Sign an MSIX package](https://learn.microsoft.com/en-us/windows/msix/package/signing-package-overview)

App Installer 的 2021 schema 可在每次启动检查、隐藏提示或阻断激活，且主 App Installer URI 不可达时按 `UpdateURI` 顺序尝试 fallback。这是官方支持的**更新描述文件 URI fallback**，不等同于重写 GitHub API/asset URL。[Microsoft—auto-update and repair](https://learn.microsoft.com/en-us/windows/msix/app-installer/auto-update-and-repair--overview)

### 6. NetSparkle、WinSparkle、AutoUpdater.NET：可替换下载层，不等于替换安装生命周期

NetSparkle 3.0.4 仍支持 .NET 6+、WPF 和严格 Ed25519，且明确暴露 `IAppCastDataDownloader`、`IUpdateDownloader` 和可覆盖 `CreateHttpClient` 的 downloader，代理 adapter 的 seam 比许多轻量库清晰。但官方也明确不负责构建/上传 installer，Windows 仍需 Inno Setup、NSIS、WiX 等。因此它适合在不改变发布安装形态时替换 GitHub 检查/下载代码，不足以单独替换本仓目录替换、启动健康和回滚逻辑。[NetSparkle repository](https://github.com/NetSparkleUpdater/NetSparkle) [NetSparkle NuGet](https://www.nuget.org/packages/NetSparkleUpdater.SparkleUpdater/)

WinSparkle 使用 Sparkle appcast 和 Ed25519，提供 C API 及 C#、Python、Go bindings，但同样依赖外部 updater/installer 完成真正安装。[WinSparkle repository](https://github.com/vslavik/winsparkle)

AutoUpdater.NET 的官方 README 证明 XML feed 可带 checksum，适合很薄的“发现、下载、启动 installer”；它没有 Velopack 的打包、delta、版本目录和完整 lifecycle，因此不会显著减少当前四个产品最难的自研代码。[AutoUpdater.NET repository](https://github.com/ravibpatel/AutoUpdater.NET)

## “GitHub 代理”设计：必须区分两类

### A. 标准 HTTP(S) forward proxy

这是网络传输层配置：客户端仍请求原始 GitHub API/asset URL，代理负责 CONNECT/转发。框架是否使用系统代理、环境变量或自定义 `HttpClient`/downloader 必须在目标运行时 PoC 验证，不能因“支持 HTTP”就假设支持。已证实的扩展点：

* Velopack 静态 source 的构造器有 `downloader`；在该 downloader 内配置 proxy 是可实现的适配点。[SimpleWebSource API](https://docs.velopack.io/reference/cs/Velopack.Sources/SimpleWebSource)
* `sanbornm/go-selfupdate` 有 `Requester` 覆盖接口；creativeprojects 通过 source/provider 分层支持 HTTP repository，但具体 proxy transport 仍应在实现/PoC 中显式注入。[sanbornm config](https://github.com/sanbornm/go-selfupdate) [creativeprojects API](https://pkg.go.dev/github.com/creativeprojects/go-selfupdate)
* TUF/tufup 应通过其 HTTP client/transport 或受控静态 origin 配置实现；保持 bootstrap root、metadata URL 和 target URL 的 TLS/签名校验，不把 proxy 当信任根。[python-tuf docs](https://theupdateframework.readthedocs.io/en/stable/INSTALLATION.html)

### B. 将 GitHub 下载 URL 改写为镜像/前缀

这是**内容发现与身份**问题，不能称为“代理支持”。只有当 API 明示 base URL/provider/source 时，才可直接使用；否则写 adapter。规则如下：

1. **保留原始身份**：release tag、asset name、精确 SHA-256 或签名/TUF target metadata 共同绑定。仅保留 tag/asset name 不足以证明 bytes 身份。
2. **Velopack C#**：对自托管 feed 使用 `SimpleWebSource(baseUrl, downloader)`；`GithubSource` 是 GitHub provider。将 GitHub 的 `browser_download_url` 任意改成前缀没有在该公开 API 中得到证明，需 `IUpdateSource`/`IFileDownloader` adapter（或发布同一 Velopack feed 到受控镜像）。
3. **Velopack Python**：1.2.0 已提供 GitHub/GitLab/Gitea/HTTP sources，但 Python reference 没有像 C# 那样清晰展示任意 custom downloader；必须以真实前缀代理做 PoC。若不能保持 feed/package checksum 语义，不得在 native binding 外再下载文件后强行塞入安装目录；改用受控静态镜像或 tufup。
4. **creativeprojects Go**：官方有 HTTP repository provider，适合把“发现 document + 资产 + checksum/signature”发布成稳定协议；若还要复用 GitHub Releases，则写 provider/adapter 并让 validator 固定验证签名/哈希，不能只字符串替换 URL。[creativeprojects documentation](https://pkg.go.dev/github.com/creativeprojects/go-selfupdate)
5. **tufup**：最合适的是镜像完整 TUF metadata/targets，客户端仍验证内置 root 和签名 metadata。不要将未签名的 GitHub release JSON 当 TUF metadata。
6. **MSIX**：`UpdateURI` fallback 是 `.appinstaller` 描述文件的候选 URI，不是 GitHub release asset rewrite；镜像必须以同一包 identity/publisher/version 且可验证签名提供内容。

## 按项目推荐与迁移路径

| 项目/范围 | 推荐路径 | 备选与退出条件 | 不建议的捷径 |
|---|---|---|---|
| VibeOCR Next | **第一 PoC**：Velopack C# 包住 WinUI + Bootstrapper + Backend closure，删除 Python `updater.exe` 路径 | 若安装身份/UAC/Bootstrapper 冲突不可接受，退 NetSparkle + 明确的签名 installer | 在现有 .NET source 后再叠一个 updater，保留双所有权 |
| VibeTable | Velopack C# 包住 WPF/WebView2 + BFF + sidecar；实现 custom source/downloader 保留当前代理 UI 与官方 GitHub API 身份 | 若 portable/未知根文件保留契约无法满足，退 NetSparkle + 新的签名 installer；否则仅替换发现下载层并暂留受测 replacer | 让 Go sidecar 或 Python BFF 自行更新；把 workspace/数据库放进可替换目录 |
| VibeOCR Classic | Velopack Python `onedir` PoC，迁移现有 ready/health/frozen updater smoke | Python proxy seam 或健康失败回退不达标时，选 tufup + custom install script/TUF repo | PyUpdater；继续扩大自研 Python replacer |
| File Toolbox | Velopack Python `onedir` PoC，保留现有代理设置迁移和便携包 smoke | Python proxy seam 不达标或必须保持 ZIP-only 时，选 tufup；只想替换网络层可评估 WinSparkle binding | PyUpdater；只换版本检查库而宣称已替换 updater |
| Backend / Protocol | 不引入应用 updater；继续 manifest、wheel/NuGet、component lock 和消费方 binding | 无 | 组件各自追 latest release |
| 未来独立 Go CLI | creativeprojects/go-selfupdate + ECDSA 或受控 checksum manifest + provider adapter | 更强信任要求时消费 TUF metadata | 用于 VibeTable PocketBase sidecar；无 validator 更新 |
| GitHub 发行渠道 | CI 产出不可变 tag/assets、SBOM、SHA-256、签名；Velopack full/delta/index 必须在 main CI 候选阶段生成 | 可镜像同一受验证 feed/targets | CD 重建框架包；把 tag/CDN URL 当信任根 |

## 迁移与 PoC 验收

1. **发布身份**：给一个 `vNext` 产物生成稳定 asset name、SHA-256、签名（或 TUF target metadata），并记录 source SHA/SBOM；镜像与 GitHub 原资产逐字节一致。
2. **下载**：在直连、A 类 forward proxy、B 类镜像三种环境测试。B 类必须通过原签名/哈希/TUF 验证；篡改一字节必须失败且不触及 current/install 目录。
3. **生命周期**：主程序、Python BFF、Go sidecar 均运行时触发更新；验证有序 quiesce、flush、等待 PID/句柄退出、更新、启动和健康检查。必须证明 Velopack 不会越过产品 shutdown gate 强制结束业务进程；杀毒或未知锁文件应得到可观察错误和可恢复的“下次重试”。
4. **增量/全量**：从至少两个连续版本升级，证明 delta/patch 成功；删除或损坏 delta 后验证框架记录失败并按已记录策略使用 full，而不是安装未验证文件。
5. **失败与回退**：下载中断、签名失败、磁盘空间不足、installer/sidecar 启动失败分别演练。明确“二进制替换 rollback”与“业务版本回退”的责任人；后者必须由 launcher + 保留版本/health gate 实现，不可凭框架名称假定。
6. **迁移**：四个现有自研 updater 都先发布一个能启动新框架安装/迁移入口的 bridge 版本；旧 ZIP/代理路径保留到支持窗口结束。只有外部仍存在 PyUpdater/Squirrel 客户端时，才额外保留其旧 feed。

## 淘汰条件

出现任一项即不进入生产：

* 不能把被下载 bytes 绑定到受控 SHA-256、嵌入式公钥签名或 TUF metadata；
* 更新器没有证据证明能处理 Windows 上运行中主程序/BFF/sidecar 的退出与重启；
* 需要“任意 URL rewrite”却没有官方 source/provider/downloader 接口，且 adapter 不能保持上述身份校验；
* 依赖已明确 legacy/archived 的 PyUpdater、Squirrel.Windows、Clowd.Squirrel，或 2020 年停更的 rhysd 模块作为新增核心路径；
* 将用户 workspace、PocketBase 数据库、日志等可变数据放入 Velopack `current`/可替换 app bundle，或让更新流程直接拥有 SQLite 写路径。

## 未验证项（必须在本仓 PoC 取证）

* Velopack Python 1.2.0 对 File Toolbox/VibeOCR Classic 的 Python 版本、PyInstaller hooks、用户可选 URL 前缀代理和 frozen artifact 的真实兼容性；官方已声明 Python/PyInstaller `onedir` 支持，但本调研未运行该 binding。[Velopack Python quick start](https://docs.velopack.io/getting-started/python)
* Velopack C# 对 VibeTable 的 WPF/WebView2 + Python BFF + PocketBase sidecar、以及 VibeOCR Next unpackaged WinUI + Bootstrapper 的进程退出、安装身份、签名与重启行为。
* Velopack Windows updater 的自动 process-kill 行为能否被现有优雅 shutdown/health 协议完全前置；不能证明时不得采用。
* 各 Windows 网络栈在企业 proxy PAC/NTLM/证书拦截下的真实行为；官方 source/downloader 接口存在不代表默认 transport 自动适配所有代理。
* GitHub Releases 直供的 rate limit、私库 token、pre-release 选择与镜像缓存一致性；这些是具体 provider/adapter 配置，不应由通用框架结论覆盖。
* MSIX 是否与现有 Win32 WebView2、离线安装、企业设备策略和发布身份完全兼容；需用真实签名证书及目标 Windows 10/11 x64 设备测试。
* 业务级自动回退策略：所有候选都需用 VibeTable 的 workspace UUID、PocketBase 数据迁移和 launcher health signal 设计专门验收，不能由下载器自动保证。

## 来源链接（均为一手来源）

* [PyUpdater—PyPI](https://pypi.org/project/PyUpdater/)；[官方文档](https://www.pyupdater.org/)
* [tufup—PyPI](https://pypi.org/project/tufup/)；[源仓库](https://github.com/dennisvang/tufup)；[python-tuf 官方文档](https://theupdateframework.readthedocs.io/)
* [sanbornm/go-selfupdate](https://github.com/sanbornm/go-selfupdate)；[rhysd/go-github-selfupdate](https://github.com/rhysd/go-github-selfupdate)；[creativeprojects/go-selfupdate—pkg.go.dev](https://pkg.go.dev/github.com/creativeprojects/go-selfupdate)；[minio/selfupdate](https://github.com/minio/selfupdate)
* [Squirrel.Windows](https://github.com/Squirrel/Squirrel.Windows)；[Clowd.Squirrel—NuGet](https://www.nuget.org/packages/Clowd.Squirrel/)；[Velopack 仓库](https://github.com/velopack/velopack)；[Velopack docs](https://docs.velopack.io/)
* [Microsoft MSIX signing](https://learn.microsoft.com/en-us/windows/msix/package/signing-package-overview)；[Microsoft App Installer auto-update](https://learn.microsoft.com/en-us/windows/msix/app-installer/auto-update-and-repair--overview)
* [NetSparkle](https://github.com/NetSparkleUpdater/NetSparkle)；[WinSparkle](https://github.com/vslavik/winsparkle)；[AutoUpdater.NET](https://github.com/ravibpatel/AutoUpdater.NET)
