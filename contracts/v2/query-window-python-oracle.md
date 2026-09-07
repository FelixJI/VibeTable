# Query 分页与游标窗口迁移前 Python oracle

本语料为 PR140 的 L3A 后续迁移冻结 `query.page`、`query.cursorOpen` 和
`query.cursorFetch` 的 Python 契约。生产者固定为 Git 基线
`c97c83336e4aa1bdf993fc46a7de57040219fb03`。冻结时没有修改生产路径；当前游标迁移已退役 cursorOpen/cursorFetch 的 Python replay，只保留仍属 Python 的 page replay。

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

原27个案例保持不可变；当前只读比较尚未迁移的10个page案例（复用锁定环境）：

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_window_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_query_window_python_oracle.py -q
```

迁移后的 `--write` 一律拒绝，包括原件不存在的情况；默认与 `--check` 均只读，偏差时报错。游标完整HTTP对照和typed补充的适用边界见 [补充语料说明](query-cursor-typed-python-oracle.md)。
聚焦测试的 `--no-cov` 不替代完整 CI 的覆盖率与发布门禁。
