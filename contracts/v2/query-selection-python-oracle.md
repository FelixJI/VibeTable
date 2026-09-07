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
