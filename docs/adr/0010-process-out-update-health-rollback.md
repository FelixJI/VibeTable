# ADR 0010：更新健康失败采用进程外旧包恢复

- 状态：已接受
- 日期：2026-08-27

## 背景

VibeTable 的便携包更新器只替换 `VibeTable.Next.exe`、`release.json` 与 `resources/`
三个发布包拥有入口。复制阶段失败时，更新 helper 已能从 staging 中的 `backup/` 恢复旧入口；
复制成功后，新版宿主通过持久化 activation journal 延迟清理 staging，并在完整 shell readiness
和只读工作区 `schema.list` 健康探测成功后确认更新。

当前健康探测失败只会令 activation 失败并保留 journal、staging 和 backup。它不会恢复旧入口。
运行中的新版宿主也不能在 Windows 上可靠覆盖自己的 executable，因此健康失败后的恢复必须由
独立进程接管，不能放进健康探测 module 或 WPF 页面生命周期。

## 决策

### 1. Activation settlement 是唯一外部 interface

调用方只提交闭集健康结果，不接触 target、staging、backup、token、进程身份或文件恢复阶段：

```csharp
internal interface IUpdateActivationSettlement
{
    Task CompleteHealthCheckAsync(
        UpdateActivationHealth health,
        CancellationToken cancellationToken);
}

internal abstract record UpdateActivationHealth
{
    internal sealed record Healthy(
        UpdateWorkspaceHealthProbeReceipt Receipt) : UpdateActivationHealth;

    internal sealed record Failed(
        UpdateActivationFailureCode Code) : UpdateActivationHealth;
}

internal enum UpdateActivationFailureCode
{
    WorkspaceHealthProbeFailed,
}
```

`CompleteHealthCheckAsync` 隐藏 journal 转移、watchdog/worker 协调与宿主退出约束。健康探测
module 只产生 `Healthy` 或闭集 `UpdateActivationFailureCode`；settlement 在内部把该小类型映射为稳定
持久化失败码，调用方不拼接或解释字符串。`MainWindow` 不解释恢复 outcome，也不复制更新事务逻辑。
settlement implementation 通过内部 host-lifetime adapter 请求失败新版退出。

失败结果一旦持久化为 rollback intent，就不能被 caller cancellation 撤销。重复提交相同结果必须
幂等；`Healthy` 与 `Failed` 冲突、已确认后要求回退或已回退后要求确认都必须 fail closed。

### 2. 单一 journal 拥有完整状态机

现有 activation journal 升级为 schema v2，并兼容读取 schema v1 的 `prepared` 与
`confirmed`。不增加第二份 recovery journal，避免两份记录之间出现新的原子一致性问题。

```text
prepared
  → launchingUpdatedApp
      → awaitingHealth
      ├─ Healthy → confirmed → cleanup → pointer removed
      └─ Failed | new-app-exit | timeout
          → rollbackRequested
              → rollbackWorkerLaunching
                  → rollbackRestoring
                      ├─ rolledBack → restoredLaunchPending receipt
                      │   ├─ old launch succeeds → rollbackComplete receipt
                      │   └─ old launch fails → restoredLaunchFailed receipt
                      └─ rollbackFailed
```

- `prepared`：update helper 已发布计划，staging 必须完整保留；target 可能尚未替换，或复制失败后已
  恢复为 `currentVersion`，不得假定 backup 已经生成。
- `launchingUpdatedApp`：新入口已经安装；journal 已持久化 watchdog identity 与一次性 launch nonce，
  新版尚未认领该 nonce，不允许进入普通 bootstrap 或提交健康结果。
- `awaitingHealth`：新入口已经安装；journal 已持久化新版 PID/启动时间和留驻 watchdog
  PID/启动时间，等待健康结论。
- `confirmed`：新包健康；只允许完成既有 staging cleanup，不得再转入 rollback。
- `rollbackRequested`：失败码与恢复 attempt 已持久化；target 仍保持新版。
- `rollbackWorkerLaunching`：watchdog 已持久化一次性 launch nonce；worker 必须用自身 PID/启动时间
  认领同一 nonce 后才能修改 target。
