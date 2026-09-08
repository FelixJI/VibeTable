# Query 只读迁移前 Python oracle

本语料落实 PR140 计划 L3A 的迁移前契约基线，仅含相邻的 `query.readRows` 和
`query.validateSnapshot`。生产者是 Git 基线 `b55f878641bf74c0b49b04222a217c48abf544a7`
的 Python `RpcDispatcher`、闭合 ProductParams、`PocketBaseProductRpc` 和真实
`PocketBaseClient`；冻结语料的原始提交未修改这些生产路径、owner、catalog 或 router。

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

原始 JSON 的 23 个 case 保持不变。`query.readRows` 迁至 Go 后，Python replay 明确
仅保留 `query.validateSnapshot` 的 10 个 case，测试同时断言两者的当前 owner，
不会根据动态 owner 自动跳过失败。Go 独立消费 12 个 readRows 原件 case；剩余的
Python→Sidecar transport 故障属于已移除的跨进程跳转，不作为 Go 直接调用的 parity。

当前只读比较未迁移子集：

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_read_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_query_read_python_oracle.py -q
```

`--write` 在 owner 迁移后无条件拒绝，文件不存在时也不能重新生成。普通测试不会写
文件，已迁移方法不能再由 Python 捕获；Go 消费冻结 JSON，不从新实现重算 expected。
本语料回放不替代实际打包验证，也不宣称其余 query 方法迁移完成。
本地聚焦 `--no-cov` 不替代完整 CI 的既有覆盖率和发布门禁。
