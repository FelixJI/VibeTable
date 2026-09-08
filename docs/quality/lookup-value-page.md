# Lookup 来源分页迁移资格

本切片仅将 `lookup.valuePage` 从 Python BFF 转译迁至现有 Go relation authority。
初始实施基线为 main `a19ccd5366d62be6338f628d06b6c5a37484f20f`，清单为14 Go /86 Python /2 native；
在该基线的分支中 search/preview 仍归 Python。后续最新 main 同步与最终包资格单独记录，
不能把本地来源结果当成另一端点的通过证据。

## 原始契约与实施边界

原 Python producer 为 a19；冻结提交 `e01cf0cacd73a4a83e9b63ab3da51606841b1dfb`，
本实施分支承接为 `9f3ea9ff6ce5533dfe29f87df7e6294bf520f5c1`。39个独立输入、完整公开输出与
authority HTTP请求顺序的JSON保持不变。Go完整Product HTTP回放26项；另13项逐个说明
固定Catalog/CellValue结构或旧Python transport无法表达的边界，不将动态value一并豁免。

八个字段全部必需，闭字段、严格文本/整数、Unicode scalar、凭据与紧凑UTF-8预算沿用原契约。
先Describe目录，检查schema/permission/lookup revision，按physicalName找首个字段，
再校验offset/limit，最后取稳定fieldId并调用既有LookupValuePage。保留两跳各自的错误族、
Product和旧REST两个1 MiB预算，以及大整数到旧REST int边界的错误位置。复用现有公开revision
算法，不新增文件摘要或新的authority。Python专属handler/client和路径常量删除，共享lookup.query
与revision helper保留；capture/write入口退役，只读检查保留producer、输入和typed边界。

Host复用已有Go forwarder与epoch lease，保留完整八字段payload、scope、取消、迟到成功/错误
与无Python fallback。来源分支14项注册和HTTP交叉guard保持fail closed。真实authority测试建立
101条来源，验证前100/末1两页及total/hasMore；成功、过期revision、非法paging和缺失来源拒绝
后逐次比较记录、metadata、audit/outbox/receipt，未发生写入。

## 来源分支本地验证

- `go test -race ./internal/app -run '^TestLookupValuePageProduct' -count=1`：passed10.116s。
- `go test -race ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb -run 'TestNewRequiresRegistrations|TestGenerated|^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1`：passed1.676/1.270/10.258s。
- `uv run --frozen --no-sync python -m pytest tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc.py tests/backend/adapters/test_pocketbase_client.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_lookup_value_page_python_oracle.py -q --no-cov`：91 passed1.31s。
- `dotnet test desktop/VibeTable.Desktop.sln --configuration Release --no-restore`：1208 passed、0 failed、1个已有ActivationPointerLinkIsRejectedAndRetained skip；本工作树仅一次locked restore并复用缓存。
- `uv run --frozen --no-sync python -m pytest tests/contract/test_product_e2e_capability_index.py tests/e2e/test_product_e2e_runner.py -q --no-cov`：150 passed7.02s。
- `uv run --frozen --no-sync pyright backend contracts/v2/generate_lookup_value_page_oracle.py tests/contract/test_lookup_value_page_python_oracle.py`：0 errors/warnings。相关Ruff、Go格式、app vet、policy与保留原件check通过。

历史失败保留：首次direct测试发现typed struct直接交给只处理decoded JSON的预算编码器，
导致REST预算未正确测量；改为显式六字段map与json.Number，最终race回归通过。一次后续编译
被共享fixture换guard留下的两个unused query imports阻塞，移除后通过。首次生成器拒绝新group
非规范顺序，按现有ordinal规则调整后生成成功。未通过降低门禁或重复完整测试组取得绿灯。

独立Spec源码初审0项；Standards为0硬违反、1非阻断Duplicated Code判断：原始JSON字符串扫描
与已有多个Query decoder同形。本切片接受该局部重复并保持其他已冻结decoder生产代码不变，
不在单方法owner迁移中混入跨多个decoder的共享校验重构；目前没有该扫描导致错误的复现证据。

## 产品资格状态

新增独立S29 `29-lookup-source-pagination`：真实字段规划/既有mutation建立101条来源，
打开面板核对首100条，点击现有footer“加载更多”后核对101条唯一Unicode来源及分页耗尽，
比较两表记录及schema/data revision不变。打开面板本身不调用valuePage，不能据此宣称分页通过。
历史S06/S26元数据及main 23/23来源报告保持原义，新场景缺口由现有证据契约记录。

当前 Go owner 组合的成功 S29 包资格、最终fresh CI及合并后main CI/CD尚未完成，不提前声明通过。

交叉注册增量：sidecar目录执行 `go test -race ./internal/app -run 'TestQuery.*ProductHTTP|TestSchemaListProductHTTPMatchesRealCatalogREST|TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities|TestHistoryReadProductHTTPReturnsFreshAuditedPage' -count=1`，passed61.705s。此项只覆盖本次受影响的既有HTTP夹具，未重复已通过的Lookup测试。

