# 混合计算依赖环校验资格

## 范围与行为

本次基于 `cadf51533ed45c3793c8f75b21025375d4d3f633`，只补充 Formula/Lookup 混合依赖环校验。原实现分别验证 Formula 本地依赖和跨表引用存在性，合法的 Lookup→Formula、Formula→Lookup 引用组合仍能形成循环，并且预检后其他表变化可能使冻结的字段计划在提交时形成新环。

`computationplan.Validate(ctx, candidate, resolve)` 使用候选字段覆盖所在表，只加载可达表，同一次校验每表至多解析一次。FieldChange 预检使用同一读取事务；`schemaapi.SyncComputedMetadata` 在既有引用验证之后、任何派生元数据替换之前调用相同校验，使用权威提交事务重新读取目标表。发现混合环时返回 `schema.computation.cycle`，包含稳定 `tableId`/`fieldId` 闭环。既有本地 `formula.cycle` 优先级保留；合法 computed 目标、COUNT 的关系成员依赖和普通 JSON 属性访问继续允许。

七个 Go 文件构成本次实现和回归：`internal/computationplan/plan.go`、`plan_test.go`，`internal/fieldchange/catalog.go`，`internal/schemaapi/catalog.go`，`internal/app/field_routes.go`、`field_routes_test.go`，`tests/integration/field_computation_plan_test.go`（均相对 `sidecar/`）。另含本资格文档。

最终接线只在 schemaapi 增加 import 和 Validate 调用。`computation_dependencies.go`、Formula 编译准备与派生元数据写入算法保持原样；没有新增 hash、执行计划协议、运行时计算或 RPC。该工作不代表 ADR 0013 的统一执行计划全部完成，也不改变缺少被编辑字段身份的独立草稿协议。

## 回归证据

以下命令均在 `sidecar/` 执行，使用既有 Go 1.27.0、w64devkit 和模块/构建缓存，`GOFLAGS=-mod=readonly`、`CGO_ENABLED=1`。日志相对仓库根目录的 `build/qa/computation-dependency-graph/`；失败记录完整保留。

| 日志 | 命令与结果 |
| --- | --- |
| `mixed-cycle-red.log` | `go test ./tests/integration -run '^TestFieldComputationPlanRejectsMixedCycle$' -count=1 -v`：旧实现 FAIL，1.153s；真实 PocketBase 本地和跨表两例均错误返回 `CanApply=true`。 |
| `error-mapping-red.log` | `go test ./internal/app -run '^TestClassifyFieldErrorPreservesComputationCycle$' -count=1 -v`：旧映射 FAIL，0.775s；领域错误被改成 HTTP 500 / `field.internal.failed`。测试随后依现有领域错误契约明确预期为 HTTP 422。 |
| `preflight-green.log` | `go test ./internal/computationplan ./internal/fieldchange ./internal/app ./tests/integration -run 'Test(Validate\|ClassifyFieldErrorPreservesComputationCycle\|FieldComputationPlanRejectsMixedCycle\|FormulaPlanAcceptsFreshNumberPhysicalNameTimesFloatLiteral\|FormulaAndLookupCreateThroughFieldChangeV2)' -count=1 -v`：PASS；内核 0.763s、app 0.793s、integration 1.642s。此过滤下 fieldchange 无匹配测试，不作为该包覆盖证据。 |
| `commit-recheck-reachable-red.log` | `go test ./tests/integration -run '^TestFieldComputationApplyRechecksOtherTableAndRollsBack$' -count=1 -v`：权威提交接线前 FAIL，1.032s；冻结 A 表计划后合法更新 B 表，提交错误接受新形成的三节点环。 |
| `commit-metadata-verified.log` | `go test ./tests/integration -run '^TestFieldComputation(ApplyRechecksOtherTableAndRollsBack\|MetadataKeepsVersionsAndPathSemantics)$' -count=1 -v`：PASS，1.306s。随后将失败审计断言进一步明确为该 operation 唯一一条，最终版本由下述 integration race 覆盖。 |
| `core-full.log` | `go test ./internal/computationplan ./internal/fieldchange ./internal/schemaapi ./internal/app -count=1`：四包 PASS，分别 0.726s、2.528s、1.021s、29.545s。 |
| `core-race.log` | `go test -race ./internal/computationplan ./internal/fieldchange ./internal/schemaapi ./internal/app -count=1`：四包 PASS，分别 1.713s、30.012s、4.970s、472.726s；单次原进程终态 EXIT 0，无重试。 |
| `integration-race.log` | `go test -race ./tests/integration -run 'Test(FieldComputation\|FormulaAndLookupCreateThroughFieldChangeV2\|FormulaPlanAcceptsFreshNumberPhysicalNameTimesFloatLiteral\|FormulaAuthor\|LegacyComputation)' -count=1 -v`：PASS，61.458s；实际匹配七个顶层测试，覆盖本次三项真实存储回归、现有字段创建/物理名以及作者源引用交界。没有匹配 LegacyComputation 测试，不单独声称覆盖该迁移。 |
| `vet.log` | `go vet ./internal/computationplan ./internal/fieldchange ./internal/schemaapi ./internal/app ./tests/integration`：EXIT 0，无诊断输出。 |
| `go-all.log` | `go test ./... -count=1`：单次完整运行 EXIT 1，objectrepo（24.074s）与 workspacev2（112.365s）失败，其他有测试的包均 PASS，包括完整 integration（67.648s）。未重试。 |

