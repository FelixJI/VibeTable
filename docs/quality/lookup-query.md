# lookup.query Go owner 迁移资格

本切片只把 `lookup.query` 从 Python 迁到已有 Go/PocketBase authority；公开方法、八字段参数、workspace/epoch scope 和单一 owner 保持闭集。Python 专属 handler、grouped-view client/result 与注册同时退役。共享 `query_lookups` 仍被 relation export 消费，必须保留；`lookup.valuePage` 独立迁移，不在本切片扩大 owner。当前工作树为 16 Go / 84 Python / 2 Host，最终以生成清单为准。

依赖 PR #291 的 Lookup 面板及精确只读 POST 边界已正常合入本地，用于真实 S29；它们须先独立进入 main，最终本 PR 不重复包含依赖意图。owner 迁移保持单独可回滚。此处未修改 CI、0.5.0/N-1 验收政策或不支持格式零写入拒绝契约。

## 语义与证据边界

固定 Python producer `6e25fd033697c57a4ca113caf98c90293b892548`，冻结提交 `78f48855c3b7fbf73a545abd53494317ae895763`（本地迁移 cherry-pick 为 `3653d98028c3c8a45cd1f30ed3917a4fdc1a1689`）。39 例原 JSON 不变，当前捕获器退役后只核对独立输入，不能用 Go 输出重写期望响应。

Go 测试完整 HTTP 比对 10 例；另外 29 例逐项说明 typed catalog/query/snapshot、畸形响应及 transport 表达边界，不是跳过所有动态数据的豁免。第二跳公开领域错误另有可表达 typed 输入的 HTTP 补充回放，不能冒充原案例全形回放。动态 Unicode 行与双级父节点首次出现顺序使用原 Python 独立追加的两例。真实 PocketBase 测试覆盖分页、单级分组，以及成功/失败前后业务行、metadata、audit、outbox、receipt 不变。

参数先作闭字段、类型、Unicode、递归凭据键、深度与语义预算校验；groups 容器先于 catalog，revision 先于 group 成员，未知 fieldRefs 在 query 之后失败。所有有效 physicalName 的 lookup 均参与列转换，重复名称最后一个生效，请求 fieldRefs 顺序和重复保留。沿用 REST 的 TableQuery 解码和既有领域端口，不增加 HTTP 自调用、authority 或 Python fallback。既有双级 parentSummaries 缺失候选不在本 PR 修复。

Host 测试使用完整八字段请求，验证生成 owner、缺失/旧 scope 拒绝、wire/payload 保持、公开错误、epoch 取消及迟到响应不成功。Python drain 测试用仍属 Python 的 field.settings.describe 保留全部原 drain 断言。作者以外的主代理已独立复核这五个 Host 文件，未发现 Spec 问题。

S29 通过真实 WebView2 选择源表渲染 Lookup、点击来源面板并从 100 条追加至 101 条 Unicode 来源；新增诊断窗口排除选表前已有请求、往返和在途请求的 requestId，要求本次选表后有成功 lookup.query 请求/响应；不依赖长度切片，避免有界诊断队列满 200 条后移除首项导致假失败。保留来源唯一、耗尽 footer、无错误与业务行/revision 不变断言。HTTP 测试不代替实际构建物上的 S29。

## 已执行验证与失败历史

- Go 新适配器聚焦测试首次因 groupLimit 的内部数值表示不匹配失败，修正为 json.Number，并为动态父键增加 null/bool/number/object 回归。随后聚焦 race 首次只因真实领域 cell state 为 ok 而测试写成 valid 失败；按既有领域修正断言后，仅重跑受影响的原件 HTTP/真实 authority 两项，通过（12.186s）；Go app vet 通过。
- 注册/生成能力/真实 sidecar 进程 race 契约分别通过（1.672s、1.266s、11.543s）。20 个变更 Go 文件 gofmt 通过。
- Python 相关集首次 133 passed / 1 failed（1.99s），唯一失败是注册测试期望集合遗漏迁移的 lookup.query；修正两处集合后该项 1 passed（0.48s），未重跑全组求绿。
- 八类 Host 聚焦测试 151 passed / 0 failed / 0 skipped（13s），TRX 为 build/host-tests/lookup-query-host.trx；复用已缓存 .NET 10.0.400，locked restore/test 共用 build/dotnet。
- Ruff check / format 通过（13 个 Python 文件）；完整 backend 加冻结保留器/测试 Pyright 0 errors；policy、product E2E index 和保留原件 --check 均通过。首次 Pyright 未解析共享环境失败，随后指定 --pythonpath 仍受项目 venvPath 约束；最终通过 --venvpath 指向已有环境，不修改配置或安装依赖。
- 交叉 HTTP 首次 build failed，根因是注册 guard 被错误插入两处普通语句；修正为 mux 注册列表唯一一行，旧日志 build/lookup-query-cross-http.log 保留，修正后交叉 HTTP race 实际测试通过（125.524s），新日志 build/lookup-query-cross-http-corrected.log 保留。更早 direct preview 测试误插入已撤销且该文件无 diff。

独立 Standards 当前无必须修复项；P3 建议合并与 query.view 重复的原始 Unicode 扫描器，本切片接受局部重复，避免把另一 decoder 的行为改动混入 owner 切换。独立 Spec 的 S06 场景绑定问题已修正为 S29 并复核代码部分；产品资格仍待实际运行。

## 尚未完成

实际 Go 构建物 S29 及最终文档/增量双轴审查 pending。runner/index 150 passed（21.08s），日志 build/lookup-query-runner-index.log。当前未提交本地 owner 变更、未开 PR。最终严格同步 main 的 fresh CI、squash merge 与合并后 CI/CD 均 pending；上述局部通过不能称完整交付。

相关精确执行入口（uv 使用已有环境，PYTHONPATH 显式绑定本工作树）：

```text
go test -race ./internal/app -run '^TestLookupQueryProductHTTP(ReplaysFrozenPython|UsesRealAuthorityWithoutWrites)$' -count=1
go test -race ./internal/app -run 'TestRelationSearchProductHTTP|TestRelationPreviewProductHTTP|TestQuery.*ProductHTTP|TestSchemaListProductHTTPMatchesRealCatalogREST|TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities|TestHistoryReadProductHTTPReturnsFreshAuditedPage' -count=1
go test -race ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb -run 'TestNewRequiresRegistrations|TestGenerated|^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1
uv run --frozen --no-sync python -m pytest tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc.py tests/backend/adapters/test_pocketbase_client.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/backend/adapters/test_pocketbase_relation_io.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_lookup_query_python_oracle.py -q --no-cov
uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q --no-cov
```

上述 Python 相关集对应保留的首次 133 passed / 1 failed；修正后仅 `tests/backend/test_main_product_data.py::test_product_rpc_registration_is_closed_and_provider_neutral` 定向通过，未将首次失败改写为整体全绿。