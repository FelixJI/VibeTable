# Surface metadata owner 资格记录

状态：**旧源码完成过完整构建与同包 S17；后续审查发现真实 Runtime gate 重放缺陷，本次生产修复已取得聚焦 GREEN，待独立双轴及必要新包资格**。旧包不能覆盖本次 runtime 变更，当前 PR 的远端门禁须绑定最终 head；本次修复未 push。

## 固定源码与完整意图

- 旧 Python producer：`12556e5db81dd49592d69b5af1780007ccd36c37`。
- 不可变捕获提交：`86639eb5af956956e637fc142e62e10160118f35`，78 例、81 次公开调用；原始捕获来源保留；捕获器现随分支版本管理，检查不再依赖该 squash 前提交，JSON 未重新生成。
- 同步：正常合入 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`，同步提交 `19230d6455fd9711b46138c65bc9faa677aaa609`。
- 初始实施源码：`4eb7f00348618a31a78e2434768f6b24629d2732`。首次构建使用其后文档提交 `3bd09a4e`；后续 Runtime gate 生产修复见末节，不能沿用旧包结论。

这是一批完整 `interface.list/load/commit/delete` 迁移。Go `metadata.SurfaceService` 使用实际 logical_id，封装定义、树、DAG、动作、排序、存储投影与 CAS；Commit/Delete 沿用 metadata coordinator identity 及同事务 receipt/audit/outbox。成功重放先于 current CAS，不恢复已删除的 aggregate，不产生重复 audit。Generic interfaces HTTP upsert/delete 已收口；GET、内部 snapshot 读取、集合和其他 namespace 保留。

Host 真实 `ISurfaceRpcGateway` 由现有 `JsonRpcProductDataGateway` 实现，复用 Product binding 的 generation/lease/cancellation/disposal。Go dispatcher 与 Host HTTP parser 都仅为四个具名方法开放 -32170 和 38 个明确 code，data 只允许 kind/message/code、可选字符串 path。旧 Python SurfaceService、四 handler、错误注册与专属直连 Host gateway 同批删除；公开 DTO/catalog 保留。独立进程和注册预期完整核对 26 个方法及 workspace scope。

设计和完整删除范围见 [固定基线与设计](../../contracts/v2/surface-python-oracle.md)。没有 Surface 之外的 metadata、SchemaCore、业务 hash 或发布逻辑迁移。

## 有意差异与证据边界

1. 固定 corpus 中三个“首次成功、再次 current 拒绝”的旧行为被修复为 durable replay。新回归要求在更新、删除以及 PB 关闭重开后返回首次结果；旧 producer 的对应 error 输出会使此契约失败。
2. 不复制 Windows Pydantic 的偶然栈限制：本机旧 DTO 深 98 可解码、99 起 recursion_loop；Go 独立限制 1 MiB 请求、256 个嵌套容器边。公开领域仍为 200 元素/深 8，深 9 返回 surface.element_depth，病态树有界 invalid params。未宣称所有非法输入与旧实现逐字等价。
3. 固定 oracle 的 revision 是 ScriptedMetadata 返回的 token。Go 对照按业务 ID 将 seed token 对应到实际 PB revision；新 mutation receipt 的权威 revision 由真实 metadata 产生，不伪造固定 token。
4. 四个旧 adapter 人工 failure 用例核对错误投影；旧“返回重复 logical ID”fixture 则由真实 PB unique logical_id 拒绝，不伪造物理数据库重复行。真实并发 CAS、审计失败回滚、generic 入口、重启 receipt 有独立 PB/HTTP 证明。

## 已运行验证

所有 Python 入口使用锁定 shared uv 环境并将 PYTHONPATH 指向当前源码；Go 固定本机已有 1.27.0，Node 固定已有 24.19.0，.NET 10.0.401 符合 global.json 的 10.0.400/latestFeature。未更新依赖或 lock，未运行 npm ci。

| 命令/范围 | 实际终态 |
|---|---|
| `go test ./internal/metadata -run TestSurface -count=1`（从 sidecar） | 修正 fixture 后 PASS 10.199s |
| `go test ./internal/metadata ./internal/productrpc ./internal/app -run 'TestSurface\|TestWorkspaceV2WriteBoundary' -count=1` | 最终三包 PASS：10.130s / 0.694s / 1.183s；含固定 corpus、8/9/病态深度、key 顺序、真实 HTTP、并发 CAS、重启 replay、audit 失败回滚 |
| `go vet ./internal/metadata ./internal/productrpc ./internal/app` | EXIT 0 |
| `go test ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1` | PASS 1.753s；独立完整 26 方法与 scope |
| `go test ./internal/productrpc ./internal/contracts/productcapabilities -count=1` | dispatcher PASS 0.712s；capability fixture 修正后单包 PASS 0.210s |
| `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release -p:RestoreLockedMode=true --filter "FullyQualifiedName~HostProductRpcInvokerTests\|FullyQualifiedName~JsonRpcProductSurfaceGatewayTests\|FullyQualifiedName~SurfaceBridgeTests\|FullyQualifiedName~ProductSidecarHttpGatewayTests" --verbosity quiet` | 61 PASS / 0 FAIL / 0 skip |
| 补嵌套 required-null DTO 与 malformed error data 后，同项目 `--configuration Release --no-restore --filter "FullyQualifiedName~HostProductRpcInvokerTests" --verbosity quiet` | 修改子集 28 PASS / 0 FAIL / 0 skip，197ms；四个正规 controller 响应、未知 code/domain/method、空 path、取消及 retired generation |
| `uv run --frozen --no-sync pytest tests/backend/rpc/test_error_registry.py tests/contract/test_product_contracts.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_product_e2e_capability_index.py --no-cov -q` | 最后整组为 88 PASS / 1 FAIL；唯一剩余文档语法修正后，`test_repository_capability_index_and_evidence_are_current` 单项 PASS 0.27s；不将原整组 FAIL 改写为 EXIT 0 |
| `uv run --frozen --no-sync python contracts/v2/generate_product_rpc_catalog.py --check`、`product_rpc_capability_policy.py --check`、`generate_surface_python_oracle.py --check` | 全部 EXIT 0，冻结 78 例不变 |
| 固定 Node `--check tests/e2e/webview_product_scenarios.mjs` | EXIT 0；仅语法，不是真实 S17 |
| 相关 Python Ruff format/check；正常提交 hooks 的 Ruff/version-consistency/package-contract | 全部 PASS |

本地日志在 `build/qa/surface-metadata/`：`go-focused.log`、`go-vet.log`、`go-process.log`、`go-policy.log`（含原失败）、`go-policy-final.log`、`host-surface-final.log`、`python-contracts.log`（保留 88/1 FAIL）、`e2e-evidence-contract.log`。早期未重定向输出保留在本任务工具记录；没有以新日志覆盖原失败。

## 原始失败与处置

- 首轮新增 oracle：Unicode 列表的多个 seed 共用 fixture-revision-0，测试错误地全局替换 revision；审计测试也误用不存在的 vibetable_change_sets。改为按业务 ID 对应 seed revision，并查询真实 vibetable_audit_events。其余领域/DTO/CAS/replay 无断言失败。
- 首轮 HTTP rollback：最后一项 fixture 把 outbox 写成 vibetable_realtime_outbox；实际生产 saveMetadataOutbox 使用 vibetable_outbox。前三个集合已经为零，修正集合名后完整回滚测试 PASS。
- Capability fixture 首轮误把既有 capabilityId 写为 interface.lifecycle；从正式 policy 确认为 content.model，修正独立预期，未改生产 policy。
- Python 契约先后暴露仍引用旧 Surface handler 的枚举、替换通用错误注册测试时误填 Insights code，以及新增 S17 语义未正常生成索引/更新严格 manifest changed 声明。保留原 86/3、88/1 失败；修正公共参数模型接线与独立四方法预期、真实 Insights -32080、生成索引及文档，不删除公共方法或降低门禁。
- 辅助命令曾有错误工作目录下的 gofmt 路径、默认 GBK 下打印/读取 Unicode、inventory 分组未排序和 manifest changed 声明格式错误。这些是工具/编写阶段失败，修正后复验；不是发布构建失败或产品通过证明。

完整 Python 入口 `uv run --frozen --no-sync python scripts/automation_project.py python-quality` 由主任务执行，handle `73726` 已 **EXIT 0**：1841 PASS / 1 skip，86.55s，backend coverage 91.74%；Ruff、Pyright、mypy 均通过。日志 `build/surface-python-quality.log` 保留。内部 uv run 继承 `UV_NO_SYNC=1`，源码保持冻结；主任务按 lock 一致建立 `.venv`、`.tools/node`、`desktop/web-grid/node_modules` junction，未安装新依赖。此完整入口通过不覆盖或改写上述历史聚焦失败。

## 独立审查与同包资格

- Standards、Spec 对完整实施源码 `4eb7f00348618a31a78e2434768f6b24629d2732` 分别完成独立审查，均为 0 findings；范围包括全部 14 个注册 fixture。随后仅文档更新，无 runtime 修改。
- 实际构建 source：`3bd09a4e92e3134def488e0300167ef66c3030b2`，构建前工作树干净。`uv run --frozen --no-sync python scripts/build_next.py --release` 仅执行一次，handle `55382`，**EXIT 0**。完整执行 sidecar、recovery tools、Web、PyInstaller、Host publish、产物校验、manifest、self-update smoke 与固定 atomic publish，产物为 `dist/VibeTable.Next`。日志 `build/qa/surface-metadata/build-release.log`，命令/source/工具链/终态记录 `build/qa/surface-metadata/build-release-command.txt`。
- 已有工具链固定 Go 1.27.0、Node 24.19.0、.NET 10.0.401（global.json 10.0.400/latestFeature），共享 uv。预检发现脚本原会选公共根目录的 .NET 10.0.400，因此仅为本 worktree 新增 `.tools/dotnet` junction 指向已有 10.0.401；缺少的 Go junction 同样复用已有目录。没有安装、升级依赖或修改 lock。构建中 updated-crash 的 `0x80131623` 是 self-update 故障注入输出；完整入口最终 EXIT 0，不是构建失败。
- 同一包只运行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 17-interface-lifecycle --evidence-root build/qa/surface-metadata/product-e2e`，handle `95374`，**EXIT 0，1/1 PASS，0 FAIL，0 skip**。场景 8775ms、22 项断言全 PASS；未重建或手工替换组件。
- 报告：`build/qa/surface-metadata/product-e2e/20260910T020400Z/product-e2e-report.json`；原 runner 日志 `build/qa/surface-metadata/s17.log`、命令记录 `s17-command.txt`。同次场景目录保留原 result、trace、Host/runner 日志、精确 fault request/result 和 `17-interface-lifecycle.png`、`17-interface-restarted.png` 截图。
- Package audit 与四组件 freshness（desktop-host、web-grid、python-backend、pocketbase-sidecar）全部通过。S17 保留原构建器/运行时和插件拒绝、取消、批准旅程；精确 sidecar child kill 完成，重启后 fresh list 返回原 committed revision，fresh public load 与完整 pages/bindings/actions 定义相等。真实 UI 删除只确认目标 aggregate，fresh list 省略目标，真实 Host Product load 返回 `surface.not_found`。
- Bridge failures 0、pending 0。仅两项精确预期失败被 acknowledged：重启窗口 `BACKEND_UNAVAILABLE` 与删除后的目标 load；无宽泛忽略。Renderer 无 page error、意外 console 或外部 HTTP 请求。Host normal exit 0，membersAfterExit、descendantsAfterExit 和 final remainingPids 为空；portsReleased、ownerLeaseCleanup、finalCleanup 均通过。