- `rollbackRestoring`：独立 worker 已认领 attempt，并按 owned entry ledger 恢复。
- `rolledBack`：target 已验证为 `currentVersion`，三个 owned entries 已恢复；pending pointer 随后必须
  原子改名为不阻塞后续更新的 terminal rollback receipt，再由 watchdog 启动旧包。
- `rollbackFailed`：恢复身份、磁盘形状、进程或文件操作无法证明安全；停止所有自动写入并保留
  pointer、staging、backup 和稳定错误证据。

未知 state/字段、非法倒退、token 或版本不一致、路径漂移、重解析点、同一 attempt 的不同 worker，以及不能由 ledger 唯一解释的磁盘形状都进入 fail-closed 结果，不猜测恢复。

### 3. 原 update helper 留驻为 watchdog

应用新包并启动 target 新版后，原 update helper 不立即退出，而是作为 bounded watchdog 留驻。它从
同一 journal 观察健康 settlement，并精确拥有本次更新启动的 target 新版进程。这样即使新版在
提交健康结果前硬崩溃，或在规定预算内一直没有完成健康确认，watchdog 仍可持久化失败原因并启动
进程外恢复。

启动新版前，watchdog 先在 journal lock 下写入 `launchingUpdatedApp`、自身 PID/启动时间和一次性
launch nonce，再把同一 nonce 作为闭集内部参数传给 target 新版。新版在 `App.OnStartup` 的最早期
以自身 PID/启动时间认领 nonce，并原子写入 `awaitingHealth`；认领完成前不得创建 MainWindow、启动
BFF/sidecar、加载 workspace 或提交健康结果。watchdog 只接受该精确 identity。新版在认领前退出时，
watchdog 使用本次 `StartUpdatedPackage` 返回的 PID/启动时间记录稳定 crash 失败码并进入回退。

- `Healthy` 必须先写入 `confirmed`；watchdog 看到该状态后退出，新版等待其精确退出并完成既有
  stage cleanup。
- `Failed` 必须先写入 `rollbackRequested`；settlement 确认 journal 中的 watchdog PID/启动时间
  仍属于本次 attempt 后，通过内部 host-lifetime adapter 请求新版退出，再由 watchdog 等待整个
  owned process group 清空；预算内未清空则终止该组。
- 新版在 `awaitingHealth` 时提前退出，由 watchdog 写入稳定的 crash 失败码，并终止 job 中仍存活的
  BFF/sidecar 后代。
- 健康预算耗尽时，watchdog 终止本次 owned process group，再进入回退；不按 executable 名称、运行后
  枚举到的 PID 或父子关系猜测其他进程。

三个失败入口都必须在 journal lock 下持久化 `ownedGroupQuiesced` 证据，证明本次 job 已为空且不会再
派生后代，才能转入 `rollbackWorkerLaunching`。等待或终止不能证明完成时进入 `rollbackFailed`，不派生
worker，也不修改 target。

watchdog 的等待有固定总预算，不轮询用户数据、不自动重试健康探测，也不接触 workspace。

### 4. 回退 worker 必须来自 staged 新包

watchdog 从已验证的 staged 新包复制一个受限 worker root，再从该 root 启动
`VibeTable.Next.exe --rollback-update`。worker 不能从 target 或 backup 启动：

- target 的新版 executable 正被失败宿主占用；
- backup 中的 N-1 旧版不一定认识新的 rollback 参数和 journal schema；
- staged 新包与写入 journal 的实现版本一致，可以验证并恢复同一事务；独立 worker root 允许
  staging package 和 backup 保持为恢复证据。

worker 启动前，watchdog 必须在 journal lock 下重新验证 pointer、token、target/staging parent、
current/target version、watchdog 身份、失败新版 owned process group、`ownedGroupQuiesced` 证据，以及
所有相关路径的 reparse-point 边界。它先写入 `rollbackWorkerLaunching` 与一次性 nonce，再派生
worker。worker 在同一 lock 下以自身 PID/启动时间认领 nonce 并写入 `rollbackRestoring`；未完成认领
或不能证明失败组仍为空时执行零 target mutation。

