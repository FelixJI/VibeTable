# ContentProfile / RecordDocumentLink 迁移资格记录

状态：真实 Runtime gate 回归发现并修正 Content 写重放缺陷，当前生产源码已变化，待独立双轴尾审及主任务安排新包验证。下文旧源码的完整构建、S18、Python 结果仅归属各自固定 source，不覆盖本次修复；旧失败及远端门禁边界保留。

## 来源与完整范围

- 原公开行为 producer：`79c2ce4faa53d65af1fa9ee297655739d5407a2e`；冻结提交 `357d70e4` 捕获 28 例真实 Python 注册/编排/DTO 输入输出，见[原契约说明](../../contracts/v2/content-metadata-python-oracle.md)。metadata/schema 查询 adapter 的边界明确，不能把该 corpus 当作真实包证据。
- 完整迁移提交：`caecfeb5d76508679721e18816d51f3ff74f6f2e`。正常同步 main `12556e5db81dd49592d69b5af1780007ccd36c37` 后源码为 `ee27eb0584f6f3de888909b079a7407e3b25287b`；同步只涉及五份文档。
- 真实 S18 暴露的 Host 错误投影修复：`e8af1501f21303b80ccc6c6250ab4c72c3d63c63`，为当时修复后的 runtime 源码；后续主干同步的固定源码见末节。
- 后续提交 `dedbf2ae8461fee0b1cb60add3f34d4b02a507ce` 仅修改 `sidecar/cmd/vibetable-pb/main_process_test.go` 的独立精确预期；相对 e8af 的 runtime diff 为 0，不产生另一个包源码结论。

本批完整迁移七方法：ContentProfile 的 load/commit/delete，RecordDocumentLink 的 list/commit/repair/delete。Go 在现有 metadata 事务中完成领域校验、CAS、durable receipt 和 audit/outbox；record 查询使用业务主键，broken document link 保持允许。同请求 replay 先读取 receipt，避免成功后的 commit/repair/delete 被当前 revision 或不存在检查拒绝。

同批删除旧 Python ContentModelService、七个生产 handler、两 namespace 的通用写入口；保留 snapshot/search 所需内部读取。公开 Request/Result 继续来自生成的 workbench DTO。Surface、Dashboard、Preset、Version、History 未混入本批，未借用尚未合入的 History context seam。

## 两轮独立代码审查

第一轮最终 ee27 的 Standards 与 Spec 各 0 个遗留问题。审查过程发现并修复：Host 非 ASCII 测试字符意外改写、显式空 path 存在性、order 字符串被 ParseFloat 过度放宽、公共 catalog 残留 Python 注册来源。未知 content code、非七方法、其他 domain 保持原拒绝/映射边界。

第一次真实 S18 失败后，以 controller seam 复现：合法 content error 被降为 `operation.failed`。RED 为 2 FAIL / 2 PASS。e8af 将严格的七方法/十八错误码与形状检查收进共享 `ProductRpcErrorMapper.TryMapContent`，HTTP gateway 与 controller 共用；controller 返回正规方法响应，保留前端依赖的 error.code 与空 path。第二轮对 e8af 的独立 Standards 与 Spec 各 0 个遗留问题。此结论不替代真实包复验。

## 本地源码验证

所有 Python 命令通过 uv 使用锁一致的共享环境，PYTHONPATH 指向当前工作树。Node 使用仓库固定 24.19.0；Go 固定既有 1.27.0；.NET 10.0.401 符合 global.json 的 10.0.400/latestFeature。未更改 lock、下载来源配置或共享依赖，未 npm ci 重装共享 node_modules。

