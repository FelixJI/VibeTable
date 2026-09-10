# ContentProfile / RecordDocumentLink 迁移资格记录

状态：源码与聚焦契约已通过审查和相关验证；最终发布构建失败，保留 staging 的 S18 功能验证已通过，但完整构建及其后产品资格仍未完成。本记录不声明本批交付完成、可合并、L5 完成或 PR #140 整体完成。

## 来源与完整范围

- 原公开行为 producer：`79c2ce4faa53d65af1fa9ee297655739d5407a2e`；冻结提交 `357d70e4` 捕获 28 例真实 Python 注册/编排/DTO 输入输出，见[原契约说明](../../contracts/v2/content-metadata-python-oracle.md)。metadata/schema 查询 adapter 的边界明确，不能把该 corpus 当作真实包证据。
- 完整迁移提交：`caecfeb5d76508679721e18816d51f3ff74f6f2e`。正常同步 main `12556e5db81dd49592d69b5af1780007ccd36c37` 后源码为 `ee27eb0584f6f3de888909b079a7407e3b25287b`；同步只涉及五份文档。
- 真实 S18 暴露的 Host 错误投影修复：`e8af1501f21303b80ccc6c6250ab4c72c3d63c63`，为当前最终 runtime 源码。
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

## 三次构建尝试与首次 S18

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

## 当前资格缺口

最终 e8af runtime staging 与失败现场保持在仓库固定目录，未盲目重跑构建。`dist/VibeTable.Next` 仍是此前 ee27 包，不能拿它运行所谓最终 S18。需在完成回滚问题处置并恢复完整构建资格后，以同一最终新包运行 S18，核对 profile、broken link→repair、sidecar restart 后持久状态、bridge 及清理。完整构建后的最终 S18 资格为 pending；下述 staging 功能证据不授予合并或发布资格。

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
原构建失败仍未解决，不能把此局部成功改写成完整发布构建成功。
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
原完整构建的自更新 smoke 失败、原 staging 和相关证据均保留；本次未重新构建，
尚未取得最终包与完整远端 CI 资格。

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