watchdog 监视精确 worker 身份。worker 在 terminal 状态前异常退出时，只有 journal ledger 与磁盘
形状能够唯一证明可继续，watchdog 才能用新的 nonce 派生一次 replacement worker；这是一次有
持久恢复点的崩溃续办，不重试失败的 I/O。replacement worker 再次异常或任何歧义都进入
`rollbackFailed`。watchdog 自身异常不使仍在运行的 worker 失效；watchdog 与 worker 同时退出属于
无法自动恢复的双故障，保留现场并等待显式修复。

### 5. 恢复只处理固定 owned entries

恢复的固定闭集仍为：

- `VibeTable.Next.exe`
- `release.json`
- `resources/`

worker 不枚举、不移动、不删除安装根中的其他条目，也不访问 `%LOCALAPPDATA%`、workspace、偏好或
业务数据库。每个 owned entry 都使用写前 ledger：先持久化即将隔离新版入口，再执行
`target → failed-package`；先持久化即将恢复旧入口，再执行 `backup → target`。进程崩溃后只在
ledger 与精确磁盘位置能够互相证明时继续；任何歧义都转为 `rollbackFailed`。

恢复 executable 必须是最后一个 owned-entry ledger 动作。全部恢复后，worker 重新读取
`release.json`，证明 target 为 `currentVersion` 且三个 owned entries 均存在，再把已经是
`rolledBack` 的 pending pointer 原子改名为同目录、同 attempt identity 的 terminal rollback
receipt，并退出。watchdog 只有在 worker 精确退出、receipt 与 target 身份再次通过验证后，才从
target 启动旧版；因此旧版从未看到会阻断后续更新的 pending pointer，也不需要理解 schema v2
或回传新健康协议。

若 worker 在恢复旧 executable 后、finalize receipt 前退出，留驻 watchdog 会按 ledger 和精确磁盘
形状重新派生 worker完成原子改名；不会把该窗口交给 N-1 旧版。staging 与 terminal receipt 可以
保留为诊断现场，后续只能由受限清理流程处理，不能为了无残留而删除正在使用的 worker root。

terminal receipt 在原子改名时先记录 `restoredLaunchPending`。watchdog 成功启动旧版后把它更新为
`rollbackComplete`；启动失败则更新为 `restoredLaunchFailed` 并保留稳定错误码。后两种状态都表示
target 已安全恢复，不重新创建 pending pointer、不再次写 package 文件；用户可在启动失败后手动
运行 target 中的旧版。

### 6. 只在真实变化点建立内部 seam

Windows 进程生命周期同时存在 production 与 test adapter，因此建立内部 process port：

```csharp
internal interface IUpdateRecoveryProcessPort
{
    UpdateProcessIdentity Current();
    UpdateOwnedProcessGroup StartUpdatedPackage(UpdateUpdatedPackageLaunch launch);
    UpdateOwnedProcessGroup StartRollbackWorker(UpdateRollbackLaunch launch);
    Task<ExactProcessExit> WaitForExactExitAsync(
        UpdateProcessIdentity process,
        TimeSpan timeout,
        CancellationToken cancellationToken);
    Task<OwnedProcessGroupExit> WaitForOwnedProcessGroupExitAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken);
    Task<ExactProcessTermination> TerminateOwnedProcessGroupAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken);
    void StartRestoredPackage(UpdateRestoredPackageLaunch launch);
}

internal interface IUpdateHostLifetimePort
{
    void RequestExit(int exitCode);
}
```

