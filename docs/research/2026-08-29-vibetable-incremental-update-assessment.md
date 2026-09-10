# VibeTable 增量更新必要性与 Velopack 可行性评估

评估日期：2026-08-29

代码基线：`GitHub/main` @ `9ec0ac8ed5b14b84d4cb40e9851faa670075dc43`

评估范围：Windows 10/11 x64 便携发布、自更新、CI/CD 候选与 Release 资产；不包含本轮代码实现。

## 结论

### 决策摘要

| 问题 | 结论 |
| --- | --- |
| 是否需要为高频发布准备增量更新 | **需要，优先级中高**。当前正式包约 126–134 MiB，历史上曾在约 11 天内发布 4 个正式版本；若恢复周更/日更，完整下载的带宽与失败窗口会持续放大。 |
| 是否已有证据要求立刻替换现有 updater | **没有**。仓库没有客户端带宽、下载失败率、安装耗时或用户跨版本数分布；其他仓库虽已用 Velopack，自 2026-08-29 可见的正式 Release 仍是 full-only。 |
| Velopack delta 是否可行 | **打包与发布侧可行性高**。Velopack 1.2.0 支持 .NET 10、Windows Portable、full/delta/feed、GitHub/静态源和 full fallback，File Toolbox 已在 `main` 落地生成/校验方案。 |
| Velopack 能否直接接到现有 ZIP updater | **不能视为可直接接入**。官方模型要求 `root/current + Update.exe + sq.version + stable stub`，并由 full nupkg/feed 驱动；这会改变安装布局、启动、发布资产和迁移路径。 |
| 是否应让 Velopack 替换现有健康回滚 | **不应**。官方没有“新版业务健康失败后自动回滚”的承诺；VibeTable 已有工作区只读健康门、持久 journal、进程组所有权和自动恢复，这是产品专属深实现。 |
| 现在最合适的动作 | **先做隔离的 build-only + Portable 真包 PoC，再决定生产迁移**。PoC 不改变正式 Release 和现有用户更新路径。 |

因此，本评估给出两个不同强度的结论：

1. **增量更新能力是高频发布路线上的必要基础设施。**
2. **Velopack 生产迁移仍是有条件决策；当前证据只足以批准 PoC，不足以批准整体替换。**

## 现状与必要性

### 当前更新链

VibeTable 已经拥有完整的自更新实现，不是从零建设：

- [`ReleaseUpdateService.cs`](../../desktop/src/VibeTable.Desktop/Services/ReleaseUpdateService.cs) 从 GitHub Releases 发现最高稳定版，只接受精确命名的完整 ZIP 与 `.sha256`，并核对 GitHub asset digest、checksum、长度和本地下载摘要；
- 同一模块安全解压完整 ZIP，拒绝 Zip Slip、符号链接、ADS、重复路径以及过大文件数/展开量；
- [`UpdateProcessCommand.cs`](../../desktop/src/VibeTable.Desktop/Services/UpdateProcessCommand.cs) 只替换 `VibeTable.Next.exe`、`release.json`、`resources/`，保留安装根中的未知文件；
- [`PendingUpdateActivationJournal.cs`](../../desktop/src/VibeTable.Desktop/Services/PendingUpdateActivationJournal.cs) 和 [`UpdateRecoveryWatchdog.cs`](../../desktop/src/VibeTable.Desktop/Services/UpdateRecoveryWatchdog.cs) 在成功健康确认前保留旧包，并可恢复；
- [`UpdateActivationWorkspaceHealthGate.cs`](../../desktop/src/VibeTable.Desktop/Services/UpdateActivationWorkspaceHealthGate.cs) 对最近工作区执行只读健康探测；
- [`build_next.py`](../../scripts/build_next.py) 的真包 smoke 已覆盖成功更新、工作区健康失败、新版受控退出和健康超时回退，以及未知安装根文件/外部用户数据不变。

当前缺失的是传输层优化：客户端每次都下载完整 ZIP，没有 delta、block map、range resume 或 patch chain。

### 包体与发布频率

- 本地 v0.2.0 正式 ZIP 为 126.30 MiB，另一候选为 133.66 MiB；展开目录约 244.66 MiB、177 个文件。
- GitHub 正式 Release 的完整包从 v0.2.2 起约为 126–134 MiB。
- v0.1.0、v0.2.2、v0.3.0、v0.5.0 四次正式发布集中在 2026-07-27 至 2026-08-07。
- 包内包含 WPF 宿主、WebView 资源、PyInstaller backend/Python runtime、PocketBase sidecar、Kopia、age 等大体积二进制；相邻构建是否能得到小 delta，取决于这些文件的可复现性与变更局部性，必须实测。

结论是“存在明显收益机会”，而不是“已由用户故障数据证明为 P0”。如果未来平均每用户每月跨过 4 个相邻版本，现有路径理论传输约 0.5 GiB；delta 能否把它降到可接受水平，是 PoC 应回答的问题。

## Velopack 的已确认能力与限制

外部事实的逐项官方证据见 [Velopack 官方证据](2026-08-29-velopack-official-evidence.md)。核心事实如下：

