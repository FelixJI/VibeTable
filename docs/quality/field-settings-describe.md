# 字段设置描述的 Go owner 迁移资格

## 意图与契约

本切片承接独立的 [Product catalog 准入](field-settings-product-catalog.md)，只将
`field.settings.describe` 从 Python 转交 Go。`tableId` 与可选 `fieldId` 的闭字段、文本、路径、
Unicode、预算及公开错误顺序保持原契约。其他五项 Field 方法继续原 Python 路由。

Go Product adapter 使用既有 schemaCore.Describe 与可选 catalog.Field；registerFieldRoutes 返回
其已构造的同一领域实例供 Product 注册，不新建 planner、executor 或数据 authority，也不自调用 HTTP。
Python 仅退役此方法的 handler/注册执行路径；完整参数模型继续用于契约。Go 传播 context、在调用前后
检查取消，inventory 明确 cooperative。失败不回退 Python。

准备基线为 main187390c7，包含独立 catalog 候选67414ddd和原件cherry02cf82bf。
初始切片闭集16 Go /85 Python /2 Host，总103。为复用一次最终构建，本地继续整合已验证的 Lookup query/valuePage 候选697a79f6，组合闭集18 Go /83 Python /2 Host，总103；待前置 Lookup 与 catalog 合并后再同步最终main。
本地分支中依赖存在不表示已通过远端验收。

原 producer `2c211088a682163bfdd126eda4528b69cd8419f8` 的30例JSON不变。
24例经完整 Product HTTP 与原公开响应比较，包括非空完整字段定义和能力；6例逐项限定为固定typed
结构或Python transport的表达边界，见[历史原件说明](../../contracts/v2/field-settings-describe-python-oracle.md)。
当前捕获入口已退役：default/--check只校验保留输入、producer及边界，--write始终拒绝。
这不是重新生成Go expected，也不把纯类型样例视为真实存储证据。

## 本地验证与失败记录

全程复用已有uv、固定Go/.NET/Node及可信依赖缓存；新worktree仅一次locked .NET restore。

| 范围 | 命令或结果 |
| --- | --- |
| 新Go参数、1MiB、Unicode、取消 | `go test -race ./internal/app -run '^TestFieldSettingsDescribeProductParamsBudgetsUnicodeAndCancellation$' -count=1`，首次通过1.999s |
| 新Go完整HTTP与真实PB | `go test -race ./internal/app -run '^TestFieldSettingsDescribeProductHTTP' -count=1`，首次通过9.881s；真实表/字段/业务行，成功与缺字段错误不改变metadata/audit/outbox/idempotency及业务行 |
| 16方法精确注册 | `go test -race ./internal/contracts/productcapabilities ./internal/productrpc`，通过1.269s/1.687s |
| 跨方法Product整合 | `go test -race ./internal/app -run Product -count=1`，368.294s失败；两个TempDir RemoveAll清理失败，见下文 |
| 真实sidecar进程 | `go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1`，通过11.727s |
| Go vet | `go vet ./internal/app ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb`，通过 |
| Python相关五文件 | 首轮59 passed/2 failed，2.30s；修正两项后仅两项通过0.29s，不声称全组重跑 |
| Host八类 | 首轮145 passed/3 failed/0 skipped，355ms；修正后仅两类64 passed/0 failed/skipped，82ms |
| E2E runner/index | 150 passed，8.21s，包含锁定Node恢复契约入口 |
| 生成/类型 | 正式policy与保留原件check通过，能力索引经脚本更新；backend加新保留器及其测试Pyright为0 errors |

Python首轮命令：

```text
uv run --frozen --no-sync python -m pytest tests/contract/test_field_settings_describe_python_oracle.py tests/contract/test_product_contracts.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/backend/adapters/test_pocketbase_product_rpc.py -q --no-cov
```

两失败分别为保留测试仍引用已删除的render捕获辅助函数、inventory测试仍使用旧Go集合。修正为直接JSON文本检查和
新增单方法后，只运行 `test_forwarding_projection_errors_and_rejection_order` 与
`test_inventory_covers_the_fresh_product_catalog_with_migrated_current_owners`，2 passed。

