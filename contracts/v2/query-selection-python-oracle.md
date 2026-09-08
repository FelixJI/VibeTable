# query.selectionOpen 原始 Python 契约语料

生产者代码基线：ccfbce59a811fcfb9b27baa11fb83bca25f5a8fc。
此时 query.selectionOpen 仍由 Python owner 注册。本次仅冻结迁移前证据，不切换 owner，
不修改现有 policy、生产实现或其他语料。

生成器独立定义请求和 authority 输入，执行真实 RpcDispatcher → ProductParams registry →
PocketBaseProductRpc → ProductQuerySchemaRpc → PocketBaseClient，再由真实
QuerySelectionProjectionResult 校验、补齐默认值并序列化。只有 HTTP transport 被脚本化。
每例记录完整 JSON-RPC 请求/响应和一次 selection.open HTTP 请求（参数拒绝时为零次）；
传入固定测试 session header，但不把 header 写入语料。冻结输出不参与捕获计算，
没有从 expected 响应反向生成结果。字段与 capability 输入摘自该基线的 Schema V2
field-definition.json 和 capability.json，已嵌入生成器，不运行时依赖可变共享 fixture。

28 例覆盖完整 schema/capability 的默认 null、完整 query snapshot、合法 limit、
Unicode/组合字符/双向文本及 false、0、-0.0、null、空数组和空对象，非终止/终止窗口、
空记录、空 query 转发、公开 product/transport 错误、闭合参数拒绝、缺失或非法 schema、
table/schemaRevision/dataRevision 不匹配，以及 cursor 存在性和类型不变量。
成功样本是完整 typed 可达结构；opaque cursor、snapshotId 和 digest 是脚本化 authority
原样输入，未声称来自真实游标签发或摘要计算，也不新增 hash 校验。

在仓库环境执行：

    uv run --frozen --no-sync python -m contracts.v2.generate_query_selection_oracle --check
    uv run --frozen --no-sync python -m pytest tests/contract/test_query_selection_python_oracle.py

无参数默认等同 --check，逐字比较规范 JSON；--write 仅首次以 exclusive create 写入，
已存在时拒绝覆盖。后续差异需要审查生产者行为变化，不能重生成旧基线覆盖差异。
预期 malformed authority 样本会产生 dispatcher 错误日志，冻结的公开响应仍为 Internal error。

边界：真实 SelectionPort 是一次原子查询，不是 schema 与 cursor 的两个独立读取。
此语料只证明 Python 请求解释、传输投影、响应闭合校验和公开错误映射，不能证明
PocketBase 查询原子性、真实 cursor 编解码、过滤/排序、并发 revision 行为或持久化。
Go 迁移需保留真实领域及产品测试，并另行重放该冻结语料；此处未运行真实 Go、
桌面、发布 build/smoke 或全量 QA，也没有修改或迁移 owner。

## owner 迁移后的保留方式

query.selectionOpen 已退出当前 Python runtime；上述生产者说明仅适用于固定的
ccfbce59a811fcfb9b27baa11fb83bca25f5a8fc 基线。原始 28 例 JSON 不改写。
当前生成器已删除 Python transport/capture 实现，--write 无条件拒绝；默认及 --check
仅验证保留的 producer、案例清单、请求和 authority 输入，不声称重放原 Python handler。
Python 契约测试继续校验冻结输出的 typed shape、错误分类和 Unicode/falsy 事实，
Go dispatcher 消费冻结输入/输出，并另用真实 PocketBase 验证领域行为。
需要重现原始捕获时使用上述 producer 的源代码，不能将当前 Go 结果反写为旧 oracle。

## Go typed authority 的补充成功语料

`query-selection-typed-python-oracle.json` 保留四个成功窗口（完整字段、末页、空记录、
空查询）的原 Python 公开响应。原始 28 例仍逐字保留；其中成功样本只有一个 capability，
不满足真实 Go SchemaSnapshot 的完整 canonical capability 契约，不能直接充当 Go Port 输出。

补充语料的 Python 生产者固定为提交 `c6115d27312a8abad176d0223aae1f060bf3d155`
中的 `contracts.v2.generate_query_selection_oracle.capture_case`，authority 类型与
capability 生产者固定为 `b8294c2deff6890c80ac1c0c809309f64b997c86`。捕获步骤为：

1. 从原 28 例取上述四个成功案例的 request 与 authorityFixture.response，使用
   `json.Decoder.UseNumber` 解码为 `query.SelectionProjection`。
2. 按 `v2.LogicalTypes` 顺序用 `v2.CapabilityFor` 填入全部 18 个 canonical
   capabilities，通过 `v2.ValidateSnapshot`，再用 `json.Marshal` 输出 Go authority。
   `TableQuery` 的 omitempty 规则在此生效，不人工补 keyword、filters 或 sorts。
3. 在固定 Python 生产者的独立 checkout 中，构造同一 method/params 的 `Case`，
   name 加 `typed-` 前缀，authority response 使用上一步 JSON；运行原始
   `capture_case`，经真实 Python dispatcher、adapter 与 typed projection 捕获 response。
   只替换其已有 scripted transport，未用新 Go handler 输出生成 expected response。
4. 使用 exclusive create 保存四例及两个 producer commit。当前 Go HTTP dispatcher
   精确比较这些冻结 Python response，并独立运行真实 PocketBase 原子查询与 cursor 延续测试。

原始 malformed JSON 中无法由 typed Port 表达的缺字段/类型错误在 Go 测试中逐例说明，
可表达的 schema、表与 revision 配对、计数、cursor 和 row 异常用 canonical 基线单项变异验证。
这些变异是当前 authority 拒绝契约测试，不冒充原始 Python corpus 的逐字重放。
