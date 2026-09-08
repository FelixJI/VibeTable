# query.validateSnapshot Go Product 资格

本变更完成公开 query.validateSnapshot 的 Go 读取路径，并退役 Python handler。app 注册复用现有 queryPort；不修改快照摘要、revision、错误格式或 PocketBase 权威。Host 补齐闭合参数 registry，scope、epoch 和取消沿既有 Product 通道。独立增量为 18 Go / 82 Python / 2 Host；最终与字段描述合并后的统计和产品资格另行记录。

## 原件与边界

原 Python producer 为 2f02bfcb8afdda46fa003d6c546d2ff2a8910aae；6e0dab1850c9f4f53cbae7595c01d599f95ee361 保存真实捕获实现。30 例原件与旧 query-read 23 例 JSON 不变。历史 capture/write 已关闭，当前默认/check 是静态一致性检查，不冒用旧 producer 重新捕获。Go 完整重放其中 20 个 typed 可表达例；其余 10 例各自记录边界，不能声称全部 Go 回放。真实 HTTP 另覆盖 query.page 产生的快照、有效及三种失效结果、精确 query.snapshot.invalid 错误、权威状态零写入。

## 本地验证记录

复用已有 uv、Go、Node、NuGet 缓存。下列是独立增量结果，组合端点仍须重新验证相关集成并完成 fresh CI。

- `go test -race ./internal/app -run 'TestQueryValidateSnapshotProduct(Params|TypedBoundary|ResultAndInputs|ErrorsAndCancellation)$' -count=1`：PASS 2.123s；`go vet ./internal/app` PASS。
- `go test -race ./internal/app -run 'TestQueryValidateSnapshotProductHTTP' -count=1`：首次仅原件路径多一级导致失败，真实 PocketBase 用例未报失败。修正后定向 `TestQueryValidateSnapshotProductHTTPMatchesOriginalPythonWire` PASS 4.747s。
- `go test -race ./internal/contracts/productcapabilities ./internal/productrpc -count=1`：PASS 1.255s / 1.702s。
- `go test -race ./cmd/vibetable-pb -run TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart -count=1`：PASS 10.306s，包含实际生产端点的精确领域错误。
- Python 相关集合首次 134 PASS / 1 FAIL（owner 预期漏项）；修正后 `uv run --frozen --no-sync pytest --no-cov tests/contract/test_product_rpc_capability_policy.py -q`：7 PASS。Ruff、聚焦 Pyright 与三个正式生成器 check PASS。扩大到旧 backend 测试的 Pyright 有 31 项，基线逐项相同，不将此扩大检查称为成功。
- Host `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --artifacts-path build/dotnet --filter 'FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~ProductDataSidecarRoutingTests|FullyQualifiedName~WorkspaceSessionEnvelopeFilterTests'`：137 PASS / 0 FAIL / 0 SKIP。首次暴露缺 registry 端点，之后修正测试层级，所有失败日志保留。
- Host 额外 registry/controller/gateway 集合 21 PASS / 1 FAIL（精确端点清单缺项）；修正后受影响 registry 用例 1 PASS。
- `uv run --frozen --no-sync pytest tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q`：150 个断言通过，但因本次未使用聚焦测试的 `--no-cov`，全后端覆盖率门禁退出 1；不能记为整命令通过，完整覆盖率验收仍待组合端点质量入口。此前尝试改变 S04 声明的 149 PASS / 1 FAIL 保留，已恢复 S04，新增 S30。
- Node `--check tests/e2e/webview_product_scenarios.mjs` 语法检查通过。

日志保留在本工作树 build/query-validate-snapshot-*、query-snapshot-*、snapshot-owner-*、snapshot-host-*。独立 Standards 与 Spec 对 Go、Python、Host 以及 S30 增量审查均无未解决项；S30 审查发现的多余关闭抽屉已删除，schema revision 使用变化比较。

## 产品与交付边界

S30 使用真实 Product bridge：page 快照、省略/传入 currentQuery 的有效结果、query_changed、真实 mutation 后 application_write、字段变更后 schema_changed，每次校验前后比较 rows 和两种 revision。历史性能报告 source/run 不变，新增场景仅列 manifest gap。

最终组合产品构建与 S02/S03/S30 实际执行已通过，见下方记录；fresh PR CI、squash 及合并后 CI/CD 尚待执行；上述单层测试不能替代这些资格。0.5.0/N-1 不作为当前开发验收，不支持格式的零写入拒绝与原 CI 门禁保持不变。