Host使用 `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --artifacts-path build/dotnet`。
首轮filter覆盖 ProductDataSidecarRoutingTests、ProductRpcCapabilityManifestTests、ProductRpcRouteSelectorTests、
WebMessageRouterTests、WorkspaceSessionEnvelopeFilterTests、HostProductRpcInvokerTests、ProductDataRpcRegistryTests及
ProductDataRequestControllerTests。旧白名单样例缺scope，另两例错误沿用Relation映射；修正为Product原Python与Go实际路径对照，
覆盖非法参数、普通远端错误、无data领域错误及完整领域错误，不改生产mapper。只重跑ProductDataSidecarRoutingTests与WebMessageRouterTests。

恢复探针已从迁Go的describe改为仍由Python负责的field.recycleBin.list，保留同一deadline、requestId、终态、失败确认及释放契约。
首次直接Node契约为24 passed/3 failed，原因是精确两方法allowlist仍为旧describe；同步为query.page与recycleBin后，执行正式
`uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q --no-cov`，150 passed。
没有扩大allowlist或降低生命周期断言。

跨方法Go失败均发生在测试结束的Windows临时目录清理：新TestFieldSettingsDescribeProductHTTPReadsRealAuthorityWithoutWrites，
以及旧TestSchemaGetTableProductHTTPRejectsInvalidTableMetadata/data-revision。没有业务断言失败信息；不据此宣称该组通过或根因已确定。
保留原日志，不重跑取绿、不添加cleanup重试或杀软例外。此前新适配独立HTTP通过与本次整合失败分别记录。

## Lookup 组合验证

本地组合保留三个 owner 的独立意图与冻结原件；远端仍分别交付。组合增量 Host 原八类 169 passed/0 failed/skipped，645ms；Python 九文件首轮144 passed/1 failed（1.73s），旧测试两处owner集合遗漏describe。修正时一次文本替换误触events参数行导致收集错误，已移除误行；补describe有效/非法形状均无Python注册的两例后，仅该文件27 passed（0.44s），不声称全组重跑。Go三方法 `go test -race ./internal/app -run '^Test(FieldSettingsDescribe|LookupQuery|LookupValuePage)Product' -count=1` 通过44.525s；注册两包通过1.276s/1.669s，实际sidecar子进程通过11.887s。四包vet通过。runner/index150 passed（8.84s），backend及三保留器Pyright 0 errors。日志为build/field-lookup-combined-*。组合增量 Standards 与 Spec 的发现已修正，最终未解决0/0；完整构建及 S02/S03待完成。

## 审查与剩余资格

Standards指出取消声明需同步，Spec指出历史捕获文档需更新，均已修正，最终增量 Standards 与 Spec 无新增未解决问题。
两项非阻断重复接受在本切片局部保留：已有REST与新typed adapter的八字段投影、原始Unicode预算扫描。
前者复用同一领域实例和冻结结果约束，后者保留既有严格预算算法；集中重构会扩大多个已迁方法或共享REST的本次审查范围。

初始16-owner阶段，Host作者不复核自己六个测试文件；主代理独立检查并要求保留当时仍Python的 Lookup Relation drain 测试，新增 Python Field drain 独立存在。当时两方法定向复验 2 passed/0 failed/skipped，125ms（filter 为 RelationReadSettlesBeforeRetiredRuntimeDrains|PythonFieldReadSettlesBeforeRetiredRuntimeDrains）。18-owner组合中 Lookup 已退役 Python，原等待样例改为仍Python的 field.change.status/jobId，另一条 recycleBin 样例独立保留；旧125ms不作为新方法证据。其余源码由独立Spec代理审查，Standards代理独立检查全部。
S02增加与本次实际requestId对应的成功描述回程、完整已恢复字段及非空能力断言；S03仍验证字段错误。
完整新构建和真实S02/S03产品运行尚未执行；旧Python包或其他方法的S29不是此owner资格。

fresh CI、严格最新main、squash及合并后CI/CD仍待完成；不修改CI、覆盖率、SLO、发布流程或不支持格式零写拒绝契约。
本地日志在build/field-settings-describe-*、field-settings-owner-*，不提交构建包或缓存。
