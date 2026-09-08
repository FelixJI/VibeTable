# lookup.query 原 Python 契约原件

固定 producer：`6e25fd033697c57a4ca113caf98c90293b892548`。本冻结变更只新增捕获器、原 37 例加后续独立追加 2 例的 JSON、本文与聚焦测试；不切换 owner，不修改生产或 CI。输入由 `cases()` 独立构造，目录 revision 使用生产 `_lookup_revision`，不从 expected 或 Go 输出反推。

每例实际运行 `RpcDispatcher` → `PRODUCT_RPC_REGISTRY["lookup.query"]` 闭字段 DTO → `PocketBaseProductRpc` → `ProductRelationLookupFileRpc._query_lookups` → `PocketBaseClient.query_lookup_view`，仅 HTTP transport 用有序脚本替代。保留完整请求、authority fixture、每跳方法/路径/query/body/expectedStatus 和公开响应；脚本成功不是 Go 领域认可，也不是实际产品 UI 资格。

## 执行契约

- Product 的八字段全部必需：contract、collection、fieldRefs、query、requestGeneration、schemaRevision、permissionRevision、lookupRevision。类型依次为 string/string/array/object/integer/string/string/string；bool 不能代替 integer。顶层未知字段拒绝，字符串非空，但不 trim、不规范化 Unicode；contract 仅校验非空字符串，不验证固定 literal。不能套用另一个 `LookupQueryParams` 模型的默认值或更严格约束。
- Product 先检查递归凭据键、32 层深度、有效 Unicode 与紧凑 UTF-8 1 MiB 预算。测试分别覆盖恰好预算、超预算、孤立 surrogate、深度及六个嵌套凭据键。此 Python client 的 `_post` 只进行 JSON 值复制/校验，没有第二层 1 MiB 本地预算；真实 HTTP 服务的请求预算不由脚本 transport 证明。
- query.groups 省略时取空数组，从 query 副本移除。错误容器类型在目录 GET 前失败。GET `/api/vibetable/v1/relations/describe` 后检查 lookups 数组、schemaRevision 和三个 revision（permissionRevision 也与 schemaRevision 比较）。之后检查各 group 对象、direction（默认 asc，仅 asc/desc）与非空 fieldRef。
- POST `/api/vibetable/v1/lookups/query` 使用 tableId、schemaRevision、移除 groups 的原 query、映射后的 `{field,direction}` 数组与固定 groupLimit=5000。原 Python 不在此处用 Go TableQuery 解码 query，也不补 offset/limit。错误分组成员阻止第二跳；未知 fieldRefs 在第二跳之后才失败。
- client 先验证 flat view 的 groupRows/hasMoreGroups，再解析 page 和 groupOffset/groupLimit。hasMoreGroups=true 在列构建前拒绝。即使 fieldRefs 未选中，所有有字符串 physicalName 的目录成员仍会执行 `_renderer_lookup`；重复 physicalName 以后者覆盖。请求中重复 fieldRefs 保留；空 fieldRefs 和负 requestGeneration 被接受。
- 列仅公开 fieldRef/title/outputType/nullable=true/scale=null/state=valid。rows 的动态对象、null/false/0/空串及 snapshot 原对象保留。组节点只接受一或二级 key，二级父节点按第一次出现输出且使用该行 parentCount；summary 不公开，aggregates 固定空对象，childCursor 固定 null，保持行顺序。负 authority 数量是原 Python 可接受的脚本输入，不能当成领域有效值。
- malformed 输入/响应通常成为公开 -32603，Product 前置校验为 -32602；两跳公共错误与 transport 错误各捕获调用次数及 -32150 公开 data，不能把内部异常文字作为公开响应扩写。

## Go 表达边界

原件 `typedGoBoundaries` 标注畸形 root、字段缺失/错误类型、开放 snapshot 与两跳 transport 故障等具体边界，并明确两跳公开领域错误可表达且必须回放，不是可跳过整个案例或所有动态值的名单。特别是 `catalog-lookups-null` 可以由 Go nil slice 表达，必须保留拒绝语义，不能因为列入元数据就豁免。`catalog-unselected-malformed` 的缺字段可通过空 Go 字段得到类似失败，但不能声称 wire 完全同形。

另有跨案例的差别：Go TableQuery 会补默认 offset/limit 并拒绝未知 query 字段，原 Python 转发空 query 或额外键；基线 snapshot.normalizedQuery={} 也不与 Go TableQuery 的必有 offset/limit wire 同形。这里没有宣称剩余 22 例自动构成完整 typed 回放集合。未来 owner 迁移需逐例确定公开结果的可表达部分，保持全部 37 原件，并以新增独立输入验证完整 typed snapshot；不得为适配 Go 重写原 JSON。Go rows 的 map/any 和 GroupRow.Key 的 any 本可表达动态 JSON，不能笼统豁免。

经审查追加恰好两例：typed-shape-dynamic-rows 与 typed-shape-grouped-rows。两者的目录显式包含 LookupDescriptor.path，snapshot 的七个字段齐全，normalizedQuery 含 offset/limit，仍由真实 Python 执行链生成公开响应。普通例给出完整可表达的动态 Unicode 行成功回放；两级组例使用成对且非空 parentSummaries，验证父节点首次出现与子节点顺序。追加时验证旧 37 条 cases 的文本前缀逐字不变，解析后的旧 37 条也完全相等；不是重生成原输入/输出。

