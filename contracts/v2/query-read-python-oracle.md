# Query 只读迁移前 Python oracle

本语料落实 PR140 计划 L3A 的迁移前契约基线，仅含相邻的 `query.readRows` 和
`query.validateSnapshot`。生产者是 Git 基线 `b55f878641bf74c0b49b04222a217c48abf544a7`
的 Python `RpcDispatcher`、闭合 ProductParams、`PocketBaseProductRpc` 和真实
`PocketBaseClient`；此提交未修改这些生产路径、owner、catalog 或 router。

`generate_query_read_oracle.py` 的 23 个固定输入通过真实 dispatcher 执行，保存完整
JSON-RPC 请求/响应和下游 HTTP method/path/body/status 期望。认证 header 在捕获时
断言为固定测试值，不写入语料。`authorityFixture` 是脚本化 HTTP 响应或异常，明确
只证明 Python 参数解释、投影和错误映射，不证明 Go 的查询结果、snapshot 领域校验、
HTTP 限额、真实持久数据或跨进程资格；没有模拟 Go parity，也没有新增摘要。

覆盖 Unicode/NFD/ZWJ/RTL、blank/0/false/null/空容器、空 rowIds、重复及空白 ID、
非路径形式 tableId、可选 currentQuery 的缺失和空对象、非法输入、错误响应形状及
公开 Product/transport 错误。保留现状而非重设计：非字符串/空 row ID 是 handler
内部错误 `-32603`，顶层形状错误是 `-32602`；空白 ID 和非路径形式 tableId 会原样
下传。readRows 的列表元素检查和 validateSnapshot 的对象响应检查也被捕获。

生成一次，后续只读比较：

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_read_oracle --write
uv run --frozen --no-sync python -m contracts.v2.generate_query_read_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_query_read_python_oracle.py -q
```

`--write` 仅可创建不存在的输出，不覆盖历史证据；普通测试不会写文件。未来 owner
迁移必须继续消费已合并的 JSON 原件，不能从新 Go 实现重算 expected。届时需有意
退役 Python replay 入口，并新增独立 Go 消费与实际打包验证；本片不宣称迁移完成。
本地聚焦 `--no-cov` 不替代完整 CI 的既有覆盖率和发布门禁。
