# lookup.query Go owner 迁移资格

本切片只把 `lookup.query` 从 Python 迁到已有 Go/PocketBase authority；公开方法、八字段参数、workspace/epoch scope 和单一 owner 保持闭集。Python 专属 handler、grouped-view client/result 与注册同时退役。共享 `query_lookups` 仍被 relation export 消费，必须保留；`lookup.valuePage` 独立迁移，不在本切片扩大 owner。单独 query 实施端点 a74fcf9f 为 16 Go / 84 Python / 2 Host；现本地承接独立 valuePage 依赖 303e0869 后为 17 Go / 83 Python / 2 Host，最终以生成清单为准。

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

独立 Standards 当前无必须修复项；P3 建议合并与 query.view 重复的原始 Unicode 扫描器，本切片接受局部重复，避免把另一 decoder 的行为改动混入 owner 切换。独立 Spec 的 S06 场景绑定问题已修正为 S29 并复核代码部分；该轮审查时产品资格尚待实际运行，后续实际结果见下文。

## 尚未完成

本地实现已提交 523a806e2e82885ae252e493872673de8a3a1133，正常同步 main2c211088 后为 a74fcf9f0f033fdc17d7a31e339a60f5840550ed；增量仅主题探针，独立 Standards/Spec 均无新增问题。runner/index 150 passed（21.08s），日志 build/lookup-query-runner-index.log。尚未开 PR，须待依赖分页 PR291 先进入 main；最终资格文档增量复核待完成。最终严格同步 main 的 fresh CI、squash merge 与合并后 CI/CD 均 pending；上述局部通过不能称完整交付。

相关精确执行入口（uv 使用已有环境，PYTHONPATH 显式绑定本工作树）：

```text
go test -race ./internal/app -run '^TestLookupQueryProductHTTP(ReplaysFrozenPython|UsesRealAuthorityWithoutWrites)$' -count=1
go test -race ./internal/app -run 'TestRelationSearchProductHTTP|TestRelationPreviewProductHTTP|TestQuery.*ProductHTTP|TestSchemaListProductHTTPMatchesRealCatalogREST|TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities|TestHistoryReadProductHTTPReturnsFreshAuditedPage' -count=1
go test -race ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb -run 'TestNewRequiresRegistrations|TestGenerated|^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1
uv run --frozen --no-sync python -m pytest tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc.py tests/backend/adapters/test_pocketbase_client.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/backend/adapters/test_pocketbase_relation_io.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_lookup_query_python_oracle.py -q --no-cov
uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q --no-cov
```

上述 Python 相关集对应保留的首次 133 passed / 1 failed；修正后仅 `tests/backend/test_main_product_data.py::test_product_rpc_registration_is_closed_and_provider_neutral` 定向通过，未将首次失败改写为整体全绿。
## 实际 Go 构建物 S29

固定生产源码 `a74fcf9f0f033fdc17d7a31e339a60f5840550ed`：`uv run --frozen --no-sync python scripts/build_next.py` 完整所有组件构建 EXIT0，复用已有 uv、Node 24.19.0、Go 和 .NET 缓存，没有使用 skip 标志；日志 build/lookup-query-product-build.log。

`uv run --frozen --no-sync python -m tests.e2e.product_e2e_runner --scenario 29-lookup-source-pagination`：QA `20260908T085038Z` 为 1/1 passed、0 failed、0 skipped，8629ms。九项断言全部成功，包含选表后新 lookup.query 往返、普通点击来源 100→101、唯一 Unicode、耗尽、业务行/revision 不变。完整诊断中的两次 lookup.query 请求与响应类型匹配、code=null；bridge failures/pending、pageErrors 为空。包审计无错误、四组件 freshness 全部通过。lifecycle.hostExitCode=0、成员/后代列表为空、端口释放、owner lease 关闭和最终 cleanup 全通过，无剩余 PID。报告为 build/qa/product-e2e/20260908T085038Z/product-e2e-report.json。

最终 S29 诊断窗口修正已获独立双轴复核：按旧 requests/roundTrips/pending 的 requestId 排除既有请求，不依赖满200条后会滚动的长度切片。首次字符串替换因CRLF只替换定义，消费端遗漏由复审发现后完整纠正；另执行实际源码窗口片段，确认满队列接受新请求、拒绝旧在途完成，日志 build/lookup-query-window-check.log。此局部检查与实际产品报告分别保留。

正常提交的 Ruff format/check、version consistency、package contract hooks 均 passed。

## 串行 owner 依赖整合

已在 query 分支本地合入 valuePage 独立候选 303e08695b84973f018b22eef34bdf9aaf58a050，远端仍按分页 PR291 → valuePage → query 单独意图依次交付。冲突保留两个 Go registration 与双方完整八字段请求；生成能力和严格 process/dispatcher 顺序共17项。Python 两个专属路径同时移除，共享 relation export 的 query_lookups 与输入 revision helper 保留。两份39例原 JSON及两套Go生产投影均无改动；HTTP夹具仅补齐互补 registration。

整合 Python 注册/两原件/共享 export 契约142 passed（1.36s），日志 build/lookup-owners-python-integration.log；同八类 Host 测试160 passed、0 failed/skipped（17s），使用 --no-restore 和既有 build/dotnet，日志 build/lookup-owners-host.log、TRX build/host-tests/lookup-owners-host.trx。Go整合 race 单次全通过：HTTP156.449s、注册1.721/1.298s、真实sidecar进程12.197s，日志 build/lookup-query-value-page-merge-{http,registration,process}.log；Pyright 0 errors，Ruff及生成policy/index/双原件 --check 均通过。本整合已通过独立 Standards/Spec，五个 Host 作者文件由主代理单独 Spec 复核；无新增未解决问题，既有 P3 局部重复保持接受。前述16-owner S29成功不替代本整合端点，后续17-owner结果如下。

整合提交 `247a227059398aace7af2ec99b34de6e064db482` 正常四项 hooks 通过。对此精确生产源码再次执行完整 `uv run --frozen --no-sync python scripts/build_next.py`（EXIT0）与同一真实 S29 入口，报告 build/qa/product-e2e/20260908T091246Z/product-e2e-report.json：1/1 passed、0 failed/skipped、7988ms，九项断言全部成功。两次 lookup.query 和一次 lookup.valuePage 均成功往返（code=null），现在两者均按生成policy走Go。包审计/四组件freshness通过；bridge failures/pending、pageErrors为空，Host退出0、成员/后代/剩余PID为空、端口释放、owner lease/final cleanup全通过。日志 build/lookup-owners-product-build.log 和 build/lookup-owners-product-s29.log。此前16-owner报告仍保留，未因文档变更重复构建。

当前待完成的是依赖PR逐个合并、最终main同步及每个独立owner PR的fresh CI/squash/main CI/CD，不能把本地资格写成远端已验收。