两级组例仅说明 Go DTO 可表达此脚本形状，不声称当前领域服务会产生它。独立待核候选：relation.Service.QueryLookups 当前没有向 ViewQuery 传 summaries；query.GroupRow.ParentSummaries 的 omitempty 会省略空数组，而 Python _valid_group_row 要求 parentCount/parentSummaries 同时存在。这是可做针对性复现的既有双级分组边界，不在本冻结变更或未来 owner 迁移中悄悄修复，尚无真实领域或 UI 资格结论。

## 冻结提交的捕获与验证（历史）

在本工作树根显式设置共享 uv 环境的 `UV_PROJECT_ENVIRONMENT`、`UV_NO_SYNC=1`、`PYTHONPATH` 为当前工作树绝对根、`PYTHONUTF8=1` 后执行：

```powershell
uv run --frozen --no-sync python -m contracts.v2.generate_lookup_query_oracle --check
uv run --frozen --no-sync python -m contracts.v2.generate_lookup_query_oracle
uv run --frozen --no-sync python -m pytest tests/contract/test_lookup_query_python_oracle.py -q --no-cov
uv run --frozen --no-sync ruff check contracts/v2/generate_lookup_query_oracle.py tests/contract/test_lookup_query_python_oracle.py
uv run --frozen --no-sync ruff format --check contracts/v2/generate_lookup_query_oracle.py tests/contract/test_lookup_query_python_oracle.py
uv run --frozen --no-sync pyright contracts/v2/generate_lookup_query_oracle.py tests/contract/test_lookup_query_python_oracle.py
```

默认和 --check 都重新运行全部真实捕获并逐字比较，不写文件。--write 使用 exclusive create，已有文件会失败。来源 guard 校验根 adapter、专属 handler、client、dispatcher、实际 Product DTO、revision helper、LookupViewQueryCommand/ViewQueryResult 与 flat view parser 的模块路径均来自当前工作树 backend；拒绝 shared editable 导致的外来导入。随后对这些实际源文件执行固定 producer 的 git diff，源码修改或 Git 验证失败都在 capture 内拒绝，不新增 hash。错误 authority 请求先记账；协议违规另外保存，dispatcher 返回后由捕获器直接失败，不能冻结成普通 -32603。

首次聚焦 pytest 51 passed（1.18 秒），Ruff 通过，Pyright 0 errors。检查期间首次临时创建脚本因默认 GBK 读取参考文件失败（尚未创建原件），修正为 UTF-8 后才首次捕获；初次 Ruff 的复合 assert 拆分提示已修复。真实错误案例产生的 dispatcher 错误日志属于被捕获的原行为。拒绝覆盖、默认/check 差异拒绝和 foreign source 都有负向断言；未修改生产，因此不声称有生产修复红→绿或新 Go/UI 资格。

审查修正记录：仅更正顶层 typedGoBoundaries 的 catalog-product-error/page-product-error 两条分类文本；它们是可由 mutation.ProductError → PublicError 表达的领域公开错误，必须回放，不是 transport 豁免。原 37 cases 的完整文本和解析输入/输出/请求顺序均验证不变，此次没有重新采集这些案例。新增来源漂移/Git 失败回归在旧捕获器上 2 FAIL，新增错误路径/第三请求回归在旧捕获器上 2 FAIL；均因旧 capture 未向调用者抛出拒绝而失败，精确红测日志保留。

最终审查修正验证：同一完整聚焦 pytest 命令得到 58 passed（14.61 秒），包含原 37 例、新增 2 例与来源/协议负向回归；Ruff check、Ruff format --check、Pyright 均 EXIT0（0 errors）。来源漂移回归在 capture 实际入口模拟 Git diff 的 1/128 返回，旧实现两例均未抛 RuntimeError；修复后 Git 检查非零即拒绝。错误路径与第三请求回归修改运行时 seam 并继续真实 dispatcher，旧实现两例也未抛 RuntimeError；修复后协议违规在 dispatcher 外拒绝。精确红测入口如下，不能把这些测试夹具红→绿称作生产修复：

```powershell
uv run --frozen --no-sync python -m pytest tests/contract/test_lookup_query_python_oracle.py -q --no-cov -k producer_source_drift
uv run --frozen --no-sync python -m pytest tests/contract/test_lookup_query_python_oracle.py -q --no-cov -k outside_dispatcher
```

## owner 迁移后的保留方式

冻结提交 `78f48855c3b7fbf73a545abd53494317ae895763` 保留上述真实 Python 捕获器及 58 例验证证据。当前迁移移除专属 Python handler/client 后，捕获入口已退役：`capture`、`capture_case` 和 `--write` 一律拒绝，包括目标不存在时；默认及 `--check` 只验证固定 producer、39 例独立输入、authority fixture 和 typedGoBoundaries，不重新产生期望响应。原 JSON 无改动，原捕获历史与当前保留检查不能混称。Go 回放与实际产品资格见 `docs/quality/lookup-query.md`。