| 命令/范围 | 结果与限制 |
|---|---|
| `uv run --frozen --no-sync python scripts/automation_project.py python-quality` | ee27 最终入口 EXIT0：1823 PASS、1 skip、69.91s，coverage 91.61%；Ruff、Pyright、backend mypy 通过。e8af/dedbf 未改 Python，不重复其全套。 |
| `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --filter 'FullyQualifiedName~ProductDataSidecarRoutingTests\|FullyQualifiedName~ProductRpcErrorMapperTests\|FullyQualifiedName~ProductSidecarHttpGatewayTests\|FullyQualifiedName~HostProductRpcInvokerTests\|FullyQualifiedName~HostProductRpcCompositionTests' -p:RestoreLockedMode=true` | e8af：128 PASS、0 skip、14s。原 ee27 的较窄 Host 集合 79 PASS 属于较早源码证据。 |
| sidecar 下 `go test ./cmd/vibetable-pb -run TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart -count=1` | dedbf：PASS，1.719s。独立列出全部 27 方法，并逐项检查 workspace scope；不引用生产生成清单充当预期。 |
| sidecar 下 `go test ./internal/metadata ./internal/productrpc ./internal/app -run 'TestContent\|TestGenericContent' -count=1` | 三包 PASS；涵盖 corpus、同事务校验、durable replay、CAS/trace rollback、参数与错误边界、generic 写入口和真实 Product HTTP。 |
| sidecar 下 `go vet ./internal/metadata ./internal/productrpc ./internal/app ./internal/contracts/productcapabilities` | PASS。 |
| workbench DTO、product capability policy、product catalog 和冻结 content corpus 的生成一致性检查 | PASS；catalog 聚焦契约 12 PASS。 |
| 正常提交 hooks | Ruff 按实际文件适用性执行；version consistency、package contract 通过，无 bypass。 |

保留的失败记录：

- 完整 Python 首次因空实体 .venv 缺依赖，Pyright 报 49 个导入派生错误，pytest 未开始；实体环境完整保留后改为指向既有共享环境的 junction。随后完整入口 1821 PASS / 1 skip / 2 FAIL，仅公共 catalog 两项契约失败。修正生成器接线后才取得上述 1823 PASS；日志分别保留在 `build/content-python-quality.log`、`build/content-python-quality-resolved.log`、`build/content-python-quality-final.log`。单独 catalog 初测 12 项本身通过，但默认全后端覆盖率门槛使命令失败；聚焦 --no-cov 复验与最终完整覆盖率分开记账。
- 扩展 Go 测试先发现旧静态 20 方法 fixture 未收录七方法，修正对应独立预期后消除这些断言失败；本次真实 cmd 进程清单遗漏也已由 dedbf 修正。
- 完整相关 Go 包命令 `go test ./internal/metadata ./internal/productrpc ./internal/app ./internal/contracts/productcapabilities -count=1` 中 metadata/productrpc/productcapabilities 通过，但 app 仍有三项 `TempDir cleanup: directory not empty`：`TestHistoryReadProductHTTPReturnsFreshAuditedPage`、`TestWorkspaceMutationReplaySerializesConcurrentSameKey`、`TestSchemaGetTableProductHTTPRejectsInvalidFieldWireShape/negative-number`。无语义断言失败，根因尚未确定；未加 retry、未放宽检查、未重复全包求绿，不能记录为完整 Go 入口通过。

## 历史三次构建尝试与首次 S18

三次均使用完整命令 `uv run --frozen --no-sync python scripts/build_next.py --release`，无 skip stage，未手工替换组件。日志根目录为 `build/qa/content-metadata/`。

| 尝试 | 固定源码与结果 |
|---|---|
| 1：`build-release.log` | ee27，EXIT1。已生成 staging sidecar，随后 recovery tools 要求 Go 1.27.0，但根命令 PATH 是 1.26.5。先前 sidecar 模块测试自动使用 1.27，未证明根构建入口相同。不能称为无产物预检。 |
| 2：`build-release-pinned-go.log` | ee27，绑定本机既有 Go 1.27.0 后，经授权重新执行完整命令，早期阶段确实重新执行。session 6756 EXIT0，四组件、包验证、manifest、自更新 smoke 与 atomic publish 完成。 |
| 3：`build-release-final.log` | e8af，因真实 Host runtime 修复而必要重建。session 37025 EXIT1：`desktop self-update smoke health-timeout rollback did not complete`。四组件生成完成，但未 atomic publish，不能给新包通过结论。 |

