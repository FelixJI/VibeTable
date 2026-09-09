# Lookup 页面批量投影资格

状态：在 main `3bf03bc6f47399bacbdb1305a9b110c8d3b11ae5` 上增量移植，相关 Go 回归及独立 Standards / Spec 审查通过；本候选尚未执行完整 `go test ./...`、完整产品构建、真实产品场景或 fresh PR CI。整体 Relation / Lookup / Formula 资格不因此改为 Closed。

## 单一意图与边界

恢复旧 `cfa6c9db`、`cd774407` 的页面批量读取意图，保留当前 main 的 owner、QueryPort、关系标签与刷新契约。实现及测试共七个文件：

- `sidecar/internal/lookup/batch.go`、`batch_test.go`
- `sidecar/internal/lookup/calculator.go`、`calculator_test.go`
- `sidecar/internal/relation/service.go`
- `sidecar/tests/integration/lookup_calculator_test.go`、`lookup_page_batch_test.go`

`CalculateCellsBatch` 在一次请求内按 Lookup 路径分组，共享 schema 和记录缓存；相同目标 ID 去重读取，前沿与数据库读取按 256 条分块。遍历保持原路径顺序和重复来源，Grid 每格投影前 100 个来源，详情使用同一分页实现。多跳扫描提前停止时明确标记总数未知；缺失来源只使消费该来源的单元格失效，schema/存储错误仍返回错误。读取与投影各自受既有 32 MiB 预算限制，取消沿用 context。

Relation service 批量回读页面来源，再将 Lookup 单元格写入原行；保留业务关系 ID、`__vibetableRelationLabels`、行序与页面结构。完整写入物化继续使用完整遍历，不能以 Grid 前 100 个来源替代全部值。

没有新增 RPC、切换 owner、修改锁文件、CI 或后台刷新策略。当前产品 handler 对 `fieldRefs:[]` 仍先调用 `QueryLookups` 计算全部 Lookup，再投影响应；本改动没有优化这一特例。新增 batch API 的空 selection 仅表示局部调用不计算字段，不代表产品 handler 已传递该选择。

## RED 与查询次数

仅先加入真实 PocketBase 集成测试，在旧 main 上执行：

```powershell
go test ./tests/integration -run '^TestLookupQueryPageSharesTargetReads$' -count=1
```

退出 1，日志 `build/qa/lookup-page-projection/query-reads-red.log`。页面为 4、64、300 行，每行四个同路径 Lookup、共享 16 个目标，目标 SELECT 分别为 16、256、1200，违反测试保留的 1–2 次上界。

移植后同一真实服务入口在 race 回归中得到：

| 页面行数 | Lookup 字段数 | 目标 SELECT | 来源 SELECT |
| --- | --- | --- | --- |
| 4 | 4 | 1 | 1 |
| 64 | 4 | 1 | 1 |
| 300 | 4 | 1 | 2 |

计数通过 PocketBase `ConcurrentDB().QueryLogFunc` 按目标/来源物理表筛选，约束 Lookup 投影的记录读取；不把它宣称为整个 QueryPort 的计数、分组、标签等所有 SQL 总数。测试逐格验证值、来源 ID/字段 ID、原关系 ID 和 DisplayField 标签，并验证来源批次读取后取消。另有真实 PB 稀疏四跳回归：1 或 16 行、每行两个同路径字段，均为四次目标读取；分页边界、重复顺序与读取预算另有断言。

## 验证记录

以下 Go 命令均在 `sidecar/` 执行，使用已有 Go 1.27.0、w64devkit、模块/构建缓存，`GOFLAGS=-mod=readonly`、`CGO_ENABLED=1`。日志目录为 `build/qa/lookup-page-projection/`。

| 命令 | 结果与日志 |
| --- | --- |
| `go test ./internal/lookup ./internal/relation ./tests/integration -run 'Test(Lookup\|CalculateCellsBatch\|CalculateFieldPage\|Batch)' -count=1` | 首次聚焦通过：1.188 / 0.734 / 1.986 秒；`focused-green.log` |
| `go test -race ./tests/integration -run 'Test(Lookup\|QueryRelationDisplay\|FormulaRelationFanout\|FormulaDereferencesValidatedRelation)' -count=1 -v` | 通过，40.745 秒；`integration-race.log`。包含既有超过一万来源分页、多跳终端分页、mutation 物化、Formula fanout/dereference、关系标签 freshness/presence 与实际查询次数 |
| `go test -race ./internal/lookup ./internal/relation ./internal/query -count=1` | 首轮退出 1；relation 1.692 秒、query 1.601 秒通过，Lookup 的新增测试 fixture 失败，见下文；`core-race.log` |
| `go test -race ./internal/lookup -count=1` | 修正测试 fixture 后完整 Lookup 包通过，4.010 秒；`lookup-race-final.log` |
| `go test -race ./tests/integration -run '^TestLookupFullMaterializationKeepsValuesBeyondGridPage$' -count=1 -v` | 新增精准真实 PB 回归通过，6.697 秒；`materialization-race-final.log`。完整物化保留 200 个值，Grid 返回 100 个来源、总数 200 且仍有后页 |
| `go vet ./internal/lookup ./internal/relation ./internal/query ./tests/integration` | 最终退出 0，无输出；`vet-final.log` 为命令/退出码观测记录 |

七个 Go 文件的 `gofmt -l` 无输出，`git diff --check` 退出 0。

首轮 `core-race.log` 的失败来自新增 `materialization_keeps_values_beyond_grid_page` 子测试：它复用了仅提供内存 schema 的游标 fixture，而完整 `Calculate` 会权威读取目标 schema，因而返回 `mutation.lookup.schema_invalid: lookup target schema is unavailable`。已将该边界回归移到同范围集成测试中的真实持久化 schema，未修改生产来适配测试。原失败日志保留；大范围 40.745 秒通过结果未盲目重跑，后续只运行受测试修正影响的 Lookup 全包与新增精准集成回归。

本地证据覆盖上述具体页面、路径与错误边界，不代表 100k 数据规模、全部 GUI 场景或完整产品 CI 已通过。后续 main 同步、产品构建与远端资格单独记录。