## 保留的资格边界

- 上述源码与同包资格不改写前文原始 fixture/契约失败。没有重复完整 Python 入口、构建或 S17 求绿；本批完整构建和 S17 均首轮通过。
- 未执行完整 Go 全仓/完整应用 suite、完整 Node 质量或全量 .NET suite，不拿聚焦结果替代它们；远端 PR required 门禁尚未开始。
- [当前 E2E 汇总](../e2e-performance.md) 继续保留旧 main 的 29 场景结果，并明确 `17-interface-lifecycle` 语义已变。本记录补充该新语义的独立同包 S17 证据，不把旧 29 场景结果改写成当前分支全场景通过。
## 发布包资格后的 Host 独立清单补漏

对固定 `dc33e6f9798a395fd6a06818ec2790536973a49e` 运行以下五组入口，实际 RED 为 138 PASS / 1 FAIL / 0 skip；唯一失败 `GeneratedManifestProvidesClosedRouteLookupForCurrentOwners` 的独立 Go 清单缺少四个 Surface 方法。先前相关审查和聚焦 Host 子集没有发现这个测试预期遗漏，不将该 RED 改写成通过。

```powershell
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release -p:RestoreLockedMode=true --filter "FullyQualifiedName~Composition|FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~WorkspaceRequestDispatcherQuery" --logger "trx;LogFileName=surface-main-host-contracts-green.trx" --results-directory build/qa/surface-metadata/host-owner-green --verbosity quiet
```