命令/source 元数据为 `build-command.json` 与 `build-final-command.json`，每次退出码分别保存；旧失败日志未覆盖。

尝试 2 的 ee27 包运行：

```powershell
uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 18-workspace-search --evidence-root build/qa/content-metadata/product-e2e
```

session 86345 EXIT1，run `20260909T144121Z`，0/1 PASS、0 skip、15.331s。新表初次 contentProfile.load 的 not_found 被 Host controller 降为 PRODUCT_DATA_FAILED，配置面板随后等待 Title 下拉超时；未将其作为 E2E 超时问题掩盖。pageErrors 为空，正常 Host 退出、进程成员/后代、端口、owner lease 与最终清理均通过，但 bridge 有实际失败，不能据清理成功声明场景成功。报告为 `build/qa/content-metadata/product-e2e/20260909T144121Z/product-e2e-report.json`，原截图/trace 保留。

尝试 3 的回滚 journal 为 rollbackFailed / UPDATE_ROLLBACK_IO_FAILED；worker 记录 System.IO.IOException / HRESULT 0x80070497，本机 Win32 1175 消息为“无法删除要被替换的文件”。ledger 中 resources 已 restored，release.json 为 isolatePlanned。证据不足以证明最终具体调用，未归因 Content，未在本批混入 Updater 生产修改；独立调查处理中。

## 尝试 3 后的历史资格缺口

尝试 3 失败时，e8af runtime staging 与失败现场保留在仓库固定目录，未盲目重跑构建；当时 `dist/VibeTable.Next` 仍是此前 ee27 包，不能作为最终 S18。其后下述 staging 功能证据没有补齐完整构建资格。本次主干同步后的必要新构建及同包结果见末节，旧日志不改写。

## 保留 staging 的独立 S18 功能证据

在未改变、未 atomic publish 的 e8af staging 上，运行原产品入口：

```powershell
uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next.staging --scenario 18-workspace-search --evidence-root build/qa/content-metadata/staging-product-e2e
```

run `20260909T150507Z` / session 41605 EXIT0：1/1 PASS，21 项断言，19.554s；
四组件 freshness 通过，bridge 未确认失败与 pending 均为 0，4 项预期故障已确认。
真实 UI 的 profile 编辑、显式关联、broken link 修复和 sidecar 重启后持久状态均通过；
正常退出 Host 0，成员、后代、端口、owner lease 与最终清理通过。

该测试使用构建已完成组件与 manifest 的原 staging，未换组件、未运行旧 ee27 包、未跳过
产品 runner 的 freshness。它证明 Host 错误投影修复在真实包中有效；自更新 smoke 的
原构建失败的根因仍未确认，不能把此局部成功改写成历史完整发布构建成功。
## 同步 Mutation 主干后的边界