生产 adapter 使用 Windows `Process` 与精确 argv/working directory。它通过 Windows Job Object 创建
owned process group：根进程先以 suspended 状态创建并加入禁止 breakaway 的 job，确认 root
PID/启动时间与 job identity 后才 resume；新版后续创建的 BFF/sidecar 必须继承同一 job。超时关闭按
job identity 终止该组，不在运行后枚举进程树或按 PID 猜测后代。测试 adapter 控制启动失败、延迟
退出、PID 复用、job 认领失败和组终止；WPF lifetime adapter 负责调度 `Application.Shutdown`。文件系统属于
local-substitutable dependency，测试直接使用隔离临时目录运行真实 move/replace/reparse 检查；
不引入通用 `IFileSystem` 浅 seam。时间通过 `TimeProvider` 作为内部 seam。

update recovery job 使用专属 adapter，明确不设置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，不能复用现有
backend `JobObject` 的 last-handle-close 杀组语义。watchdog 从 suspended create 起唯一拥有 job handle：
失败路径显式终止并等待组为空；`confirmed` 后只释放 handle，让健康新版继续；worker job 同样由
watchdog 显式终止或在 worker 精确退出后释放。watchdog 异常关闭 handle 不得杀死健康新版或仍在恢复
的 worker；对应的 handle 生命周期、显式终止和关闭顺序都必须用 production adapter contract test 锁定。

worker root 固定为本次已验证 staging 下的 `rollback-worker/VibeTable`，只复制同一 targetVersion
包的三个 owned entries；不得使用 `%TEMP%`、用户数据目录或系统目录。watchdog 在 worker 精确退出后
可以清理该 worker root；它自己所在的 staging package、backup、failed-package 与 terminal receipt
由后续受限诊断清理拥有，不能在运行中的 watchdog 内递归删除。

## 错误与恢复语义

- 健康失败、内部超时或 readiness 失败只提交稳定失败码；失败字段和日志不保存用户值、workspace
  内容、调用方提供的任意路径或异常正文。journal 只保留恢复所必需且已规范化验证的 target/staging
  路径。
- terminal receipt 生成前的 watchdog/worker 身份认领失败、精确进程等待失败或 owned entry 恢复失败
  都写入稳定 `rollbackFailed` 证据，不删除现场、不进行无依据文件重试。
- 早期启动 resolver 对所有 schema-v2 pending state 默认阻断 MainWindow、BFF、sidecar 与 workspace
  bootstrap。只允许携带一次性 nonce 的本次新版从 `launchingUpdatedApp` 完成认领，以及已认领的
  精确新版进程在 `awaitingHealth` 内继续。若 `confirmed` 后原进程在 cleanup 前退出，新进程只有在
  重新验证 targetVersion、attempt identity 与路径边界后才能进入受限 `ResumeConfirmedCleanup`；它先
  完成 cleanup 并移除 pointer，随后才进入普通 bootstrap，不重新执行健康探测或回退。任何其他手动
  第二实例、`prepared`、所有 rollback state、未知 state 或不一致 pointer 都 fail closed。生产模式
  显示稳定恢复错误，test mode 以非零状态退出；不得把 pending 或无效 pointer 当成“没有 pending
  update”继续普通启动。
- `rollbackFailed` 是 terminal。未来若增加人工 repair/retry，必须另立公开入口、重新验证完整身份与磁盘形状，不能把它预先暴露在本 ADR 的 interface 中。
- target 已完整恢复并原子生成 terminal receipt 后，旧包启动失败不再写 `rollbackFailed` pending；
  watchdog 只把 receipt 更新为 `restoredLaunchFailed`，停止自动进程操作，并保留安全的旧包 target。
- 回退不会回滚 workspace/schema/snapshot，也不会删除审计记录；安装包与业务数据是两个独立事务域。

## 验收

后续实现按两个小 PR 交付：先完成 journal v2、settlement module、process adapter 与确定性状态机
测试；再增加真实发布包的 health-failure smoke。最低行为证据包括：

1. 新包健康成功时，watchdog 精确退出后才清理 stage；确认路径保持当前行为。
   `confirmed` 后、cleanup 前强制原进程退出时，下一实例只续办 `ResumeConfirmedCleanup`，移除 pointer
   后才启动产品服务；watchdog 释放非 kill-on-close job handle 不终止健康新版。