修正只在 `ProductRpcCapabilityManifestTests` 独立排序清单增加 `interface.commit/delete/list/load:workspace`，保持精确集合断言，不从生产 manifest 生成预期。Surface 四方法不在 `ProductDataRpcRegistry.RequestTypes`：renderer 使用 `interface.*Requested` 经 Surface controller、ISurfaceRpcGateway 与 Host Product 生命周期调用，workspace wire 由 Host 绑定。因此 RouteSelector、WebMessageRouter 的通用 owner/Whitelist 循环本次没有同类 RED，不向其中伪造 Surface generic 请求或绕过 scope validator。

同一五组 GREEN 为 **139 PASS / 0 FAIL / 0 skip，18s，EXIT 0**。RED handle `68891`，日志 `build/qa/surface-metadata/host-owner-red.log`、TRX `host-owner-red/surface-main-host-contracts-red.trx`；GREEN handle `85395`，日志 `host-owner-green.log`、TRX `host-owner-green/surface-main-host-contracts-green.trx`（后三者同在 `build/qa/surface-metadata/`）。仅测试及本记录变化，production diff 为 0，未重建或重跑 S17；等待此补漏的独立尾审，不宣称五组等于全量 .NET suite。

## Oracle 校验去除 squash 前提交依赖

此前 checker 使用 `git show 86639eb5:...json`，该分支捕获提交在 squash 后不保证可达。此修正恢复原 capture 工具为受版本管理的 `contracts/v2/capture_surface_python_oracle.py`；原 cases/helper/ScriptedMetadata 的 AST 与原捕获工具逐项对照一致，冻结 JSON 无改动。