本分支正常合并 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`。生产与测试
注册均保留 Content 七方法和 Mutation 两方法；生成映射由
`uv run --frozen --no-sync python contracts/v2/product_rpc_capability_policy.py` 更新。
独立清单现为 Go 29 方法、Python 73 方法，未按生成结果动态削弱预期。

合并验证：`uv run --frozen --no-sync python -m pytest tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_workspace_rpc_capability_manifest.py -q --no-cov`
为 32 passed（1.09s）；sidecar 目录使用既有 Go 1.27.0 执行
`go test ./cmd/vibetable-pb -run TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart -count=1`
通过（1.691s），真实进程严格核对29个方法和 registration scope。

`go test ./internal/productrpc ./internal/contracts/productcapabilities ./internal/app -run 'TestContent|TestRecordDocument|TestMutation|TestNew|TestCapabilities|TestGenerated' -count=1`
整体退出1：productrpc 与 productcapabilities 通过，app 中
`TestMutationProductHTTPPreviewApplyAndIdempotentReplay` 在 TempDir 清理 snapshots 时
报告目录非空。没有业务断言失败，不等于整个命令通过；不加入重试或忽略失败。
日志为 `build/content-main-merge-python.log`、`build/content-main-merge-process.log` 与
`build/content-main-merge-go.log`。

主干同步改变 runtime，先前 e8af staging 的 S18 功能通过不能覆盖此次源码。
同步当时尚未重新构建，原 staging 不能覆盖新源码；本次必要构建后旧 smoke 已原样归档，
新包与同包 S18 通过结果见末节，完整远端 CI 资格仍未取得。

同步后的独立审查发现八个测试 fixture 重复了原 registration 参数。字段设置现有
`TestFieldSettingsDescribeProductHTTPReplaysFrozenPython` 首先复现
`duplicate Product RPC registration: relation.inspectPair`；删除重复项后该 HTTP 组通过
（1.081s）。随后对14个发生冲突的 fixture 文件中全部47项既有测试做一次精确筛选，
发现 query cursor/page/readRows/selection/validateSnapshot/view 与 relation preview 七处
同类重复；原日志 `build/content-main-conflict-fixtures.log` 保留，包括两项独立 TempDir
清理失败。修正仅删除合并重复参数，保留 Content 七项、Mutation 两项及所有原注册，
未修改生产注册规则或断言。

相同47项复验 `build/content-main-conflict-fixtures-correction.log` 不再报告重复注册或
业务断言失败，但仍退出1：`TestHistoryReadProductHTTPUsesPythonSemanticParamBudget`
在 TempDir 清理 coordination 目录时报非空。该运行不是完整通过，不重复运行求绿。
原字段设置 RED/GREEN 日志分别为 `build/content-main-field-settings-red.log` 与
`build/content-main-field-settings-green.log`。审查早先未逐一核对 fixture 的零问题结论
已撤回，以全部修正后的尾审为准。
## 同步后的最终源码与新包资格

固定源码 `3ef2da8c09725cbf63cc43fc5eb7ff07a62d5b97`，已同步 main 146a 并修正上述八个重复 fixture；最终独立 Standards 与 Spec 均为 0 findings。构建前工作树干净。本节只追加真实新源码资格，不撤销历史失败或把原 1175 根因归于 Content。

旧现场在启动新构建前原样归档：核实 `build/self-update-smoke` 与拟目标 `build/self-update-smoke-failed-e8af-20260909` 的绝对解析均在本 worktree 的 build 内，目标不存在、源非 reparse point，进程枚举中该目录所属活跃进程为 0；用同一 PowerShell `Move-Item -LiteralPath` 整体移动。未复制 exe、递归删除或改写 journal，journal 内绝对路径引用仍指原目录。检查和归档记录为 `build/content-e8af-smoke-archive.txt`；原诊断 probe、日志及失败目录均保留，未触碰 AV 隔离区。

- 新源码完整命令 `uv run --frozen --no-sync python scripts/build_next.py --release`，handle `62767`，**EXIT 0**。这是总第 4 次完整构建尝试、本次主干同步后新源码的第 1 次；没有失败重跑。Go 1.27.0、Node 24.19.0、.NET 10.0.401（global.json 10.0.400/latestFeature）和共享 uv 均为现有工具链，仅补本 worktree 的 Go/.NET junction，未改依赖或 lock。
- 完整阶段包含 sidecar、recovery tools、Web、PyInstaller、Host publish、产物 verify、manifest、self-update smoke、atomic publish，固定产物为 `dist/VibeTable.Next`。命令/source/终态记录 `build/qa/content-metadata/build-release-main-command.txt`，完整日志 `build/qa/content-metadata/build-release-main.log`。本次 self-update 通过不证明旧 1175 的根因已解决。
- 同一新包运行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 18-workspace-search --evidence-root build/qa/content-metadata/product-e2e-main`，handle `91707`，**EXIT 0，1/1 PASS，0 FAIL，0 skip**。21 项断言全部通过，20602ms；未重建、替换组件或跳过原 runner 检查。
- 报告 `build/qa/content-metadata/product-e2e-main/20260910T021430Z/product-e2e-report.json`；原日志 `build/qa/content-metadata/s18-main.log`、命令记录 `s18-main-command.txt`。场景目录保留 result、trace、截图、Host/runner 日志与精确 sidecar fault request/result。
- Package audit 与 desktop-host、web-grid、python-backend、pocketbase-sidecar 四组件 freshness 全通过。真实 UI 的 ContentProfile、显式 link、unlink 后 broken、repair 到第二 authority document 均通过；精确 sidecar child kill 后记录与 repaired link 在重开时持久。原搜索、历史范围、stale hit 重新解析旅程同时通过。
- Bridge failures 0、pending 0；重启窗口仅有 5 项原机制精确确认的 BACKEND_UNAVAILABLE，未吞意外失败。Renderer 无 page error、意外 console 或外部 HTTP 请求；Host 正常 exit 0，membersAfterExit、descendantsAfterExit、final remainingPids 均空，portsReleased、ownerLeaseCleanup 和 finalCleanup 全通过。

