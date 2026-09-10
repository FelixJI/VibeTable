# Surface metadata owner 资格记录

状态：**源码冻结，尚未取得完整迁移资格，不可据此宣称可合并**。本分支未执行完整发布构建或真实包 S17，未 push/创建 PR。

## 固定源码与完整意图

- 旧 Python producer：`12556e5db81dd49592d69b5af1780007ccd36c37`。
- 不可变捕获提交：`86639eb5af956956e637fc142e62e10160118f35`，78 例、81 次公开调用；原捕获器保留在该提交，JSON 未重新生成。
- 同步：正常合入 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`，同步提交 `19230d6455fd9711b46138c65bc9faa677aaa609`。
- 实施源码：`4eb7f00348618a31a78e2434768f6b24629d2732`。本资格文档的后续提交只有文档，不改 runtime。

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

## 剩余资格


- 独立 Standards/Spec 两轴审查 pending。未执行完整 Go 全仓/完整应用 suite、完整 Node 质量或全量 .NET suite，不拿聚焦结果替代它们。
- 完整 `build_next.py --release` 与同包 S17 pending；本分支没有 Surface 发布包。S17 已扩展原完整旅程：插件任务行为之后，精确重启 sidecar，以 fresh list/load 比较完整 aggregate/revision，重开运行时，再由真实删除按钮删除，fresh list 省略、public load not_found。取消/恢复窗口仍按既有 ownership 规则精确清理，不增加重试或放宽超时。
- [当前 E2E 证据](../e2e-performance.md) 继续保留旧 main 的 29 场景结果，并明确 `17-interface-lifecycle` 语义已变；旧结果不证明新增重启/删除断言。