## 整合 main 3f665 的增量

来源提交 `f32290357d55dd5fd66b0378b197262e02e1b28f` 承接 main
`3f665176e16e00d168edba98b40ac2ae26174c24` 的 preview 迁移。当前为15 Go /85 Python /2 native，
lookup.valuePage 与 relation.previewDelta 都归 Go，relation.searchTargets 仍归 Python。
两方法生产 adapter 与原始39/37案例保持来源一致；15项严格注册完整，两类真实HTTP fixture
互加不可调用guard。Host保留 lookup 八字段与 preview 六字段，以及两个方法各自的错误、
epoch取消/迟到结果断言；仍属Python的search及field.settings.describe入口保持合法。
S26/S28/S29并存，原main23场景报告的source/run不变，历史manifest gap明确为三项。

本次以下命令使用既有锁定环境及缓存，全部日志前缀为 `lookup-value-page-main-sync-`：

- `uv run --frozen --no-sync python -m pytest tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc.py tests/backend/adapters/test_pocketbase_client.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_lookup_value_page_python_oracle.py tests/contract/test_relation_preview_python_oracle.py tests/contract/test_product_e2e_capability_index.py tests/e2e/test_product_e2e_runner.py -q --no-cov`：248 passed，7.26s。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~ProductDataSidecarRoutingTests|FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~RelationLookupRpcRegistryTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~WorkspaceSessionEnvelopeFilterTests|FullyQualifiedName~HostProductRpcCompositionTests|FullyQualifiedName~QueryCursorOwnerCompositionTests'`：141 passed，0 failed/skip，14s。未重复来源完整.NET矩阵。
- sidecar目录：`go test -race ./internal/app ./internal/productrpc ./internal/contracts/productcapabilities -run 'TestLookupValuePageProductHTTP|TestRelationPreviewProductHTTP|TestQuery.*ProductHTTP|TestSchemaListProductHTTPMatchesRealCatalogREST|TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities|TestHistoryReadProductHTTPReturnsFreshAuditedPage|TestNewRequiresRegistrations|TestGenerated' -count=1`：app77.513s、dispatcher1.668s、capabilities1.260s，全部passed。
- `uv run --frozen --no-sync pyright backend contracts/v2/generate_lookup_value_page_oracle.py contracts/v2/generate_relation_preview_oracle.py tests/contract/test_lookup_value_page_python_oracle.py tests/contract/test_relation_preview_python_oracle.py`：0 errors/warnings。
- sidecar目录：`go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1`：passed10.201s；`go vet ./internal/app ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb`：exit0。
- 相关九个Python文件Ruff check/format check通过；policy与E2E索引生成一致性检查通过，Go测试文件gofmt、Git diff空白检查通过。

本轮生成器首次误传不支持的 `--write`，仅usage拒绝，未运行生成；按实际默认写入模式生成成功。
尚待更新后产品构建/S29与最终fresh CI，来源包和旧完整矩阵不充当新端点通过证据。

## UI 与只读边界依赖组合

Go owner 来源 `3b477257` 的实际S29 `20260908T060700Z` 失败：加载更多按钮在视口外，
没有完成分页。UI来源 `e74d95c3f338307122ef79a10878f79a8f9fde35` 的实际S29
`20260908T063000Z` 已能普通点击按钮，但Python owner的后续分页RPC失败；此时未取得第101条。
独立只读边界回归随后用真实中间件捕获旧POST 423，并以精确路径许可修复。两次产品失败均保留。

UI与边界组合来源 `7092f0d35e221c94e00cb0878e82ed7f47a00f8d` 的实际S29
`20260908T065613Z` 为1/1 passed，普通点击完成100→101条唯一Unicode来源、分页耗尽及
两表记录/revision不变。包审计、四组件新鲜度、Host退出0、成员/后代为空、端口释放和清理通过。
**该来源的lookup.valuePage仍归Python，不是本Go owner端点的产品资格。**

当前在 `3b477257` 正常合入依赖 `1a4160193ae6db3ba19c8177340ec9986a2ee308`，
仅承接UI布局、精确只读POST边界、相关测试及资格文档/截图，共八个依赖文件，均与依赖来源一致。
清单仍为15 Go /85 Python /2 native；lookup生产adapter、39原始案例、owner接线及S29脚本未改。
当前组合新包/S29和fresh CI尚待执行，未复用Python owner的通过结论。

本次组合聚焦验证：sidecar目录执行
`go test -race ./internal/app -run '^TestWorkspaceV2(LookupValuePage|WriteBoundary|WriteRejection)' -count=1`，
passed7.750s（`lookup-dependency-boundary-race.log`）；
`uv run --frozen --no-sync python -m pytest tests/contract/test_product_e2e_capability_index.py tests/e2e/test_product_e2e_runner.py -q --no-cov`，
150 passed8.59s（`lookup-dependency-runner.log`）。policy与E2E索引生成一致性检查通过。
未重复此前全部Go/.NET测试，未在本次检查中构建产品包。
