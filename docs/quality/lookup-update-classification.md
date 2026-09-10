# Lookup 配置更新分类资格

## 修复范围

基线为 fresh `GitHub/main` 的 `cadf51533ed45c3793c8f75b21025375d4d3f633`。字段计划的分类逻辑未比较 `LookupSpec`，因此单独更改 `TargetFieldID` 或 `Path` 时，在进入目标校验前即错误返回 `field.change.noop`。

生产改动仅 `sidecar/internal/fieldchange/planner.go` 一行：将 Lookup 配置差异归入既有 `ClassSchema`。继续使用原权威事务、schema revision、Lookup 元数据与依赖投影，不引入 migration、图校验或新的执行接口。不依赖尚未合并的混合环校验或公式/Lookup 组合候选。

相邻 `planner_test.go` 通过公开 `Planner.Plan` 验证仅目标变化、仅路径变化均产生可应用的 schema 计划，字段身份不变、不创建 migration；目标和路径完全不变时仍精确返回 `field.change.noop`。测试的 before/draft 使用独立 LookupSpec，避免指针共享掩盖改动。

真实 PocketBase 回归位于 `sidecar/tests/integration/field_computed_v2_test.go`：创建两个指向同一目标表的关系，查询初始值 `Title 1`；只改目标字段后读取 `CODE-1`，再只改关系路径后读取 `CODE-2`。每次均通过公开字段计划/Apply 和 `relation.Service.QueryLookups`，并验证稳定字段身份、schema revision 推进、Lookup metadata revision 从 1 到 2 到 3、目标字段/完整路径/首跳 relation ID，以及唯一的当前外部依赖及其 definition version。不存在旧目标或路径依赖残留。

同一真实存储回归还验证不变配置仍为 noop；不存在的目标字段被原 `field.lookup.target_invalid` / `draft.lookup.targetFieldId` 诊断拒绝，已保存配置及查询结果继续保持 `CODE-2`。

## 验证结果

所有命令在 `sidecar/` 执行，复用既有 Go 1.27.0、w64devkit、模块和构建缓存，`GOFLAGS=-mod=readonly`、`CGO_ENABLED=1`。日志均位于 `build/qa/lookup-update-classification/`。

| 日志 | 命令与结果 |
| --- | --- |
| `planner-red.log` | `go test ./internal/fieldchange -run '^TestLookupConfigurationUpdateIsSchemaChange$' -count=1 -v`：旧生产实现 FAIL，0.784s；target_only 和 path_only 均错误返回 `field.change.noop`，unchanged 子例 PASS。 |
| `focused-green.log` | `go test ./internal/fieldchange ./tests/integration -run '^TestLookupConfigurationUpdate(IsSchemaChange\|PersistsAndChangesQuery)$' -count=1 -v`：PASS，分别 0.751s、1.033s。 |
| `core-race.log` | `go test -race ./internal/fieldchange ./internal/schemaapi ./internal/relation ./internal/lookup -count=1`：EXIT 1。fieldchange 32.014s、relation 1.764s、lookup 1.809s PASS；schemaapi 4.168s FAIL，见下文。 |
| `integration-race.log` | `go test -race ./tests/integration -run '^Test(LookupConfigurationUpdatePersistsAndChangesQuery\|FormulaAndLookupCreateThroughFieldChangeV2\|FormulaPlanAcceptsFreshNumberPhysicalNameTimesFloatLiteral)$' -count=1 -v`：三项 PASS，27.429s。 |
| `vet.log` | `go vet ./internal/fieldchange ./internal/schemaapi ./internal/relation ./internal/lookup ./tests/integration`：EXIT 0，无诊断输出。 |

三个 Go 文件已运行 `gofmt`；`git diff --check` EXIT 0。

四包 race 的唯一失败是 `schemaapi.TestListIncludesNewlyCreatedEmptyTable`：`testing.go:1617: TempDir RemoveAll cleanup: unlinkat .../001: The directory is not empty`。保留原 FAIL 和日志，未重跑、未修改清理或断言；现有输出不足以确定目录清理失败的原因，不能由其他包 PASS 推导该运行通过。

尚未执行完整 Go 仓库测试、产品构建、实际产品 E2E 或本候选 fresh CI。聚焦普通测试、真实存储 race 与 vet 不代替这些门禁。
