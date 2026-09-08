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

最终组合产品构建、S02/S03/S30 实际执行、fresh PR CI、squash 及合并后 CI/CD 均尚待执行；上述单层测试不能替代这些资格。0.5.0/N-1 不作为当前开发验收，不支持格式的零写入拒绝与原 CI 门禁保持不变。
