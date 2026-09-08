# Formula 编译计划缓存资格

状态：独立缓存候选的相关 Go race、vet 和作者集成回归已通过；全量 Go 因既有临时目录清理失败未通过，真实打包与 fresh CI 尚未执行。本文不声明 Formula、统一 ComputationPlan 或 ADR 0013 整体验收完成。

## 来源与独立边界

基线为 `main@3bf03bc6f47399bacbdb1305a9b110c8d3b11ae5`。从旧语义缓存分支四个提交 `74d73ca9`、`8bb5fd82`、`177211d2`、`6631da8e` 中按增量恢复缓存意图，保留当前 main 的 PR217 作者文档、源码位置和 UTF-16 映射实现。数值语义修正已拆出；本候选不改变除法、非有限值或数值输入边界。

生产与测试共 12 个 Go 文件，另有本文档：

- `sidecar/internal/formula/cache.go`、`cache_test.go`：128 条 LRU，待编译条目计入容量；相同定义并发编译合并。缓存键使用 Schema revision 和完整字段定义，排除数据水位与运行状态；不同草稿不互相复用，编译错误不留在缓存中。
- `sidecar/internal/formula/app_compiler.go`、`app_compiler_test.go`：应用与事务副本共享编译器，缓存准入读取根应用的已提交 revision；提交后失效，回滚不失效，迟到回调不驱逐较新计划。事务中新表尚不可见时只编译、不准入；提交后可缓存，回滚后不残留。
- `sidecar/internal/formula/compiler.go`、`plan.go`：将有界缓存放到 `CompileExecutionTable` 入口，保留原编译错误与源码位置；没有迁入数值语义 hunk。
- `sidecar/internal/formula/calculator.go`、`calculator_test.go`：移除 Calculator 私有无界缓存及其重复摘要，复用同一编译器计划。
- `sidecar/internal/formula/schema_v2.go`、`sidecar/internal/fieldchange/catalog.go`：作者草稿携带 Schema revision，Catalog 计划编译消费应用编译器；作者解析与 source map 保持原契约。
- `sidecar/internal/app/app.go`、`sidecar/internal/schemaapi/catalog.go`：应用启动安装共享编译器，Schema 校验与计算元数据准备复用该入口。

缓存刷新读取失败会清除该次观察到的旧条目并记录诊断，随后继续 PocketBase 的 after-success hook 链，不把已经提交的写入伪装成失败。真正的下游 hook 错误仍返回调用方；缓存准入时的其他读取错误也仍返回。未修改 PocketBase、RPC owner、公共自动化、CI、依赖版本或 lock；`go.mod` 与 `go.sum` 保持基线内容。

## 独立缓存验证

以下命令均在 `sidecar/` 执行，使用现有 Go 1.27.0、w64devkit 与可信模块/构建缓存，`GOFLAGS=-mod=readonly`、`CGO_ENABLED=1`；未重建环境。日志根目录为 `build/qa/formula-semantic-cache/`。

| 精确命令 | 结果 | 日志 |
|---|---|---|
| `go test -race ./internal/formula ./internal/fieldchange ./internal/schemaapi -count=1` | EXIT 0；Formula 4.429s、FieldChange 22.201s、SchemaAPI 4.945s | `cache-only-race.log` |
| `go test -race ./tests/integration -run '^TestFormulaAuthor' -count=1` | EXIT 0，9.969s | `cache-only-author-race.log` |
| `go vet ./internal/formula ./internal/fieldchange ./internal/schemaapi` | EXIT 0 | `cache-only-vet.log` |

对这 12 个 Go 文件执行 `gofmt -l` 输出为空，`git diff --check` 退出 0。上述 race 包含容量、并发合并、失效期间编译、迟到回调、不同草稿、数据更新复用，以及真实 PocketBase 的提交、回滚和 hook 错误边界。作者交界同时覆盖草稿计划隔离、展示名变化后的 UTF-16 错误位置，以及真实 Catalog 的引用缺失和目标改名往返。

## 保留的 RED 历史

以下是移植阶段的故障与修复证据，不替代上面的独立缓存验证。首次旧 main 的精确入口为：

```powershell
go test ./internal/formula -run 'TestCELV1|TestCalculatorSharesCompilerPlan' -count=1
```

| 故障与精确入口 | RED | 后续证据 |
|---|---|---|
| 首次旧 main 入口中的 `TestCalculatorSharesCompilerPlan` | `semantic-red.log` 中 `TestCalculatorSharesCompilerPlan` 明确得到两个不同计划；该日志还含数值语义失败，后者属于独立修正 | 独立 `cache-only-race.log` 包含共享计划回归 |
| `go test ./internal/formula -run '^TestAppCompilerInvalidatesOnlyCommittedSchemaChanges$' -count=1` | `pending-table-red.log`：未提交新表被缓存准入误拒绝，`formula.runtime`，0.761s | `pending-table-green.log` 通过，0.766s；后增回滚不残留断言已在独立 race 通过 |
| `go test ./internal/formula -run '^TestAppCompiler' -count=1` | `after-commit-red.log`：direct 与 transaction 都已提交 revision，却返回缓存读取错误并中断后续 hook，0.822s | `after-commit-green.log` 通过，0.827s；独立 race 同时验证下游真实错误仍传播 |

数值黄金测试已归档在本地 `build/qa/formula-semantic-cache/semantic_golden_test.go.archived`，对应剥离 patch 为 `semantic-split.patch`。移植阶段组合运行的 `formula-green.log`、`focused-final.log`、`race-final.log` 与 `race-consumers.log` 不作为纯缓存候选的独立资格；其中大关系聚合与后台重算结果也不扩写为本候选的完整产品验收。

## 尚未执行

独立缓存候选尚未执行其他语言完整质量入口、真实打包及产品场景、最新远端 main 同步后的 fresh PR CI。当前结果没有修复或取代其他候选中已保留的 Windows/SQLite 清理失败证据；不得据此放宽清理断言或发布门禁。

## 完整 Go 结果

固定源码 `56e74b7a142f0f1a994ec9dd6bd38dd7aef43dee` 执行一次 `go test ./...`：EXIT 1，未重试。日志 `build/qa/formula-semantic-cache/go-all.log`。失败包为 objectrepo（18.124s）与 workspacev2（80.346s），共7处叶用例清理失败，均为 `testing.go:1617: TempDir RemoveAll cleanup` 目录非空：

- `TestKopiaRetentionFaultBoundariesReplayIdempotently/before-content-delete`：repository/x/n0_。
- `TestImportedRestoreAuditIsLocalAndIdempotentAcrossCompletionReplay`：coordination。
- `TestSnapshotRestoreValidatesWindowsStorageKeysBeforeAttachmentStaging` 的 trailing_space、valid_PocketBase_key：分别为 coordination、snapshots。
- `TestRestoreJournalRejectsWindowsInvalidAttachmentKeys`：snapshots。
- `TestSnapshotRestoreCommitsAuthorityAndRecoversFailedSearchRebuildAfterRestart`：coordination。
- `TestInterruptedInstalledSnapshotRestoreRollsBackBeforeReadiness/missing-previous`：coordination。

同次 cmd/vibetable-pb、app、formula、fieldchange、schemaapi、integration 均通过，分别11.250s、21.924s、4.736s、6.083s、3.185s、37.713s。失败用例日志未记录其他业务断言失败，但这不足以判断目录持有者或晚写来源，也不能将完整 Go 记为通过。保留原始失败，不修改清理策略或降低门禁。