- `vpk pack` 从应用目录生成 full nupkg、可选 delta、Portable 和 `releases.{channel}.json`；无状态 CI 需要先取得上一已发布 full 包。[官方 delta 文档](https://docs.velopack.io/packaging/deltas)
- 客户端会按 delta chain 重建目标 full 包；delta 缺失、总大小不划算、默认超过 10 段、缺本地 base 或重建失败时自动改下完整 full。[官方集成概览](https://docs.velopack.io/integrating/overview)
- Portable 可以自更新，但必须使用 Velopack 规范布局；它不是继续使用任意 flat ZIP。[Windows 布局](https://docs.velopack.io/packaging/operating-systems/windows)
- 更新会整体替换 `current`；设置、日志、workspace 和其他持久数据必须位于 `current` 外。[持久文件规则](https://docs.velopack.io/integrating/preserved-files)
- SDK 与 `vpk` 应锁定同版本。本次 PoC 应固定稳定版 1.2.0，而不是预发布版。[NuGet Velopack](https://www.nuget.org/packages/Velopack)
- 官方能证明包 size/hash 校验与 Windows Authenticode 打包支持，但没有证明 feed 独立签名、发布者证书 pinning，或新版业务健康失败后的通用自动回滚。[签名文档](https://docs.velopack.io/packaging/signing)
- 现有自研 flat-ZIP updater 没有官方无缝迁移器；必须设计最后一个旧版本到 Velopack Portable/Setup 的桥接与失败恢复。

## 其他仓库能证明什么

### 已证明

- VibeOCR Next 和 VibeOCR Classic 已使用 Velopack 1.2.0、Portable-only、full nupkg 和 feed，证明多进程桌面产品可以采用其目录/发布模型。
- File Toolbox `main` 已加入 delta 构建：从上一正式 Release 下载 full nupkg，`vpk --delta 1 --noInst` 生成 full/delta/feed，再把精确资产写入 checksums、SBOM、build identity 和发布契约。
- 这些仓库已有 custom downloader/feed adapter、固定工具版本、候选资产验证与镜像发布模式，可减少 VibeTable 的脚手架设计成本。

### 尚未证明

- 截至本评估日，VibeOCR Next 0.4.0–0.4.2、Classic 0.10.6–0.10.10、File Toolbox 0.2.6–0.2.10 的公开 Release 都没有 delta 资产。
- File Toolbox 的 delta 提交 `0de19db` 位于 v0.2.10 之后；因此它是已进入 `main` 的实现先例，不是已发布并有客户端成效的生产证据。
- 其他仓库没有 VibeTable 当前这套工作区只读健康门、持久激活 journal 和自动业务恢复契约，不能用其成功启动代替本仓验收。

## 方案比较

| 方案 | 增量收益 | 迁移成本 | 保留现有健康回滚 | 可行性 | 建议 |
| --- | --- | --- | --- | --- | --- |
| A. 保持完整 ZIP | 无 | 低 | 完整 | 高 | 作为当前生产基线和回退路径保留 |
| B. 只用 `vpk` 生成 delta，继续由现有 flat-ZIP updater 消费 | 可能 | 中高 | 可保留 | **中低**：官方 delta/SDK围绕 nupkg、feed 和 Velopack install context，容易形成非标准低层集成 | 不直接生产化；仅用于 build-only 比例实验 |
| C. 完整迁移到 Velopack Portable，另保留产品健康 supervisor | 有 | 高 | 需要重新接合 | **中**：框架能力与 sibling 先例充分，但布局、启动、桥接和业务回滚必须重验 | PoC 通过后的首选生产方向 |
| D. 用 Velopack 完整替换现有 updater、journal、watchdog | 有 | 中高 | 丢失 | 低 | 拒绝 |
| E. 自研 delta 算法并保持现有布局 | 有 | 高且长期维护 | 完整 | 中 | 只有 Velopack PoC 因布局失败且收益很高时才重新评估 |

### 为什么不建议直接做“下载 adapter”生产实现

从 VibeTable 内部设计看，理想 seam 是把外部传输隐藏在小 interface 后：

```text
版本/工件发现 + 下载/重建 adapter
                │
                ▼
      已验证的完整 package tree
                │
                ▼
产品激活与恢复深模块
  ├─ owned-entry 替换
  ├─ pending journal
  ├─ workspace health gate
  └─ rollback watchdog
```

但 Velopack 的公开深模块把“delta 重建、packages 缓存、`current` 替换、稳定 stub、Update.exe”作为一个整体。把它只拆成 delta library 使用，会走到非标准实现；把第三方类型直接传播到 journal/watchdog，则会扩大 interface、降低 locality。

因此 PoC 应同时验证两件事：

1. `vpk` 对 VibeTable 真包是否产生足够小的 delta；
2. Velopack Portable 生命周期能否与产品健康 supervisor 建立干净 seam。

如果第 2 项无法做到，不应为了已经投入的打包工作强行迁移运行时。

## 推荐的分阶段路径

### PR 1：build-only 增量收益 PoC

目标是回答“值不值得迁移”，不改变客户端、正式资产或 Release。

- 固定 `Velopack`/`vpk 1.2.0`；
- 用 3 组具有代表性的连续候选生成 full/delta/feed：
  - 仅 Web/业务代码变化；
  - Python backend/依赖变化；
  - Go sidecar 或工具链变化；
- 记录 full/delta bytes、delta/full 比例、pack 时间、峰值临时空间和输出资产；
- 验证同输入可重现包契约，feed 精确引用 full/delta；
- 只写 `build/` 证据，不修改 `.ci/project.json` 的正式 `required_assets`。

建议 go/no-go 门槛（属于本项目决策阈值，不是 Velopack 官方要求）：

- 三组相邻候选的 delta/full 中位数不高于 40%；
- 最差一组不高于 60%，或能明确归因到低频的 runtime/toolchain 大升级；
- 新增打包时间不超过现有 release build 的 10% 或 5 分钟中的较小者；
- 无任意单文件超过 Velopack delta 的 2 GB 限制；
- 生成失败能 fail closed，不能静默把缺失 delta 当正式资产通过。

门槛不通过：停止运行时迁移，继续完整 ZIP 更新；未来包结构变化后再测。

### PR 2：隔离 channel 的 Portable 生命周期 PoC

仅在 PR 1 通过后开始，使用测试版本/channel，不发布到生产 stable：

- 让 `vpk pack --noInst` 生成规范 Portable；
- 在入口最早调用 Velopack hook，审计所有旧 flat-root 假设；
- 把 workspace、偏好、日志、插件与运行时状态证明性地留在 `current` 外；
- 验证直连、前缀镜像与 forward proxy；feed/包被篡改时 fail closed；
- 验证 N-1、跨 3 版、超过 10 段、缺 delta、损坏 delta时的 full fallback；
- 验证 WPF、WebView2、Python BFF、PocketBase sidecar、托盘和其他句柄的有序退出；
- 验证杀软/锁文件/空间不足时旧版仍可启动且诊断可读；
- 设计并演练“最后一个 flat-ZIP updater → Velopack Portable”的幂等桥接；
- 使现有成功更新、工作区健康失败、更新后进程退出、健康超时四类真包 smoke 保持等价；真实 crash 仍单列，不把受控退出当作 crash。

### PR 3：候选与发布链生产化

只在前两阶段通过且决定采用后执行：

- 上一正式 full nupkg 在 `main` CI candidate 阶段获取并验证产品/channel/version/Release 身份；
- full、delta、Portable、feed、identity、SBOM 与 checksum/manifest 绑定同一 source SHA；
- PR 验证使用固定 fixture 或隔离 feed，不依赖可变生产 latest；
- CD 继续只发布 CI 候选，不重建 delta；
- GitHub、Gitee、CNB 同步同一精确资产集合；
- retention 保持 full/delta/feed 一致，并保留足够链长度；
- 评审 Authenticode 与 feed 授权边界，不能把 feed 内自洽 hash 误写成独立签名信任链；
- 旧 updater bridge 支持窗口结束且仍存客户端可安全升级后，再删除旧 ZIP 路径。

## 生产采用的硬性验收门槛

出现任一项不满足，就保留现有 full-ZIP updater：

1. 真包 delta 收益未达到 PR 1 门槛。
2. 规范 `current` 布局会覆盖/丢失任何 workspace、PocketBase 数据、偏好、日志或插件状态。
3. 更新可能越过产品 shutdown gate 强制终止仍持有业务状态的 BFF/sidecar。
4. 不能在新版健康失败或超时时恢复旧包并重新启动。
5. 旧 flat-ZIP 到 Velopack 的桥接不能幂等、可重试和失败可恢复。
6. 缺失/损坏 delta 不能自动验证并回退 full。
7. delta/feed 只能在 CD 临时生成，无法纳入 immutable candidate、精确资产、attestation 与镜像契约。
8. 自定义 GitHub 前缀代理或镜像会绕过受控资产身份校验。

## 最终建议

将“支持增量更新”列入近期发布基础设施路线，并创建 **build-only PoC PR**；不要直接创建“迁移 Velopack 生产 updater”的大 PR。

如果 PoC 证明大多数相邻版本可节省至少约 60% 下载量，并且 Portable 生命周期能保留现有健康回滚契约，则推进完整 Velopack Portable 迁移。否则继续使用现有完整 ZIP updater；它已具备较强的安全解压、精确 owned-entry 替换和业务健康恢复，不应只为统一技术栈而替换。

## 尚未验证

- VibeTable 连续真包的 delta 比例、生成时间和磁盘峰值；
- Velopack 规范 Portable 对当前 flat-ZIP 用户的迁移体验；
- BFF/sidecar/WebView/托盘与杀软环境下的真实句柄退出；
- 前缀代理、Gitee/CNB feed 镜像与 Velopack checksum 行为；
- Authenticode 证书、时间戳和发布者授权策略；
- 新版真实 crash 的端到端恢复。
