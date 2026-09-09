# 已资格来源：计算预检、Lookup 更新与副本准入

本端点按已批准的批次流程，将各自完成完整 CI 的 #313、#315、#314 固定来源同步到实际 main #312，统一验证合并结果。初始三个来源各自保留独立意图和审查记录；随后追加各自通过完整 CI 的纯测试 #316 与计划文档 #317，理由及固定版本见末节。#306、A5、关系检查与 S24 不进入本批。

| 来源 | 固定 head | 独立 CI |
| --- | --- | --- |
| #313 混合计算依赖环预检与提交重读 | `598890e6d7b74cde5ba904907b23733cafe3294c` | `34299282171` SUCCESS |
| #315 Lookup 配置更新分类 | `31b61c53ba605411118bd3796dbf0ddcd24f6fa4` | `34302081565` SUCCESS |
| #314 provisional 副本受限操作准入 | `9261f2db5f84555759907b6ae0b69f24bf4a0e5e` | `34302021455` SUCCESS |

main 基线 `3cd6f83202ea0977d690ad71cf7dff25595e4f02`；三次正常合并后产品源码固定为 `0508a6415b7204357f46e01f0d255bc1a6d0a404`。唯一冲突位于 fieldchange catalog：先保留计算依赖校验，再保留双端关系基数检查的返回；main 的 Formula compiler/cache/schema revision 接线均保留。Lookup 配置变化现在进入 schema 分类与候选图校验，提交仍在元数据替换前重读。Host 源码和测试与 #314 相同，只准入既有状态与 epoch lease 约束下的两种副本操作，不放宽普通写入。

Standards/Spec 双轴及实际冲突交界复核无确定问题。此批不代表统一计算 runtime 计划或 S24 真实冲突闭环已完成。源 PR 资格中的失败、限制与旧源码结果继续保留。

## 固定组合的本地验证

日志均在 `build/qa/qualified-schema-admission/`，复用原有 uv、Node、Go、.NET 环境及下载缓存，未重建环境或改锁文件。

- `go test -race ./internal/computationplan ./internal/fieldchange ./internal/schemaapi ./internal/app`：整体 FAIL。前三包分别 PASS 1.744s/30.026s/4.878s；app 470.432s FAIL，见下文。日志 `core-race.log`。
- `go test -race ./tests/integration -run 'Test(FieldComputation|LookupConfigurationUpdate|FormulaAndLookupCreateThroughFieldChangeV2|FormulaPlanAcceptsFreshNumberPhysicalNameTimesFloatLiteral|RelationPair(Patch|Update))' -count=1 -v`：13 个顶层测试 PASS，132.919s；`integration-race.log`。
- `go vet ./internal/computationplan ./internal/fieldchange ./internal/schemaapi ./internal/app ./tests/integration`：EXIT 0；`vet.log`。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release /p:RestoreLockedMode=true --filter 'FullyQualifiedName~WorkspaceProductControllerInterfaceTests|FullyQualifiedName~WorkspaceSessionEnvelopeFilterTests'`：69 PASS、0 skip，330ms；`host-tests-correct-path.log`。
- `uv run --frozen --no-sync python scripts/build_next.py`：完整产品构建 EXIT 0；`product-build.log`。产品源码在构建期间保持固定。

app 失败均为 TempDir RemoveAll 目录非空：`TestHistoryReadProductHTTPReturnsFreshAuditedPage` 的 audit 目录，`TestQueryCursorProductHTTPConsumesOriginalPythonOracle` 的 fixture 根目录，以及 `TestSchemaProductStoreCleanupTerminatesBeforeReset/fixture` 的根目录。日志未报告 race detector 或业务断言失败；这不足以确定清理失败根因，更不能把完整命令记为通过。未重跑、未增加重试或放宽清理断言，该次运行尚未包含 #316。后续纳入 #316 的独立测试终止修复不用于解释这些 app 包失败。

两次入口配置错误保留：`host-tests.log` 使用错误的 desktop/src 测试项目路径，MSB1009、尚未执行测试；`product-e2e.log` 的三个场景名称与 manifest 不符，配置检查 EXIT 2、尚未启动产品。修正入口后复用同一构建，不为入口错误重建候选。

## 同一构建的真实产品场景

`uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 02-all-field-schema --scenario 05-formula-lifecycle --scenario 12-backup-consistency --scenario 23-directory-replica-recovery`：run `20260909T031448Z`，4/4 PASS、0 skip，63 断言。S02 为16.601s/18断言，S05为5.725s/7断言，S12为17.375s/22断言，S23为16.421s/16断言。

四组件 freshness 通过；四场景均直接运行真实 WPF/WebView2，Node/Host 退出0，pageErrors、bridge failures/pending 为0。S02与S12各有1项已断言的 acknowledged failure，其他场景为0；不将其误记为无任何拒绝响应。正常关闭后成员与后代为空、端口释放、lease关闭、最终清理全部通过。日志 `product-e2e-correct-scenarios.log`；报告 `build/qa/product-e2e/20260909T031448Z/product-e2e-report.json`。

这些通过结果不替代完整 app race 的失败，也不代表 S24 已验收。fresh PR CI 尚待终态，不计为通过。


## 完整 CI 前追加两项已资格测试与文档来源

初始端点 `fea02908` 的 CI `34306769582` 仍在 prepare 时，#316 与 #317 已各自完整 required SUCCESS。按同一已批准批次流程加入 #316 `47a42cf6da88c11d97f607bc2b87fe473634a82e`（CI `34302721986`）与 #317 `ca9eb4b8ec9dbbfa732332c84ccd7980c8f83533`（CI `34303480074`），最终五来源正常合并为 `61deb7699114ab818c43af3138d48fc8c9ffcb4e`，无冲突。

相对初始端点只增加 schemaapi 测试夹具、其资格文档与计划文档。`git diff --quiet 0508a6415b7204357f46e01f0d255bc1a6d0a404 HEAD -- backend desktop contracts sidecar ':!sidecar/internal/schemaapi/catalog_lifecycle_test.go'` 为 EXIT 0：生产源码未变，因此保留并复用上述0508完整产品包及实际四场景资格，无需重建可执行文件。该证据不把旧构建 source SHA 改写为新提交。

对改变的测试集合执行一次 `go test -race ./internal/schemaapi -count=1`：PASS 9.135s；`go vet ./internal/schemaapi`：EXIT 0。日志 `five-source-schemaapi-race.log` 与 `five-source-schemaapi-vet.log`。Standards/Spec 实际交界无确定问题。原 app 包三项 TempDir 失败仍为失败，不能由不同包的测试修复与通过解释或覆盖。更新后的新 head 必须重新取得完整 fresh required，初始端点的未完成 CI 不充当最终门禁。