2. 健康失败先 durable 写入 `rollbackRequested`，再由内部 lifetime adapter 请求新版退出；此时
   target、backup、workspace 与未知安装根文件均未变化。
3. 新版必须在最早期用 PID/启动时间认领 launch nonce 后才能 bootstrap；认领前退出或健康预算超时
   时，watchdog 自动进入回退，不需要新版执行失败 handler。对每个 pending/recovery state 手动启动
   第二实例都必须在创建 MainWindow、BFF、sidecar 或加载 workspace 前 fail closed。
4. worker 未用 PID/启动时间认领 worker launch nonce、失败新版仍存活，或 token、路径、版本、
   reparse-point 任一不匹配时执行零 target mutation。
5. 在每个 owned entry 的 ledger 写前/写后位置模拟 worker 崩溃；watchdog 只对可证明形状派生一次
   replacement worker，歧义或第二次异常进入 `rollbackFailed`。
6. 恢复 executable 最后执行；随后 pending pointer 原子改名为 `restoredLaunchPending` receipt；
   watchdog 再启动旧版并写 `rollbackComplete` 或 `restoredLaunchFailed`。任一步失败都不能把残留
   pending pointer 的收口责任交给 N-1 旧版。
7. 新版、BFF 与 sidecar 在根进程 resume 前已归属禁止 breakaway 的 Windows Job Object；分别模拟
   `Failed`、根进程 crash 和 timeout，并加入子 PID 复用/逃逸请求。每条路径都必须先证明本次 owned
   process group 已为空才派生 worker，且不遗留 BFF、sidecar 或占用端口。
8. 恢复失败保留 pointer、staging、backup、失败新版入口与稳定错误码；不遗留运行中的 worker。
9. 真实打包 smoke 从旧包应用新包，分别强制健康失败、健康超时和新版进程退出，证明 target
   恢复 `currentVersion`、未知安装根文件与外部用户数据不变，并且没有残留进程或端口。

## 后果

### 正向

- MainWindow 和健康探测不理解安装树、Windows 文件锁或 crash recovery。
- 一个 journal 统一成功清理与失败恢复，状态和诊断不会跨多个 owner 漂移。
- 留驻 watchdog 覆盖新版尚未来得及报告健康结果的崩溃与总预算超时。
- staged 新包提供与 journal 同代的 worker，不依赖 N-1 旧包预先实现新协议。
- 文件恢复范围保持闭集，不因自动回退扩大到 workspace 或未知用户文件。

### 成本

- 三个 owned entries 不是一个原子目录交换，需要 per-entry ledger 与崩溃恢复测试。
- watchdog 与 worker 是两个受同一 attempt 约束的进程；它们同时异常退出时保留现场，需要显式修复。
- 回退成功后 staging 可能暂时保留，优先保证旧包可启动和现场可诊断。
- terminal receipt 可能在旧包启动失败后保留 `restoredLaunchFailed`，但不会阻断后续更新；它是
  诊断证据，不是第二个事务 journal。
- schema v2 必须兼容已由 schema v1 updater 写入、但尚未确认的 pointer。

## 被否决方案

- 在 `UpdateActivationWorkspaceHealthGate` 内直接移动文件：混淆健康判断和安装恢复，且运行中的新版无法可靠替换自身。
- 继续使用同步 `IUpdateActivationGate.FailActivation()` 承载恢复：无法表达 durable 写入、worker 派生失败和退出顺序。
- 让 `MainWindow` 解释 settlement outcome 并决定退出：会把更新事务状态扩散到 composition root；
  host 退出由内部 lifetime adapter 拥有。
- 为 recovery 另建第二份 journal：增加跨 journal 原子一致性与恢复歧义。
- 从 backup 的旧版 executable 启动 worker：N-1 旧版不保证认识新 rollback 协议。
- 整体重命名安装根：会移动未知用户文件，违反发布包 owned-entry 契约。
- 自动重试 `rollbackFailed`：无法证明失败后的磁盘形状仍安全，可能把可恢复现场变成混合版本。
