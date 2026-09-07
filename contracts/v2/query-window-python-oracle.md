# Query 分页与游标窗口迁移前 Python oracle

本语料为 PR140 的 L3A 后续迁移冻结 `query.page`、`query.cursorOpen` 和
`query.cursorFetch` 的 Python 契约。生产者固定为 Git 基线
`c97c83336e4aa1bdf993fc46a7de57040219fb03`；本片不修改生产路径、owner、catalog 或 CI。

27 个固定输入执行真实 `RpcDispatcher`、闭合 ProductParams、Python adapter 与
PocketBaseClient，仅下游 transport 返回预设响应或异常。原件记录完整 JSON-RPC
请求/响应及下游 HTTP method/path/body/query/status 期望；认证 header 使用固定测试值
并在捕获时断言，不写入语料。

覆盖分页 offset/limit 和 snapshot 字段投影、查询原样下传、Unicode/falsy、继续与终止
游标窗口、未知字段/非法输入、畸形响应，以及公开领域错误与 transport 错误的区别。
保留当前语义：page 输出 `snapshot`，cursor 输出 `querySnapshot`；空 nextCursor 字符串
仍与 hasMore=true 配对。query 的嵌套 offset=-1 只是透传证据，不表示 Go 接受该查询。

这不证明真实 Go 查询、cursor 生命周期/过期/并发、HTTP 大小预算或实际打包资格。
冻结结果不能由后续实现重算替代；owner 迁移时应明确退役对应 Python replay，并独立
消费原件验证新 owner。没有新增依赖、摘要或通用生成框架。

`query.page` 已迁移到 Go：其 10 个原始案例保留供新 owner 独立消费，Python replay
仅运行 `query.cursorOpen`、`query.cursorFetch` 的 17 个案例。捕获器拒绝 page replay，
`--write` 无论文件是否存在均拒绝，避免后续实现冒充原始生产者。

后续只读比较（复用锁定环境）：

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_window_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_query_window_python_oracle.py -q
```

默认与 `--check` 均只读比较剩余 Python cursor 契约，偏差时报错；27 个冻结原件不变。
聚焦测试的 `--no-cov` 不替代完整 CI 的覆盖率与发布门禁。