最新组合的完整 Python 入口 `uv run --frozen --no-sync python scripts/automation_project.py python-quality` 由主任务运行，handle `10769`，**EXIT 0：1851 PASS / 1 skip，78.90s，coverage 91.60%**；Ruff、Pyright、mypy 均通过，日志 `build/content-python-quality-main.log`。这是当前 3ef 与 main 146a 组合的完整结果，旧 ee27 的 1823 PASS 仍单独保留。上述本地通过不覆盖旧 Go TempDir 清理失败，也不替代尚未执行的远端完整 required 门禁。

## Host 独立 owner 预期的补充验证

同步后补查五组 Host 测试，初次为131 passed、4 failed。添加TRX的同一程序集诊断
确认四项都是迁移后遗漏的独立预期：Go方法完整列表未列七项、route selector 与公开
policy测试仍预期pythonBff，以及白名单成功输入未给新增Go方法提供合法workspace scope。
未修改生产路由或scope验证；仅在三份测试中明确列入Content七方法并按既有格式提供scope。

当前相同五组复验为135 passed、0 failed、0 skipped（18s）：

```text
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release -p:RestoreLockedMode=true --filter 'FullyQualifiedName~HostProductRpcCompositionTests|FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~WorkspaceRequestDispatcherQueryTests' --logger 'trx;LogFileName=content-main-host-contracts-correction.trx' --results-directory build/qa/content-main-host-contracts
```

原quiet日志 `build/content-main-host-contracts.log`、带详情的
`build/content-main-host-contracts-diagnostic.log` 与原TRX保留；复验日志为
`build/content-main-host-contracts-correction.log`，TRX位于上述results目录。
这些测试修正及文档相对实际构建source3ef的production runtime diff为零，不重建相同包，
当前分支仍需完整fresh CI。此处不覆盖或改变之前Go TempDir清理失败的结论。

## 真实 Runtime gate 的结果回放修复

以 `10b4d4afdf79fabc4f703fc10f1c4264a62f8c8f` 为基线。旧生产组合向 Content 同时
注入普通与 background-idempotent gate，而 handler 选择后者。已有 workspace receipt
使 Runtime 在调用 apply 前直接成功返回，五项写操作的 result 因而为 null；同键异请求
也未进入 metadata 请求摘要校验。旧无 Runtime gate 的 fixture 无法发现这一差异。

新增真实 PocketBase、Workspace v2 Runtime 和 Product HTTP 回归，在同一注册中保留
两个真实 gate。原实现 RED（`build/content-runtime-replay-red.log`）：五项写全部出现
null replay，修改 expectedRevision 的同键请求也未冲突。修复后普通 gate 执行回调，
metadata 先核验请求摘要并恢复持久 receipt，再通过既有 `ReplayedBusinessWrite` 返回
exact replay signal；Runtime 完成 prepared intent abort 后才消费该信号。
Content 同时保留恢复后的 snapshot/delete result，不把 signal 投影为领域失败。

`metadata.executeIdempotent` 只增加与并行 Preset/Surface 约定一致的可选 replay callback。
旧调用没有 callback 时行为不变；只有 exact `ErrBusinessReplay` 跳过重复 workspace
receipt 写入，其他 callback/事务错误不被吞掉。公共 Runtime/coordinator 未改。
生产 Content 注册仅注入普通 gate；kind/key、workspace epoch、写准入及事务审计不变。