## 与字段描述的组合验证

合入字段描述增量后，正式生成表为 19 Go / 82 Python / 2 Host，共 103。两个历史 JSON 原件不变，双方 HTTP 测试夹具均补齐对方的 fail-on-call guard。组合定向 Python/runner/index 共 289 PASS（11.38s，--no-cov 仅聚焦集合，未变更覆盖率配置）；Go -race 注册、dispatcher、两个端点与实际进程分别 PASS 1.297s / 1.765s / 15.880s / 10.616s；三冲突 Host 类 107 PASS（348ms）。日志为 build/schema-read-combined-{python,go}.log 与 build/field-snapshot-merge-host.log。组合实际产品资格见下方最新记录。

## 19-owner 实际产品与完整质量记录

固定源码 `4ee30a130e89eae05e032387fb94be45873856f5` 的
`uv run --frozen --no-sync python scripts/build_next.py` EXIT0，完整构建全部组件，没有 skip。
复用已有 uv/Go/Node/.NET/NuGet 缓存；三个缺失的插件依赖通过 `npm ci --offline` 各安装一个锁定包，未改依赖或 lock。

`uv run --frozen --no-sync python -m tests.e2e.product_e2e_runner --scenario 02-all-field-schema --scenario 03-schema-errors --scenario 30-query-snapshot-validation`
在真实 WPF/WebView2 通过 3/3，0 failed、0 skipped。
报告 `build/qa/product-e2e/20260908T135305Z/product-e2e-report.json`；S02 13754ms/18断言、S03 7425ms/11断言、S30 4253ms/18断言。
包审计 errors=[]，四组件均 fresh；三场景 pageErrors、bridge failures/pending 均空；Host exit 0，成员与后代为空、端口释放、owner lease 与最终清理全部通过。

S30五次校验均为实际 page 快照：省略 currentQuery 和传入同查询均 valid；
query_changed 请求 `e2e-be1c5071-9b37-4d33-94f6-2c8606d3bb5c` 保持 data=1/schema_0002；
application_write 请求 `e2e-81227063-9c5f-4bf3-afea-d31ca0af9f04` 为 data=2/schema_0002；
schema_changed 请求 `e2e-6b8516b2-34a8-4f07-95b0-f2b52a22f65e` 为 data=2/schema_0003。
每次校验前后 rows 与 revision 相同。日志 `build/schema-read-combined-product-{build,s02-s03-s30}.log`。

正式 `scripts/automation_project.py quality` 的分段结果必须与产品资格区分：

- contracts Python245/Web78、Ruff、backend Pyright0/mypy80文件全部通过；完整 Python1784 PASS/1 SKIP，覆盖90.88%；Web174文件/1430测试及typecheck/build、三个插件项目检查通过。
- 初次默认.NET obj缺 restore 元数据，复用NuGet按 locked mode补齐；第二次缺插件tsc，离线补齐后严格按quality原函数顺序继续剩余步骤，没有重跑已通过的Python/Web，也未删减门禁。
- Go format/vet通过；`qa/next.py --stage go-test` 未通过。原脚本的三次有限重试均遇 Windows TempDir RemoveAll目录非空（history及既有workspacev2测试）；未出现业务断言失败。期间主代理提交纯文档也触发release identity changed保护，是独立操作顺序错误，不能作为清理失败根因。之后源码固定。
- 旧纯SQLite WAL/DELETE诊断曾保存同型清理红例，不证明外部watcher根因。本次复用旧harness、匹配QA TEMP/TMP作5组对照共10子测试全部通过；未稳定复现、未宣称修复，不增加重试或变更门禁。
- 独立继续 `uv run --frozen --no-sync python qa/next.py --stage dotnet` EXIT0：六项目合计1272 PASS/1 SKIP，全部既有覆盖率门禁通过，Desktop line70.61%/branch60.79%。最后编译器占用导致QA临时证据清理警告，保留现场。
- Go coverage 阶段及该quality入口的额外 sidecar build因前序失败未执行；完整产品构建的sidecar已成功。完整本地quality不能记为PASS，最终完整fresh CI仍为合并门禁。

完整日志：`build/schema-read-combined-quality.log`、`build/schema-read-combined-quality-restored.log`、`build/schema-read-combined-quality-remaining.log`、`build/schema-read-combined-dotnet.log`。本地资格不代表最终PR或main验收完成。