新 `--check` 只读取可从 main 到达的 producer `12556e5db81dd49592d69b5af1780007ccd36c37` 的 backend Git archive。只接受 backend 下普通文件/目录，拒绝路径越界、绝对路径、Windows 流路径、符号/硬链接与特殊成员；解出位置为固定 `build/contract-oracles/surface-python/` 下独立 run 目录，不删除或覆盖既有目录。隔离子进程忽略继承的 PYTHONPATH/user site，从旧 backend 注册四个方法并通过原 dispatcher/DTO/SurfaceService 重放全部 78 例、81 次请求；捕获结果与冻结 JSON 全值比较，保留数组顺序和数值/布尔类型差异，不依赖 Go 输出或新的业务 hash。

`uv run --frozen --no-sync python contracts/v2/generate_surface_python_oracle.py --check` 最终 EXIT 0，日志 `build/qa/surface-metadata/oracle-replay-final.log`。`uv run --frozen --no-sync pytest tests/contract/test_surface_python_oracle.py --no-cov -q` 为 **10 PASS，2.33s**，日志 `oracle-replay-contract-final.log`（同目录）：真实旧 producer 重放一致，复制样本的 response 修改、数值 0 改为 false 均拒绝；八种非法归档成员拒绝，固定 producer 不存在时 fail closed。每次正常重放的 captured.json/stdout/stderr 保留在 build 隔离目录。首次 Ruff 曾报恢复捕获器的导入排序错误，修正后相关 format/check 全通过；原始工具输出与此前 9 PASS 日志保留。

