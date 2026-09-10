# Velopack Windows/.NET 增量更新官方证据（2026-08-29）

## 研究边界与结论

本文只使用 Velopack 官方文档、官方 GitHub 仓库/Release、官方 API 参考和 NuGet.org 元数据；没有安装或运行 Velopack，也没有检查 VibeTable 产品代码。因此，下面的“可行”是框架能力判断，不是本项目已经通过集成验证的结论。

**结论：Velopack 的增量更新值得进入 PoC，但不是发布正确性的必需条件。** Velopack 始终以 full `.nupkg` 为可应用目标，delta 是可选的下载优化；delta 缺失、链过长、合计不划算、没有本地 base，或重建失败时会自动下载 full 包。因此，频繁发版时采用它的主要收益是降低多数相邻版本更新的下载量，而不是消除 full 包、改变发布原子性或提供业务级自动回滚。[Delta Updates](https://docs.velopack.io/packaging/deltas) [Integrating Overview](https://docs.velopack.io/integrating/overview)

**工程上可行，但不能只把 delta 算法接到现有 ZIP 上。** 官方支持的 Windows 路径要求由 `vpk pack` 生成 full/delta、Setup、Portable、feed 和 Velopack 元数据，并采用 `{root}\current`、`Update.exe`、`sq.version` 和稳定入口 stub 的布局。`Portable.zip` 本身可自更新，也可把 Setup/MSI 安装到自定义目录，但更新时仍会整体替换 `current`。这意味着迁移范围至少涉及应用启动、打包、发布资产、feed、安装布局、持久化文件位置和更新 UI/错误处理。[Packaging Overview](https://docs.velopack.io/packaging/overview) [Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows) [Installers](https://docs.velopack.io/packaging/installer)

**建议以稳定版 `1.2.0` 做封闭 PoC，不直接采用当前预发布版。** 截止本研究日期，NuGet.org 将 `1.2.0`（2026-06-03）列为稳定版，另有 `1.2.110-ge826545`（2026-07-16）预发布版；官方组织页面显示主仓库在 2026-08-21 仍有更新，不能判定为停更项目。[NuGet: Velopack](https://www.nuget.org/packages/Velopack) [官方 Releases](https://github.com/velopack/velopack/releases) [官方组织页](https://github.com/velopack)

## 1. Delta 如何生成

### 1.1 默认构建行为

- `vpk pack` 的必需输入是 `packId`、SemVer2 `packVersion`、已编译应用目录 `packDir` 和 Windows 主程序 `mainExe`；Velopack 不支持四段版本号。输出包括 full `.nupkg`、可选 delta `.nupkg`、自更新 `Portable.zip`、`Setup.exe`、`releases.{channel}.json` 和供部署命令使用的 `assets.{channel}.json`。[Packaging Overview](https://docs.velopack.io/packaging/overview)
- 只要 `--outputDir` 中存在上一版 release，`vpk pack` 默认就会为新版本生成 delta。在无状态 CI 中，需先用 `vpk download` 拉取当前已发布版本，再 `pack`；官方推荐端到端链为 `download -> pack -> upload`。[Delta Updates](https://docs.velopack.io/packaging/deltas) [Deployment CLI](https://docs.velopack.io/distributing/deploy-cli)
- delta 对包内各文件使用 Zstandard 二进制 patch。单个文件不得超过 2 GB；这是单文件限制，不是整个包的大小限制。官方讨论进一步说明，当前只要出现一个超过 2 GB 的文件，整包 delta 就不可用。[Delta Updates](https://docs.velopack.io/packaging/deltas) [官方 docs 讨论](https://github.com/velopack/velopack.docs/discussions/26)
- `--delta` 支持 `None`、默认 `BestSpeed`、`BestSize`；`BestSize` 可能更小但显著更慢。也可用 `--delta none` 完全不生成 delta。[Delta Updates](https://docs.velopack.io/packaging/deltas)
- 可脱离常规 pack 流程手工执行 `vpk delta generate --base <old-full> --new <new-full> --output <delta>`，以及用一个或多个 `--patch` 执行 `vpk delta patch` 重建新 full 包。[Delta Updates](https://docs.velopack.io/packaging/deltas)

### 1.2 对发布流水线的硬约束

- 官方只支持应用内 `Velopack` SDK 与 `vpk` 使用相同版本；因此应把两者锁成同一个稳定版本，而不是在 CI 中浮动安装 latest。[From Squirrel](https://docs.velopack.io/migrating/squirrel)
- `vpk` 不接受由 `dotnet` 或 `nuget.exe` 预先制作的任意 `.nupkg` 作为发布输入；应先编译/`dotnet publish` 到目录，再将该目录交给 `vpk pack`。[From Squirrel](https://docs.velopack.io/migrating/squirrel)
- delta 生成依赖“上一已发布 full 包”进入本次 pack 输出目录。因此 CI 需要可信地下载正确 channel/架构的上一版，避免把不同产品、架构或 channel 当 base。官方要求跨平台/跨架构发布使用不会碰撞的独立 channel。[Release Channels](https://docs.velopack.io/packaging/channels)
- 官方 GitHub Actions 示例依次执行 `dotnet publish`、`vpk download github`、`vpk pack`、`vpk upload github --publish`；私有仓库的 download/upload 都需要 token，发布需要 `contents: write`。示例也提醒 SDK 和 `vpk` 版本保持一致。[GitHub Actions](https://docs.velopack.io/distributing/github-actions)

## 2. Delta 如何被客户端消费与何时回退 full

### 2.1 正常消费链

1. `CheckForUpdatesAsync` 从 update source 读取当前 channel 的 feed，返回目标 full release，以及可行时从本地 base 到目标版本的 delta 序列。[Integrating Overview](https://docs.velopack.io/integrating/overview)
2. 假设客户端从 `1.0.0` 直接更新到 `1.0.3`，会依次下载 `1.0.1`、`1.0.2`、`1.0.3` 三个 delta；它们顺序应用到本地保存的 base `.nupkg`，最终先重建出 `1.0.3-full.nupkg`，然后才进入应用阶段。[Delta Updates](https://docs.velopack.io/packaging/deltas)
3. `DownloadUpdatesAsync` 在 packages 目录取得每应用全局锁，将未完成下载写入 `.partial`，已有完整 full 包时跳过重复下载；delta 重建完成后得到待应用 full 包。[Integrating Overview](https://docs.velopack.io/integrating/overview)
4. `ApplyUpdatesAndRestart` / `ApplyUpdatesAndExit` 退出应用后调用 `Update.exe` 应用完整包；也可用 `WaitExitThenApplyUpdates` 最多等进程自行退出 60 秒，超时会终止进程。[Integrating Overview](https://docs.velopack.io/integrating/overview) [Update.exe CLI](https://docs.velopack.io/reference/cli/content/update-windows)

### 2.2 官方明确的 full 回退条件

| 条件 | 行为与证据 |
| --- | --- |
| delta 不存在 | 下载最新 full 包。[Integrating Overview](https://docs.velopack.io/integrating/overview) |
| delta 数量超过 `UpdateOptions.MaximumDeltasBeforeFallback` | 下载 full；默认阈值为 `10`，负数会禁用 delta。[Integrating Overview](https://docs.velopack.io/integrating/overview) |
| delta 合计大小大于 full 包 | 下载 full。[Integrating Overview](https://docs.velopack.io/integrating/overview) |
| 没有可用本地 base full 包 | 禁用 delta，下载 full。[Integrating Overview](https://docs.velopack.io/integrating/overview) |
| delta 下载后无法重建或校验失败 | 放弃 delta 路径并下载 full；官方 API 文档将其列为 `DownloadUpdatesAsync` 行为。[UpdateManager API](https://docs.velopack.io/reference/cs/Velopack/UpdateManager) |
| 降级或同版本跨 channel 移动 | `IsDowngrade=true`，强制下载目标 full，且删除本地比目标更新的包。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version) |
| 单文件超过 2 GB | 无法生成 delta，因此客户端只能使用 full。[Delta Updates](https://docs.velopack.io/packaging/deltas) |

这使 delta 成为失败可降级的优化，但也有两个容量含义：

- 频繁发版不等于所有旧客户端都能只下载一个小 patch；跳过多个版本的用户需要整条连续 delta 链，默认超过 10 个就直接 full。[Delta Updates](https://docs.velopack.io/packaging/deltas) [Integrating Overview](https://docs.velopack.io/integrating/overview)
- 远端不能只保留最新 delta。feed 必须与真实可下载资产一致；删除包时要同步删除 feed 条目。官方维护者建议 full/delta 成对保留和删除，只保留 delta 并非受支持/测试场景。[Distributing Overview](https://docs.velopack.io/distributing/overview) [官方 docs 讨论](https://github.com/velopack/velopack.docs/discussions/26)

## 3. Windows 包、安装和便携布局

### 3.1 规范布局

Windows 规范布局为：

```text
{root}/
├── current/
│   ├── <应用文件>
│   ├── sq.version
│   └── <mainExe>
├── Update.exe
└── <稳定入口 stub>.exe
```

Setup 默认把 `{root}` 放在 `%LocalAppData%\{packId}`；根目录入口 stub 指向 `current` 中真实 exe，使快捷方式、启动路径等在版本间稳定。更新会整体替换 `current`，而不是原地逐文件 patch 运行目录；delta 只优化下载和 full 包重建。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows) [From Squirrel](https://docs.velopack.io/migrating/squirrel)

`sq.version`、`Update.exe` 和这一目录关系是定位与更新契约，不应把现有 flat ZIP 直接标记成 Velopack install。应用二进制目录中的设置、日志、崩溃报告或用户数据会在更新时丢失；希望随卸载删除的数据放 `current` 的上一级，需跨卸载保留的数据应放 `%AppData%\{packId}`。运行时应通过 `VelopackLocator.Current` 获取 `RootAppDir`、`AppContentDir`、`PackagesDir`、`AppTempDir`、版本、channel 和 portable 状态。[Preserving Files & Settings](https://docs.velopack.io/integrating/preserved-files)

### 3.2 Portable ZIP

- `vpk pack` 生成的 `MyAppId-Portable.zip` 是官方支持的“无需安装但可自更新”交付物，用户解压后即可运行和更新。[Packaging Overview](https://docs.velopack.io/packaging/overview) [官方 docs 讨论](https://github.com/velopack/velopack.docs/discussions/8)
- Portable 并不意味着继续使用任意原有 ZIP 布局；官方产物仍带 `Update.exe`、`current`、入口 stub 和 portable 标记，应用更新时仍替换 `current`。这是从官方仓库维护说明与规范布局共同得到的结论。[官方仓库 CLAUDE.md](https://github.com/velopack/velopack/blob/develop/CLAUDE.md) [Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)
- 便携模式是否满足“完全自包含设置”应以 `IsPortable` 和实际路径 PoC 为准；无论如何，不能把需持久化数据写入会被替换的 `current`。[UpdateManager API](https://docs.velopack.io/reference/cs/Velopack/UpdateManager) [Preserving Files & Settings](https://docs.velopack.io/integrating/preserved-files)

### 3.3 自定义安装目录与 MSI

- `Setup.exe --installto <DIR>` 可在安装时覆盖默认 `%LocalAppData%\{packId}`。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)
- MSI 可按 `PerUser` / `PerMachine` / `Either` 设置默认范围；管理员可用安全属性 `VELOPACK_INSTALLDIR="D:\Apps\MyApp"` 覆盖目录。MSI 在所选目录中建立与 Setup 相同的规范布局，之后同样由 `Update.exe` 更新。[Installers](https://docs.velopack.io/packaging/installer)
- per-machine Program Files 安装需要提升权限；默认 Setup 是 per-user、无需提升。对计划保留“用户任选目录”的产品，Setup 自定义目录和 MSI 都可行，但必须验证目标目录权限、被占用文件、杀软和多进程退出行为。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)
- 若 `current` 被进程、打开文件、工作目录或杀软锁定，更新器会尝试终止位于其中的进程；仍失败时会询问用户终止锁定进程或中止。无法识别锁定者时显示错误并重新启动旧版本。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)

## 4. Feed、GitHub Releases 与 channel

### 4.1 静态 feed

- 简单 HTTP/文件源通过 `releases.{channel}.json` 发现 full/delta；feed 和其引用的 `.nupkg` 必须可下载且一致。feed 是 `UpdateManager` 发现发布的唯一索引，手工更新错误会导致用户收不到更新。[Distributing Overview](https://docs.velopack.io/distributing/overview)
- 内建源包含 `SimpleWebSource`、`SimpleFileSource`、`GithubSource`、GitLab、Gitea 和 Velopack Flow；私有或特殊认证可实现 `IUpdateSource`。公共 GitHub 未认证请求受每 IP 每小时 60 次限制，私有仓库需要 token。[Update Sources](https://docs.velopack.io/integrating/update-sources)
- channel 是发布基本单位；默认 Windows channel 为 `win`。多架构或 stable/beta 必须制定互不碰撞的 channel，例如 `win-x64-stable`。[Release Channels](https://docs.velopack.io/packaging/channels)

### 4.2 GitHub Releases 特性

- `GithubSource` 直接读取仓库 Releases；Velopack 会跨多个历史 GitHub Release 查找 delta。为保证这种策略可用，每个 GitHub Release 只能包含该发布的一个 full 和一个 delta 更新包。[Delta Updates](https://docs.velopack.io/packaging/deltas)
- `vpk upload github` 默认创建 draft；只有传 `--publish` 或之后在 UI 发布，客户端才可见。`--pre`、`--releaseName`、`--targetCommitish`、`--merge` 可控制 prerelease、名称、目标 commit 和更新同名 release。[Self hosting](https://docs.velopack.io/distributing/self-hosting)
- 因此不应在已有正式 Release 资产目录上手工拼接 Velopack feed/包；应让 `vpk download` 恢复上一发布上下文、`pack` 生成一致资产、`upload` 创建/更新 Release。官方部署命令负责更新远端配置，并可在支持的后端应用 retention。[Deployment CLI](https://docs.velopack.io/distributing/deploy-cli) [GitHub Actions](https://docs.velopack.io/distributing/github-actions)

## 5. 签名、校验与供应链边界

### 5.1 官方明确提供的能力

- feed 的 `VelopackAsset` 包含文件大小、SHA-1 和可用时的 SHA-256；生成资产时会计算 SHA-1/SHA-256。下载端 `VerifyPackageChecksumAsync` 依据 release entry 校验包，不匹配会抛 `ChecksumFailedException`。[VelopackAsset API](https://docs.velopack.io/reference/cs/Velopack/VelopackAsset) [UpdateManager API](https://docs.velopack.io/reference/cs/Velopack/UpdateManager) [Integrating Overview](https://docs.velopack.io/integrating/overview)
- Windows Authenticode 签名必须由 Velopack 在 pack 流程中协调，因为应用二进制、Velopack 的 `Update.exe` 和 `Setup.exe` 需要在不同构建阶段签名。官方支持 `signtool.exe` 参数、Azure Artifact Signing、`--signTemplate` 自定义命令和跨平台 JSign。[Code Signing](https://docs.velopack.io/packaging/signing)
- 官方强烈建议正式分发前签名；未签名程序可能被 SmartScreen/杀软拦截。签名参数示例使用 SHA-256 文件摘要和时间戳。[Code Signing](https://docs.velopack.io/packaging/signing)

### 5.2 不能从官方材料推出的保证

- SHA-256 证明下载字节与 feed 中摘要一致，但官方文档未说明 `releases.{channel}.json` 自身有独立数字签名，也未说明客户端强制验证所有包内 PE 的预期发布者证书。官方曾以 issue 增加 SHA-256 作为 tamper-proof 讨论的一部分，但这不等同于现有 feed 已有端到端签名信任链。[官方 issue #105](https://github.com/velopack/velopack/issues/105)
- 因此，采用时仍应使用 HTTPS 或 GitHub 受控源、最小权限发布凭据、既有 Release/source-SHA/资产清单/attestation 契约，并把“feed 或托管账户被篡改时客户端能否拒绝恶意但摘要自洽的包”列为单独威胁模型评审项。此条是基于官方已记录校验边界的工程建议，不是 Velopack 官方安全承诺。
- 官方的代码签名文档主要面向可执行文件身份和 SmartScreen；它没有证明 Windows 更新客户端会把 Authenticode 发布者 pin 成更新授权根。PoC 应明确测试签名缺失、证书变更、feed hash 错误、包损坏和 GitHub token 最小权限，而不能只看正常更新成功。

## 6. 前进更新、自动应用、降级与回滚

### 6.1 默认自动行为

- 默认 `CheckForUpdatesAsync` 只返回比当前版本新的远端版本。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version)
- 下载后可立即显式 apply；若不调用 Apply，`VelopackApp.Build().Run()` 默认会在下次启动检测本地较新 full 包并应用、重启。官方仍推荐显式 apply，因为多实例可能导致失败或其他进程被终止。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- 启动清理只保留当前已安装版本和最新本地版本的包；这不是长期本地回滚仓库。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- `DownloadUpdatesAsync` 支持取消下载，但官方 issue 列表中“允许取消更新”仍作为增强项存在，说明不能假设进入 apply 后仍可由应用 UI 安全取消。[UpdateManager API](https://docs.velopack.io/reference/cs/Velopack/UpdateManager) [官方 issue #904](https://github.com/velopack/velopack/issues/904)

### 6.2 降级/指定版本

- `AllowVersionDowngrade=true` 允许 `CheckForUpdatesAsync` 选择当前 channel 的最新但更低版本；指定任意历史版本则需从 feed 找 full asset、手工构造 `UpdateInfo` 并正确设置 `isDowngrade`。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version)
- 降级或横向 channel 移动禁用 delta，只下载 full，并删除比目标版本新的本地包，避免下次启动又自动升级。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version)
- 自动 startup apply 只处理 `target > current`，不会自动执行降级或同版本 channel 切换；必须显式调用 Apply。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version)
- 旧版本必须仍在 feed 中；若 retention 已删除，就要重新发布旧 full 或切换到仍提供该版本的 feed/channel。[Targeting a Specific Version](https://docs.velopack.io/integrating/specific-version)

### 6.3 “回滚”的准确含义

- 自托管模式可以通过把 feed 最新版本指回较低的已知良好版本并允许 downgrade 来实现发布回退；Velopack Flow 官方描述为通过管理 releases list 处理 rollback。[Self hosting](https://docs.velopack.io/distributing/self-hosting)
- Windows 在 apply 前/替换目录遇到无法解决的锁定时会重新启动旧版本，这是安装失败保护。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)
- **没有找到官方文档证明 Velopack 会在新版本成功安装但业务启动、数据库迁移或健康检查失败后自动回滚。** 因此“部署回退到旧 full”与“事务性业务状态回滚”必须区分；后者应由应用自己的兼容性策略和恢复流程解决。

## 7. 迁移既有自更新器的关键约束

### 7.1 应用启动与生命周期

- `.NET` 主程序必须在 `Main()` 最早位置恰好调用一次 `VelopackApp.Build().Run()`；Velopack 会用特殊参数再次启动主程序以执行安装、更新和卸载 hook，fast hook 结束后进程会退出。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- 每个包只有一个 VelopackApp 主二进制；如名称不等于 `{packId}.exe`，pack 时必须传 `--mainExe`。更新必须重启应用后才应用，不能沿用“进程内覆盖后继续运行”的自更新语义。[From Squirrel](https://docs.velopack.io/migrating/squirrel)
- Windows fast update hook 超时为 15 秒，安装/卸载为 30 秒；hook 不可显示 UI，也不能通过返回值取消操作。[App Hooks](https://docs.velopack.io/integrating/hooks)

### 7.2 文件与进程模型

- 所有应用二进制必须适合被整体替换；设置、日志、崩溃数据库、workspace 和用户数据不能留在 `current`。[Preserving Files & Settings](https://docs.velopack.io/integrating/preserved-files)
- 应用的所有子进程、sidecar、WebView/崩溃收集器和持有 `current` 文件句柄的外部进程都必须有可靠退出协议；否则更新器可能终止它们、弹窗或中止并重启旧版。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows)
- 运行路径会变成 `{root}\current\<mainExe>`，用户/快捷方式应走根目录稳定 stub。任何硬编码旧 flat 目录、外部快捷方式、文件关联、防火墙规则、GPU 偏好或插件路径都需做兼容性审计。[From Squirrel](https://docs.velopack.io/migrating/squirrel)

### 7.3 从旧更新器过渡

- 官方对 Squirrel/Clowd.Squirrel 有特殊原位迁移：同一 feed 同时发布 legacy `RELEASES` 和 `releases.win.json`，旧客户端先取得 Velopack 版本，Velopack updater 再迁移快捷方式和布局。[From Squirrel](https://docs.velopack.io/migrating/squirrel)
- 官方对 ClickOnce 的建议是发布最后一个旧格式更新，由它下载/运行 Velopack Setup，随后退出旧应用，并在 Velopack 首次安装 hook 中卸载旧 ClickOnce。[From ClickOnce](https://docs.velopack.io/migrating/clickonce)
- **没有找到针对任意自研 flat-ZIP updater 的官方无缝迁移器。** 可借鉴 ClickOnce 的桥接模式：最后一个旧更新版本启动已签名的 Velopack Setup（或切换到官方 Portable 布局）并退出，但安装目录复用、旧快捷方式/卸载项清理、用户数据保留、首次启动失败恢复都必须 PoC，不能从 Squirrel 的专用迁移支持外推。
- IDE/`bin\Debug` 运行不是 Velopack install，check/download 会抛 `NotInstalledException`；真实更新验证必须从 `vpk pack` 产出的 Setup/Portable 安装形态开始，或仅在单元测试中注入 locator。[Integrating Overview](https://docs.velopack.io/integrating/overview)

## 8. 版本、维护状态与许可

| 项目 | 截至 2026-08-29 的官方证据 |
| --- | --- |
| 当前稳定 SDK/CLI | NuGet.org 的 `Velopack` 和 `vpk` 稳定版均为 `1.2.0`；SDK 包直接列出 `net10.0` 兼容目标，同时提供 `net8.0`、`netstandard2.0`、`.NET Framework 4.7.2` 等目标。[NuGet: Velopack](https://www.nuget.org/packages/Velopack) [NuGet: vpk](https://www.nuget.org/packages/vpk) |
| 当前预发布 | `1.2.110-ge826545`，官方 GitHub Release 与 NuGet 均标为预发布/较新 prerelease，发布时间 2026-07-16。[官方 Releases](https://github.com/velopack/velopack/releases) [NuGet: Velopack](https://www.nuget.org/packages/Velopack) |
| 维护状态 | 官方组织页显示主仓库于 2026-08-21 更新；2026-07 的 prerelease 汇集功能、部署 CLI、Windows bootstrapper 和依赖修复，说明仍在活跃维护。[官方组织页](https://github.com/velopack) [官方 Releases](https://github.com/velopack/velopack/releases) |
| 许可 | 官方仓库和 NuGet 元数据均标明 MIT。[LICENSE](https://github.com/velopack/velopack/blob/develop/LICENSE) [NuGet: Velopack](https://www.nuget.org/packages/Velopack) |

维护活跃不等于所有场景无风险。官方 issue 在 2026 年仍记录企业 Windows/EDR 环境更新 apply 可能出现明显延迟、MSI 清理等开放问题；PoC 应覆盖真实 Windows 10/11、Defender/常见杀软、含多个子进程和数百文件的候选，而不只验证最小 Hello World。[官方 issue #947](https://github.com/velopack/velopack/issues/947) [官方 issues](https://github.com/velopack/velopack/issues)

## 9. 与“频繁发版”直接相关的采用判断

### 支持引入的事实

- 相邻版本改动较小时，按文件生成的 Zstandard patch 可显著小于整个 runtime/应用包；Velopack 自动生成、选择、下载、重建和 full fallback，产品侧不需自研差分算法。[Delta Updates](https://docs.velopack.io/packaging/deltas)
- 下载在应用运行期间进行，apply 使用已重建并校验的 full 包；显式重启后替换 `current`，有利于把网络时间与短暂不可用窗口分离。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- GitHub Releases、静态 HTTP、文件共享和自定义源都有正式路径；Windows Setup、MSI、自定义目录和 Portable 均可自更新。[Update Sources](https://docs.velopack.io/integrating/update-sources) [Installers](https://docs.velopack.io/packaging/installer) [Packaging Overview](https://docs.velopack.io/packaging/overview)

### 限制必要性的事实

- delta 不是 correctness gate；full 包路径完整存在并自动回退。若当前 full 包并不大、用户带宽成本低或发版频繁但单次改动会重写大量压缩/打包文件，引入 delta 的实际收益可能有限。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- 每次发布仍必须生成、签名、上传和保留 full 包；GitHub 每个 release 还需 full/delta 成对组织。delta 不会降低发布资产数或供应链验证责任。[Distributing Overview](https://docs.velopack.io/distributing/overview) [Delta Updates](https://docs.velopack.io/packaging/deltas)
- 对长期不启动的客户端，连续 delta 链可能超过默认 10 个或合计大于 full，最终仍下载 full。频繁发版更应测量“用户实际跨越版本数分布”，而不是只比较相邻两版大小。[Integrating Overview](https://docs.velopack.io/integrating/overview)
- 最大迁移成本是安装/目录/启动/发布模型切换，而不是 delta 本身。若产品必须保持原 flat ZIP、在现有目录原地覆盖、更新时不重启或让业务数据与二进制混放，官方 Velopack 模型不直接兼容。[Windows Overview](https://docs.velopack.io/packaging/operating-systems/windows) [Preserving Files & Settings](https://docs.velopack.io/integrating/preserved-files)

### 建议的最小 PoC 通过条件

以下是根据官方契约提出的验证建议，尚未执行：

1. 锁定 `Velopack`/`vpk 1.2.0`，对同一个 Windows x64 channel 连续打三个候选；验证 `download -> pack -> upload` 可重复生成 full、delta 和正确 feed。
2. 记录 full 与 delta 大小、pack 时间、从 N-1 更新、跨 3 版更新、超过 10 个 delta 的 full 回退、故意破坏 delta 后的 full 回退。
3. 分别从 Setup 默认目录、`--installto` 自定义目录和 Portable 解压目录执行安装、下载、显式 apply、重启；确认路径、快捷方式、卸载和用户数据保持。
4. 覆盖主应用、BFF/sidecar/WebView/托盘进程正常退出以及文件被占用；验证失败时旧版本仍可启动，日志能明确区分 delta 重建失败、checksum、锁竞争和 apply 失败。
5. 使用实际 GitHub draft/publish 流程，但在隔离测试仓库/channel 中验证每 Release 的 full/delta 资产、匿名限流、私有 token 和 retention；不要在生产 Release 上手工试验。
6. 验证 Authenticode 覆盖应用、`Update.exe`、`Setup.exe` 和需要执行的其他 PE；分别注入 feed hash 错误、包损坏、未签名/证书变化，记录客户端真实拒绝边界。
7. 做一个“最后一个旧 updater -> Velopack Setup/Portable”的桥接演练，包括中途退出、重复执行、旧卸载项/快捷方式、安装目录冲突和失败恢复。

只有当相邻更新的真实 delta 收益显著、full fallback 与旧 updater 桥接可靠、以及规范布局不破坏持久化数据/多进程模型时，才应把 Velopack 纳入生产发布链。否则，可先保留 full 更新并继续发布频率优化，因为官方模型本身也把 delta 定义为可选优化。

## 10. 证据缺口

- 没有运行 Windows PoC，未测本产品候选的 delta 比例、生成时长、应用时长、杀软误报、文件锁和磁盘空间。
- 官方材料没有给出针对任意自研 updater 的无缝迁移流程；Squirrel 自动迁移不能视为通用能力。
- 官方文档没有建立 feed 独立签名或发布者证书 pinning 的端到端信任模型；现有事实仅能证明资产 size/hash 校验和可执行文件签名能力。
- 没有找到“新版本已成功替换但业务健康检查失败后自动回滚”的官方承诺；只能确认显式 downgrade、feed 回退和部分 apply 失败时旧版本重启。
- 没有证明自定义目录、Portable、per-machine MSI 与本产品所有 workspace/插件/sidecar 路径语义兼容。
- `1.2.110-ge826545` 是活跃预发布而非稳定版；其部署 CLI/Windows bootstrapper 变化不应在未单独验证时混入 `1.2.0` PoC 结论。