使用既有 Go 1.27.0，在 sidecar 目录执行：

- `go test ./internal/app -run '^TestContentProductRuntimeGateRestoresResultsAndRejectsChangedReplay$' -count=1`：首轮修复 GREEN 1.371s（`build/content-runtime-replay-green.log`）；随后补充两个 Content 集合不变断言，最终 PASS 1.389s（`build/content-runtime-replay-final.log`）。覆盖五写原结果、同键异请求冲突、删除后重放、旧 epoch 拒绝、Runtime 关闭后拒绝，以及 mutationRevision、workspace proof、审计/outbox/幂等记录和 Content 数据均不重复变更。
- `go test ./internal/metadata ./internal/app -run 'TestContent|TestRecordDocument|TestGenericContent|TestMetadata' -count=1`：整体 EXIT 1。metadata PASS 3.744s；app 无业务断言失败，但新增 Runtime 测试的 TempDir coordination 清理非空。原日志 `build/content-runtime-related.log` 保留，不记为通过；最终单项通过不覆盖此整组失败。
- `go test ./internal/metadata -count=1`：完整 metadata 包 PASS 3.774s，用于共享内部 seam 的旧调用兼容检查；`build/content-runtime-metadata-all.log`。
- `go vet ./internal/metadata ./internal/app`：EXIT 0；`build/content-runtime-vet.log`。

本次未同步 main、push、构建或运行 S18，不声称已完成独立审查或远端资格。原完整构建
与 S18 仍只是旧 source 证据；新生产源码需要主任务安排必要新构建及产品验证。

## Runtime 重放修复后的新包资格

当前生产 source `6f1164678560c56668b93f44263b8abc8b601da0` 已正常合入 main `58032b97043c2bba80a8eb1f65ae2906797e3251`；修复及同步增量 Standards/Spec 均无新增确定问题。