此批仅修改 oracle 工具、契约测试与说明；冻结 JSON 和 production diff 均为 0，没有同步 main、重建、重跑 S17 或 push。原源码包资格与本次校验工具资格分别保留；本次修改等待独立双轴审查。

## 真实 Runtime gate 的幂等重放修复

独立 Spec 在 oracle 修复源码 `a7bb52bb8ef0e36f17a25f8fc64627a153d162f3` 发现：生产 app 同时传普通/幂等 gate，而 Surface 写 handler 选中了为 background batch 设计的 CoordinateIdempotentBusinessWrite。该 gate 查到 workspace receipt 后直接成功返回，不执行领域 callback；因此 commit/delete 的局部 result 未赋值，且相同 key 的不同 payload/expectedRevision 绕过 metadata digest 校验。原 HTTP fixture 只用了 apply-through gate，此前零问题审查和包 S17 未覆盖这个生产配置。

新增实际 PocketBase、Workspace Runtime、audit ledger 和公开 Product HTTP 回归，注入两种真实 gate。生产未改时的 `go test ./internal/app -run '^TestSurfaceProductRealRuntimeGate' -count=1 -v` 明确 RED：commit/delete 重放返回零值，相同 key 改 payload/expectedRevision 仍成功，关闭重开 Runtime/PB 后两方法重放仍为零值。日志 `build/qa/surface-metadata/runtime-gate-red-contract.log`。

修复沿用现有 Mutation 约定：Surface registration 和 app composition 只选择普通 CoordinateBusinessWrite；metadata.executeIdempotent 在 request digest 验证、已存 receipt 解码之后执行必要内部 replay callback，调用现有 ReplayedBusinessWrite 对精确 kind/key 返回 ErrBusinessReplay。该 exact signal 使 metadata 跳过重复 workspace receipt 持久化，事务完成后交 Runtime 正常 abort prepared intent；handler 已恢复的原 result 保留。事务错误优先，只有 exact signal 原样通过 Surface 投影，joined/其他错误不冒充成功。无 callback 的旧 metadata 调用者行为不变；workspace admission、身份/epoch 验证、公共 coordinator、其他 namespace 写入和错误 allowlist 未改。

验证结果及原失败：

