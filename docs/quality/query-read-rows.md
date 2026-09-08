# query.readRows Go Product 迁移

来源 PR278 只切换 `query.readRows`：WPF 依据生成 policy 发送到 Go Product gateway，直接调用
既有 Query Port。Python 对应 handler 与注册已移除；`query.page`、cursor、selection、view
和 `validateSnapshot` 的 owner 在该来源中不变。本次整合另纳入独立通过 CI 的 PR280 和 PR284，
最终同时迁移 readRows、cursorOpen、cursorFetch、selectionOpen；其余 owner 遵循最新 main。

参数形状、递归凭据键、深度、Unicode scalar 和紧凑 UTF-8 JSON 的 1 MiB 参数预算仍在
Product 校验层拒绝；非字符串/空 row ID 保留原 handler 错误。空数组、重复和空白 ID
不提前重写。旧 REST body 增加 `operation` 后的 1 MiB 限额、纯空白 tableId 的公开错误
以及 Query Port 的 200 项上限、顺序/重复/不存在项处理仍保留。取消传给领域 context。

迁移消费 PR272 固定 producer `b55f878641bf74c0b49b04222a217c48abf544a7` 的
`contracts/v2/query-read-python-oracle.json` 原件，覆盖 12 个 readRows 投影/拒绝用例。
Python HTTP transport failure 不属于直达 Go Port 的领域结果，在测试中显式说明不适用。
此外，真实 PocketBase Product HTTP 测试保存两条记录并验证 Unicode、空文本、JSON falsy、
请求顺序、重复/不存在 ID、200/201 项边界及取消；这些证据与脚本化 oracle 分开。

旧 Python 下游 HTTP 可接收 16 MiB，但最终 Python/C# JSON-line framing 已限制为
4 MiB；新 Product HTTP 和 WPF gateway 继续使用公共 4 MiB 上限。不同 envelope 的边缘
开销并非逐字节相等，不将下游 16 MiB 误记为旧产品可传输的最终响应大小。

既有 packaged 场景 `04-json-round-trip` 通过工具栏插入和 JSON 编辑，调用宿主
`PocketBaseTableGateway` 的 `ReadRowsInternalAsync`，在新 policy 下走 Go owner。
宿主组合测试单独验证 `ReadRowsAsync` 选择真实 Product HTTP gateway 并保持返回值。
`query.readRows` 不是网页 `ProductDataRpcRegistry` 的命令，本片不新增网页调用入口。
代码和源码测试不代替该场景实际执行；完整 fresh CI、严格同步 main、双轴审查和合并后
验收完成前，本片仍不能记为交付完成。