- `uv run --frozen --no-sync python scripts/build_next.py --release` 完整入口退出 0，含 self-update smoke 与原子发布包目录；日志 `build/qa/content-metadata/build-release-runtime-replay.log`。
- 使用同一新包执行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/content-metadata/product-e2e-runtime-replay --scenario 18-workspace-search`：1 passed、0 failed、0 skipped，21 项断言通过，19.923s。
- 报告 `build/qa/content-metadata/product-e2e-runtime-replay/20260910T031721Z/product-e2e-report.json`：包审计及四组件 freshness 通过，未预期 bridge failure 与 pending 均为 0；正常退出码 0，进程及后代为空，端口与 owner lease/final cleanup 通过。

这组新包结果覆盖上文 Runtime 修复；历史失败记录仍保留，不把本地 TempDir 整组失败改记为通过。新的远端 head 仍需 fresh CI、严格同步、squash 及合并后 CI/CD。

## 同步 History 恢复主干

正常合入 main `a3ca78b9181a529d978f9fba46586fbb924ecada`，保留 Content 七方法与 History preview/apply 两方法。Go owner 独立清单为31项、Python为71项；生成物通过既有生成器重生，不从生成物派生测试期望。Host独立31项列表完整保留，fixture History注册各一次。

- `uv run --frozen --no-sync python -m pytest tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py -q --no-cov`：18 passed，1.15s。
- Go1.27 `go test ./internal/contracts/productcapabilities ./internal/productrpc`：两包PASS。
- `go test ./internal/app ./cmd/vibetable-pb -run 'Test(ContentProduct|GenericContent|HistoryRestore|SidecarWorkspaceV2HTTP)' -count=1`：两包PASS，4.088s/1.783s，含真实Runtime重放与实际sidecar握手。
- .NET Release：ProductRpcCapabilityManifestTests 5 passed；ProductDataSidecarRoutingTests、JsonRpcProductDataGatewayTests、ProductRpcErrorMapperTests、ProductDataRpcRegistryTests共70 passed。TRX在 `build/test-results/content-history-main/`。首个过滤另含两个不存在的类，仅报告实际5项，不作为其它路由测试证据。

旧source6f1164的完整构建/S18仍仅代表该旧组合；此合并交界定向验证不替代最新head的fresh CI。所有历史失败保持原结论。

## CodeQL 存储快照 JSON 边界修复

在 source `3df9c5e84e9df2b5fb95e54dd5d19999b823632a` 的 CodeQL check `102738495356` 中，`profileSnapshot` / `linkSnapshot` 的两处 `Potentially unsafe quoting` 指向把存储 payload 直接拼入 JSON 请求外壳。相邻快照 seam 回归确认实际错误接受：独立非法的 `合法对象,"expectedRevision":"injected"` 或 `合法对象,"idempotencyKey":"injected"` 片段在拼接后成为合法 JSON；重复外层键被解码合并，快照错误地返回成功。

两个快照现共用仅内部 `decodeStoredContent`，通过 `json.Marshal` 将 `json.RawMessage` payload 编码为单一属性值，再调用原 `DecodeContentParams` 封闭 DTO 解码；编码或解码失败均沿原路径转为 `content_model.storage_invalid`。不修改公共解码器、合法投影、revision 或冻结 oracle JSON，不 ignore 告警。回归同时保留合法引号/反斜杠内容和 revision 全值，并检查非法外层字段片段、未闭合字符串、null、数组及缺失字段对象的拒绝。

- RED：在原生产实现上运行 `go test ./internal/metadata -run '^TestContentStoredSnapshotsKeepPayloadInsideOneJSONValue$' -count=1`，EXIT 1、0.783s；profile/link 各两项非法独立 payload 被错误接受，共四项断言失败。日志 `build/content-json-wrapper-red.log`。
- GREEN：修复后 `go test ./internal/metadata -run '^TestContent' -count=1`，EXIT 0、3.855s；包含新增回归、原冻结 Python corpus、CAS/回滚与重放等 Content 相邻测试。日志 `build/content-json-wrapper-green.log`。
- `go vet ./internal/metadata` EXIT 0，日志 `build/content-json-wrapper-vet.log`；改动 Go 文件已 gofmt。

本次未运行发布构建、GUI/E2E、完整 suite 或远端 CodeQL 复验；本地回归通过不代表告警已由远端确认关闭。生产修改后的包验证与 fresh CI 由独立双轴审查后安排，旧完整构建和 S18 证据仅覆盖其原 source，历史失败记录保留。
## 新包自更新 smoke 失败

生产修复6e25b565执行 `uv run --frozen --no-sync python scripts/build_next.py --release`，shell4196已结束EXIT1，日志 `build/qa/content-metadata/build-release-json-boundary.log`。完整构建在 updated-crash 回滚 smoke 未完成，不能计作完整包PASS；旧dist包不代表此修复。

现场 `build/self-update-smoke/updated-crash/` 保留：worker错误为System.IO.IOException/HResult0x80070005，journal为rollbackFailed/UPDATE_ROLLBACK_IO_FAILED，owned group已记quiesced，唯一resources ledger在isolatePlanned；target/resources与backup/resources存在、failed-package/resources不存在。证据将失败限定在隔离资源目录前后，尚未确定占用者或访问拒绝原因。不归因杀软、不移动/删除现场、不放宽重试或回滚门禁，亦不重跑求绿。

CodeQL源码修复独立Standards/Spec均0，拟正常推送由fresh CI重新验证；远端安全告警关闭、新包S18及合并后CI/CD仍待完成。本地失败保持原结论。


## 同步 Surface owner 与当前主干（2026-09-10）

从 `ddaf39473aa6bc00c8b33806c9af0e2cebb3302d` 正常合入指定 main
`f88e856eea3b830c8f910acc3dbc9eae842eb5d1`。保留 Content 七方法、Surface 四方法和
History 恢复闭集，Go 35 方法、Python 67 方法。两套已迁移 Python handler/error domain
均删除；inventory 的旧 shared-content 组已由各自独立 Go owner 分组替代。Content
单 JSON 值 payload 解码、两类逐方法 Host 错误验证、两方 generic metadata 写禁及
普通 Runtime gate/replay signal 全部保留。主干真实进程测试同时核对 RPCMethods、
Registrations 和 workspace scope；未删除旧范围断言或从生成结果派生固定预期。
Surface 自有 fixture 显式补入 Content 七项，Content 复用的既有 schema fixture 保留双方注册。

证据目录为 `build/qa/content-surface-main/`，所有 Python 入口复用锁一致的 shared uv；
Node 24.19.0、Go 1.27.0 和 .NET 10.0.401 均为已有工具，未修改 pin/lock。

- `uv run --frozen --no-sync python -m pytest tests/backend/rpc/test_error_registry.py tests/contract/test_product_contracts.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_workspace_rpc_capability_manifest.py tests/contract/test_product_e2e_capability_index.py tests/contract/test_surface_python_oracle.py --no-cov -q`：113 PASS，3.68s，`python-contracts.log`。
- `product_rpc_capability_policy.py --check`、`generate_product_rpc_catalog.py --check`、`contracts/workbench/generate_dtos.py --check`、两套 oracle `--check`、E2E capability index `--check`、固定 Node 的场景脚本语法检查及相关五文件 Ruff format/check 全部 EXIT0，精确命令在 `generation-checks.log`。Content checker 验证28例冻结请求输入；Surface checker真实重放78例原Python完整输出。两份冻结JSON均保持原件，没有把Content输入检查称为原producer重放。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~HostProductRpcInvokerTests|FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~ProductSidecarHttpGatewayTests|FullyQualifiedName~ProductDataSidecarRoutingTests|FullyQualifiedName~JsonRpcProductSurfaceGatewayTests|FullyQualifiedName~SurfaceBridgeTests|FullyQualifiedName~ProductRpcErrorMapperTests' --logger 'trx;LogFileName=host.trx' --results-directory build/qa/content-surface-main/host --verbosity quiet`：174 PASS / 0 FAIL / 0 skip，334ms，`host.log` 与 `host/host.trx`。
- `go test ./internal/metadata ./internal/productrpc ./internal/contracts/productcapabilities ./internal/app ./cmd/vibetable-pb -run 'Test(Content|RecordDocument|GenericContent|Surface|Generated|NewRequires|WorkspaceV2WriteBoundary|SidecarWorkspaceV2HTTP)' -count=1`：五包全部 PASS，分别5.829s/0.808s/0.233s/3.332s/1.778s，`go-focused.log`。含Content存储JSON边界、冻结样本、两方真实Runtime回放、错误边界、闭集和真实进程握手。
- 从14个受影响既有app fixture提取47个顶层Test名称（`fixture-tests.txt`），加 `TestDashboardCommitUsesBusinessWriteGateAndReturnsTheAppliedReceipt`、`TestMetadataMutationUsesIdempotentBusinessWriteGate`，以 `go test ./internal/app -run '^(上述具名测试以|连接)$' -count=1` 执行：整体EXIT1，9.642s，`go-fixtures.log`。唯一失败为 `TestQueryCursorProductHTTPConsumesTypedPythonOracle` 的TempDir RemoveAll目录非空；未出现注册或业务断言失败，未重跑求绿，不称整组通过。
- `go vet ./internal/metadata ./internal/productrpc ./internal/contracts/productcapabilities ./internal/app ./cmd/vibetable-pb` EXIT0，`go-vet.log`；冲突Go文件已gofmt。

额外显式 Pyright 检查 `backend/__main__.py backend/rpc/error_registry.py contracts/v2/generate_product_rpc_catalog.py` 为EXIT1：catalog的 `model_name` nullable、两处dict值不变性、plugin event literal共4项类型错误，见 `generation-checks.log` 尾部。将指定main的原catalog用git show保存至build后，以相同解释器显式检查，`pyright-main-baseline.log` 复现同4项，原件保留为 `catalog-main-baseline.py`；main行406/483/569/810对应合并后422/499/585/826，均不在本次映射冲突逻辑内。不在同步任务混入无关类型修复，不将该检查记为通过。

本次未push、完整build或产品E2E，未复制尚未合入主干的PR330 timer改动。
历史CI、完整包和S18证据仅覆盖各自旧source；本次合并仍待独立双轴与fresh required。
