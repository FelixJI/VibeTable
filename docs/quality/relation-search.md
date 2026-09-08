# relation.searchTargets 的单方法 Go owner 资格

本次只迁移 `relation.searchTargets`。Go Product adapter 复用既有
`relation.Service.SearchTargets`，PocketBase 仍是数据权威。Host 根据生成清单选择
既有 Go forwarder，保留工作区租约、取消、迟到结果拒绝及原 Relation renderer 错误映射。
其他 relation 写方法和 lookup 方法不随本次切换。没有更改 CI、发布门禁或不支持格式的
零写入拒绝契约。

## 原行为与删除范围

原始生产者为 `8cf989c5873830d28d61067a9afb82ddb06db124`，冻结原件为
`contracts/v2/relation-search-python-oracle.json` 的30例。22例由 Go HTTP adapter
完整比较公开 wire；8例单独标明 Go DTO 或旧 Python HTTP transport 不能表达的边界。
不把脚本化成功等同于真实领域接受。真实 Product 不执行 `relation_admin` 参数/结果模型。

Go 保留省略 query/offset/limit 的默认值、显式空字符串拒绝、原始空白 query、Unicode、
两层1 MiB预算及 collection/itemId/label/total 投影；secondaryLabel 与 snapshot 不公开。
真实 relation 服务仍拥有关系解析、搜索和分页限制。本次删除 Python 的专属 handler
及其注册；保留其他 Relation/Lookup 实现与公共参数契约。capture 的 `--write` 已退役，
默认检查固定生产者和历史输入，不再调用已删除的 Python owner，不重写原 JSON。

## 本地验证记录

以下是迁移提交 `b7aad489` 的相关证据；随后同步 `query.view` 的 main 增量需要独立复核，
不能把本节旧端点结果当成最终合并端点的完整通过。

- `uv run --frozen --no-sync python -m pytest tests/contract/test_relation_search_python_oracle.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/backend/test_main_product_data.py -q --no-cov`：52 passed。首次为50 passed/2 failed，旧 owner 集合和数量断言修正后通过。
- `dotnet test desktop/VibeTable.Desktop.sln --configuration Release --no-restore`：1201 passed/2 failed/1既有 skipped。两处失败为旧 manifest owner 清单及 Relation 白名单测试缺失 Go workspace scope；修正后对 `ProductRpcCapabilityManifestTests`、`WebMessageRouterTests`、`ProductRpcRouteSelectorTests`、`ProductDataSidecarRoutingTests`、`WorkspaceSessionEnvelopeFilterTests` 运行聚焦 `--filter`：93 passed。未宣称最终完整 solution 已重跑全绿。
- Go 聚焦 race：app 25.896s、productrpc 1.832s、productcapabilities 1.266s passed。包括原语料、真实 PocketBase 目标查询/分页/Unicode/limit边界、交叉 HTTP 注册及不可调用 guard。
- `go test ./cmd/vibetable-pb -run TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart -count=1`：passed 1.722s。相关 app/productrpc/productcapabilities/cmd 四包 `go vet` passed。
- 相关 Ruff、Pyright（0 errors）、锁定 Go format、`product_rpc_capability_policy.py --check` 及正常提交 hooks passed。能力索引由 `scripts/generate_product_e2e_capability_index.py --write` 更新/确认。
- 独立 Standards 与 Spec 审查均为0 findings，包含 S06 代码增量；源码审查不代替产品运行。

复用已有 uv 环境、Go/CGO 缓存、锁定 .NET SDK 与 NuGet 缓存；不改 lock 或 CI 的依赖来源。
全局门禁仍由精确 PR head 的 fresh `required` check 验证。

## 真实产品场景

既有 `06-relation-fanout` 保留 cascade 计划断言，并增加51个目标与一条未关联源记录。
从真实 grid 关系单元格打开编辑器，验证首屏50/51、续页51个不重复候选、Unicode 搜索、
无匹配空态，以及清空搜索恢复首屏。只搜索候选，不点击候选或创建目标来混入写 owner。
宿主取消与迟到响应另由工作区租约测试验证。

最新源码 `1ca00c0e4341d1d2abb4886f4c12a6c8f3d4f92d` 的全组件构建成功。
真实 S06 最终1/1 passed（QA `20260908T032325Z`），四组件新鲜度与 package audit通过，
宿主退出码0、members/descendants为空、portsReleased=true、lease及final cleanup通过。
脚本首跑 `20260908T031801Z` 重复关闭已关闭的字段面板超时；删除多余调用后，
`20260908T032015Z` 的trace证明选择器命中列头，补 `.tabulator-cell` 限定后通过。
两次均为脚本定位修正，保留失败报告，使用同一个产品包，未重建、未增加超时。
最终 PR CI 与 squash 后 main CI/CD仍pending。0.5.0/N-1兼容不属于当前开发验收；现有发布与格式拒绝契约保留。

## 同步 main 的增量验证

已正常合入 `6ed36810f3753caed5e2e8ca27a4d4ad2117d41d`（已合并的 query.view），
最终清单为10 Go /90 Python /2 native。双方生产 adapter 和原始语料保持与来源一致，
HTTP fixture 使用交叉不可调用 guard，S02 分组和 S06 搜索场景均保留。

同步增量：相关 Python 60 passed（首次59 passed/1旧数量断言失败，修正后通过）；
Host 定向87 passed；Go app/productrpc/productcapabilities race 分别32.452/1.686/1.260s
passed；既有进程测试1.717s passed。策略生成检查、相关 Ruff 和 Go vet passed。
独立 Standards 与 Spec 增量均0 findings。实际 S06结果见上节；fresh CI仍为合并门禁。