两次失败夹具纠正不计作产品缺陷修复：`commit-recheck-red.log` 最初只改变 Lookup 目标，被既有分类器判为 `field.change.noop`，随后改用合法 Formula 更新构造可达三节点环；`commit-metadata-green.log` 已正确拒绝环，但测试把所有失败审计纳入不变快照，违背既有失败审计契约，随后修正断言，未更改执行器。

提交拒绝回归验证权威 schema、版本、两类派生元数据及成功审计均未改变，不生成成功 receipt；允许且要求本 operation 唯一的失败审计。失败审计 code 仍沿用执行器既有的 `field.change.apply_failed` 降级，外部返回保留精确 `schema.computation.cycle`，不宣称审计 code 已细分。

真实持久化回归还核对 FormulaRuntime 版本递增、Lookup metadata revision、跨表依赖各 hop 的 `__path__`、首跳 relation ID、完整 Path 和 definition version；COUNT 不生成目标值依赖。嵌套在父事务中的预检不会提前提交，父事务回滚后标记记录不存在。内核测试覆盖候选覆盖、仅解析可达表、retired 字段、读取错误与取消传播，以及本地 Formula 错误优先级。

完整 Go 运行的五个失败均报告 `testing.go:1617: TempDir RemoveAll cleanup: unlinkat ...: The directory is not empty`：

| 包 / 用例 | 报错目录后缀 |
| --- | --- |
| objectrepo / `TestKopiaRetentionFaultBoundariesReplayIdempotently/after-content-delete` | `repository/x/n0_` |
| workspacev2 / `TestExternalFileOperationResumesBothAtomicReplaceKillWindows/replaced-before-central-receipt` | `002` |
| workspacev2 / `TestMirroredRuntimeRequiresAndComposesVerifiedFilesystemReplica` | `workspace/.vibetable/objects/kopia/_/log` |
| workspacev2 / `TestConflictReplanPinCleanupSurvivesEngineReopen` | `workspace/.vibetable/coordination` |
| workspacev2 / `TestConflictExternalNormalFaultRollsBackSettingsAndRequestsShutdownOnlyAfterReceipt` | `workspace/.vibetable/coordination` |

同一日志另有三条 `retention.inventory_unsafe` 后台日志；现有输出不足以建立其与目录清理失败的因果关系，也不能据此归因操作系统或杀软。保留完整运行 FAIL，不修改清理逻辑、断言或增加重试。

## 剩余验证

七个 Go 文件的 `gofmt -l` 无输出，`git diff --check` EXIT 0。完整 Go 结果如上为 FAIL。尚未运行完整产品构建、实际产品 E2E 或此候选的 fresh CI；这些不得由聚焦测试结果代替。