- 最终同命令 **EXIT 0，两项 PASS，1.950s**，日志 `runtime-gate-green-final.log`（下述日志均在 `build/qa/surface-metadata/`）。覆盖 commit/delete 原结果重放、不同 payload/expectedRevision 拒绝、aggregate/audit/outbox/idempotency 与 workspace receipt/mutationRevision 不变；重放后的新写入恰好推进一次；Runtime/PB 重开后仍重放且不复活已删 interface；关闭 gate 拒绝，错误 workspace/epoch 为真实 HTTP 400/InvalidRequest，取消不改变 authority。
- 三包 `go test ./internal/metadata ./internal/productrpc ./internal/app -run 'TestSurface|TestWorkspaceV2WriteBoundary' -count=1 -v`：metadata PASS 10.301s、productrpc PASS 0.738s；app 的原 Surface/WriteBoundary 和新重放主测试通过，但新 scope 拒绝 fixture 误用了只允许 HTTP 200 的成功 helper，整体 EXIT 1。改为独立核对真实 HTTP 400/InvalidRequest 后取得上述两项 GREEN，未放宽原成功 helper。原日志 `runtime-gate-focused-final.log` 保留，不改写为整组 PASS。
- `go test ./internal/metadata -count=1` EXIT 1，10.264s；仅 `TestSurfaceFrozenPythonOracle/blank-name` 的 TempDir 清理目录非空，无语义断言失败。日志 `runtime-gate-metadata-full.log` 保留，不添加 retry 或重复全包求绿。
- `go vet ./internal/metadata ./internal/productrpc ./internal/app` EXIT 0，日志 `runtime-gate-vet.log`；相关文件已 gofmt。
- 早期 fixture 曾用不存在的 auditledger.Store，随后曾把 coordination 目录传给要求数据库文件的读取器，分别保留 `runtime-gate-red.log`、`runtime-gate-red-semantic.log`。首修复语义全部通过但 TempDir snapshots 清理失败，日志 `runtime-gate-green.log` 保留。这些不替代真实 RED/GREEN。

本次有生产 runtime 修改；未运行发布构建、S17、完整 Python 或全 Go suite，未同步 main/push。旧 3bd 完整构建及同包 S17 仅证明旧源码，修复需独立双轴后再决定新包资格；历史包和失败证据均保留。


## Runtime 重放修复后的新包资格

当前生产 source `65be85ce3d5727dc95cf3723ab0fd4795fb2016e` 已正常合入 main `58032b97043c2bba80a8eb1f65ae2906797e3251`；修复及同步增量 Standards/Spec 均无新增确定问题。

- `uv run --frozen --no-sync python scripts/build_next.py --release` 完整入口退出 0，含 self-update smoke 与原子发布包目录；日志 `build/qa/surface-metadata/build-release-runtime-replay.log`。
- 使用同一新包执行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/surface-metadata/product-e2e-runtime-replay --scenario 17-interface-lifecycle`：1 passed、0 failed、0 skipped，22 项断言通过，8.852s。
- 报告 `build/qa/surface-metadata/product-e2e-runtime-replay/20260910T031830Z/product-e2e-report.json`：包审计及四组件 freshness 通过，未预期 bridge failure 与 pending 均为 0；正常退出码 0，进程及后代为空，端口与 owner lease/final cleanup 通过。

这组新包结果覆盖上文 Runtime 修复；历史失败记录仍保留，不把本地 TempDir 整组失败改记为通过。新的远端 head 仍需 fresh CI、严格同步、squash 及合并后 CI/CD。

## 合入 History Product owner 后的组合边界

从 `505cf87891458836293a066e4e991cf52e87e1db` 正常合入 main `a3ca78b9181a529d978f9fba46586fbb924ecada`，保留 Surface 四方法及 History 两个恢复方法：独立 Go owner 清单为 28、Python 为 74。18 处冲突保留双方能力、Scope/effect 和严格匹配断言；14 个既有 app fixture 的 Surface 四方法及 History read/preview/apply 均各注册一次。生成文件由 `uv run --frozen --no-sync python contracts/v2/product_rpc_capability_policy.py` 和 `uv run --frozen --no-sync python scripts/generate_product_e2e_capability_index.py --write` 重生，两者 `--check` 均退出 0。E2E 历史 29 场样本的 changed 明确为 S07、S17 两项。

- `uv run --frozen --no-sync pytest tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_workspace_rpc_capability_manifest.py tests/contract/test_product_e2e_capability_index.py -q -o addopts=`：87 PASS，2.17s，日志 `build/surface-main-contracts.log`。
- `go test ./internal/productrpc ./internal/contracts/productcapabilities -count=1`：两包 PASS（0.813s、0.234s），日志 `build/surface-main-go-policy.log`。
- 逐文件提取 14 个受影响 fixture 的 47 个现有顶层 Test 名称（保留于 `build/surface-main-fixtures-tests.txt`），以 `go test ./internal/app -count=1 -run '^(名称以 | 连接)$'` 精确执行：整体 EXIT 1、9.523s，仅 `TestQueryPageProductHTTPReadsPersistedAuthorityAndSignedSnapshot` 的 TempDir 清理目录非空；没有重复注册或业务断言失败。原日志 `build/surface-main-fixtures.log` 保留，不写为整组通过。
- `go test ./internal/app -count=1 -run '^(TestSurface|TestHistoryRestore)'` 首次 EXIT 1：Surface 自有 HTTP fixture 的独立无关方法清单遗漏 main 的两个 History 方法，导致五项 Surface 测试在严格注册检查处失败（`build/surface-main-owners.log`）。仅补入两个字面量后同命令 PASS、4.768s（`build/surface-main-owners-correction.log`），包括真实 Runtime 重放、CAS、事务回滚及 History 恢复；未放宽校验。
- `go test ./cmd/vibetable-pb -count=1 -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$'`：PASS，1.731s；真实进程验证独立 28 方法清单和重启，日志 `build/surface-main-process.log`。
- `go vet ./internal/app ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb`：EXIT 0，日志 `build/surface-main-vet.log`；冲突 Go 文件已 gofmt。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~HostProductRpcCompositionTests|FullyQualifiedName~WorkspaceRequestDispatcherQueryTests' --logger 'trx;LogFileName=surface-main-host.trx' --results-directory build/qa/surface-main-host`：135 PASS，0 FAIL、0 skip，18s；日志 `build/surface-main-host.log` 及对应 TRX。

