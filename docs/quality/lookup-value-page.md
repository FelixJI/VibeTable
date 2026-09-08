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
与无Python fallback。所有14项注册和HTTP交叉guard保持fail closed。真实authority测试建立
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

实际S29包、最新main增量、最终fresh CI及合并后main CI/CD尚未完成，不提前声明资格通过。

交叉注册增量：sidecar目录执行 `go test -race ./internal/app -run 'TestQuery.*ProductHTTP|TestSchemaListProductHTTPMatchesRealCatalogREST|TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities|TestHistoryReadProductHTTPReturnsFreshAuditedPage' -count=1`，passed61.705s。此项只覆盖本次受影响的既有HTTP夹具，未重复已通过的Lookup测试。
