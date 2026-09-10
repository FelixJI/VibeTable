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

## 同步已合并的关系检查 main

#319 已 squash 合并为最新 main `25b260394a0a01e8432d23fa3d1a6e8b9b65922f`。本分支从旧端点 `f158cd5836a76c2586846e48354173af76ff91f5` 正常无冲突合入 main，得到 `b9984c931a6e042f142e293be2dd07a36c939558`。五项来源范围不变；新增关系检查属于已合并 main 的依赖，未把其他待合并 owner 或稳定性修复混入本 PR。

上节“生产源码未变”仅适用于追加 #316/#317 的旧端点。本次 main 同步包含 RR2 生产代码，不能把旧 `0508` 的完整构建和四场景结果归为当前源码资格。复用现有依赖与工具缓存，运行以下交界验证（日志仍位于 `build/qa/qualified-schema-admission/`）：

- `go test -race ./internal/app -run 'Test(RelationInspectProduct|ClassifyFieldErrorPreservesComputationCycle)' -count=1`：PASS，1.777s；`main-319-app-race.log`。
- `go test -race ./internal/schemaapi ./internal/computationplan ./internal/fieldchange -count=1`：三包 PASS，9.110s/1.663s/30.636s；`main-319-core-race.log`。
- Web `npm run test -- --run src/relation-inspection/RelationInspectionPanel.test.ts src/relation-inspection/type.test.ts src/field-settings/service.test.ts`：71 PASS，3 文件，2.65s；`main-319-web.log`。

Standards 与独立 Spec 交界审查无确定代码问题：计算依赖校验、pair 基数顺序、schema 元数据替换前复核、Lookup 分类和 Provisional 准入均保留，新增 inspection 接线与 main 一致。本地未再次构建或运行完整产品场景；最新源码的完整 build/E2E 与最终 required 由新 head 的 fresh CI 验证，尚待终态。旧 CI `34307773041` 不能替代新 head 门禁；旧完整 app race 失败记录同样保留。

## Fresh CI 视口超时与诊断补全

head `0fde466d141e8b3872b2f7381de98728b203232d` 的 CI34349172860，core job102463248877 于2026-09-09 12:41 UTC失败：既有 Lookup 来源视口 Node 测试超过15秒限制，实际报告19.226s，Node合约220 PASS / 1 cancelled、Python入口1805 PASS / 1 FAIL。测试与基线main25b无diff，旧日志没有活动阶段，无法确定阻塞在截图、页面创建、清理还是其他未记录步骤；不据此断言运行器慢或产品缺陷已修复。

本次仅为该失败补齐未记录await的既有phase观察，覆盖页面创建、截图、取样、后续点击和清理。保留所有原断言、15秒整体限制及2秒点击限制，没有重试或吞错。独立Standards/Spec均0确定问题。

独立诊断工作树原样测试1 PASS（1641ms），补阶段后1 PASS（1551ms）。固定Node24.19.0的本地负控通过build目录临时preloader使截图不返回：既有15秒超时仍失败（进程exit1），日志明确记录pending phase `capture initial screenshot`及此前阶段耗时。它证明诊断能区分截图阻塞，不证明原CI即为截图问题。未重建产品包。当前候选仍须新的完整fresh CI，旧FAIL继续保留。

### 最新主干组合与 viewport 挂载诊断

本分支正常合入主干 `146a9c2c`（Mutation 成对 owner）。上一 head `5f9594f4` 的
CI `34361854095` 全部通过，但不能替代同步后新 head 的门禁。
PR323 的 CI `34361844114` 显示同一 viewport 契约在挂载阶段超时：launch 741ms，
mount pending 7793ms / total 15001ms，尚不能确定具体 await。

在原外围 phase 基础上，将挂载的八个 await 分别计时，并在 abort 时同步输出已缓存
pageerror；保留 15s、原操作顺序、真实组件、焦点 readiness 与全部断言，不增加 CDP 等待。
相同测试与生产模板在固定 Node 24.19.0 的诊断工作树单次通过 2109.716ms；人为卡住焦点
await 的单次负控按既有 15s 失败，准确报告 pending focus phase 与缓存 pageerror。
负控仅验证诊断标签，不是原 CI 故障的复现，不能据此称根因已修复。

主干 CI `34367794428` 的工作台查询取消测试也暴露了旧观察缺口：两次 RequestQuery
均发生在 debounce 前，随后睡眠 350ms 并检查至少一次调用，既依赖调度，也未验证在途取消。
本交付的工作台资格同时改为使用既有 ManualTimeProvider，等待首读开始后再 supersede，
确认首 token 取消、第二读完成，严格断言两次调用与仅新版成功通知。生产逻辑不改，
不通过增加 sleep 掩盖问题。相同测试在独立诊断工作树单次通过 32ms；锁定 restore 与
warnings-as-errors 正常。该结果不代表主干原 CI 已通过，组合后仍需新 head 的完整 CI。