本次没有发布构建、重跑 S07/S17、完整 suite 或远端 CI 资格。上节 `65be85ce` 的新包与 S17 PASS 仅覆盖该 source，不能替代本次 History/Surface 组合的 fresh CI。原失败和历史包证据均保留，组合提交交独立双轴审查。
### CI race 超时后的夹具修正（2026-09-10）

head 7757d3d6 的 CI 34434998739 在 race-a 的 `TestSurfaceFrozenPythonOracle` 达到原 5 分钟预算而失败；其余 shards 与 CodeQL 成功，required 仍失败。原夹具对每个样本重复完整 migration，且只 ResetBootstrapState，没有触发正常 OnTerminate。

本次仅调整测试夹具：空库完整迁移一次并正常终止、关闭连接后读取两个数据库文件；每个样本创建独立目录、文件和 PocketBase 生命周期。78 个冻结样本不变，11 个无 seed 的单 DTO 拒绝样本使用原调用首步 decoder。新增正常终止顺序和持久化隔离回归，不改变 5 分钟预算或生产实现。

- 生命周期回归原实现 RED（未触发 OnTerminate）。首次普通组整体 FAIL，唯一 TempDir 目录非空清理错误，不能计为通过。
- 首次 race 命令未启用 CGO，EXIT2、未执行测试；随后复用本机 Go 1.27/GCC，原预算运行 EXIT1/20.437s，仅新隔离夹具的空对象被 payload_json 拒绝。改为非空有效对象。
- 修正夹具后 `go test -race ./internal/metadata -run '^TestSurface' -count=1 -timeout=5m` EXIT1/20.628s：oracle 8.43s，业务断言无失败，但三个 TempDir RemoveAll 目录非空错误；整体仍 FAIL。日志 `build/surface-fixture-race-fixed.log` 保留，不将清理错误归因为杀软或用重试掩盖。
- `go vet ./internal/metadata` EXIT0。独立 Standards/Spec 审查均 0 项发现。

本地结果证明原超时路径已缩短，不能替代 fresh CI 成功；推送后仍须等待当前 head 完整 required，失败不得合并。
