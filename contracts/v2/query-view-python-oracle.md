# query.view 原始 Python 契约语料

固定生产者：`a6840f3ad983a0e6722d623737d4de668c905660`。
本片只冻结 L3A 下一只读纵切的迁移前证据；不改 owner、生产 handler、catalog 或 router。

专用生成器独立定义 33 个 request 与 authority 输入，经真实 `RpcDispatcher` →
`ProductParams` → `PocketBaseProductRpc` → `ProductQuerySchemaRpc` → `PocketBaseClient`
捕获完整公开响应。仅 HTTP transport 被脚本化，并断言每个非参数拒绝案例至多一次请求。
输入未读取 expected response，也未执行未来 Go Product handler。此方法不返回 schema，
不需要复制 schema/capability fixture；固定 query snapshot 和分组字段只代表 authority 输入。

语料覆盖普通记录、单层分组分页、两层 parent 汇总、空结果、Unicode/NFD/ZWJ/RTL、
false/0/-0.0/null/空容器、view 原样转发、参数拒绝、malformed authority 以及公开
Product/transport 错误。下游 page 的 `querySnapshot` 在 Product 响应中投影为 `snapshot`；
groupRows 和 parent 字段保留原值。JSON 比较保留 false 与 0、负浮点零的区别。

## 原 Python 与 Go typed 边界

原 handler 使用 client dataclass 和局部形状校验，**没有**执行
`backend.contracts.query.QueryViewResult.model_validate`。它不为分组补 parent null，
也不把 view 解析为完整 `ViewQuery`。测试仅额外检查五个常规成功样本符合公开 typed model，
不能据此声称原 Python 对所有返回值执行了该模型。

| 样本 | 固定生产者实际行为及 Go 迁移注意事项 |
| --- | --- |
| 五个常规成功窗口 | Go `ViewResult/Page/GroupRow/QuerySnapshot` 的字段形状可表达；snapshot 使用 Go `TableQuery` 的 offset/limit 形状。未运行真实查询，不证明分组计算、分页数量或签名正确性。 |
| empty-view / unknown-view-members / table-non-path | Python 原样转发；真实 Go REST 解析和领域行为须另测，脚本化成功不代表 authority 接受这些输入。 |
| paired-null-parent | Python 保留两个显式 null；Go 两个 parent 字段使用 omitempty，不能将该 JSON 假装为直接 marshal 的 typed Port 输出。 |
| extra-group-member / empty-snapshot | Python 保留；Go 闭合结构不能表达额外 group 成员或无字段的 QuerySnapshot。 |
| negative-counters | Python 局部整数检查接受负数和零 limit；Go 数值类型可表达，但真实查询不会据此证明可生成这些值，不得为 parity 而削弱领域校验。 |
| malformed authority | Python 公开为 Internal error；Go 缺字段/错误 JSON 类型不能一概视为 typed Port 可达。迁移时按具体案例分类，避免先在无关字段失败而伪称覆盖。 |
| transport-error | 原 Python HTTP 边界的 sidecar.unavailable；进程内 Go Port 无同样的 HTTP transport 异常。 |

当前证据只证明 Python 请求解释、单次 HTTP 请求、响应投影及错误映射。
Go 迁移须独立消费保留的 JSON、补真实 Port 契约，并通过产品现有分组/汇总路径：
`ViewQueryControls` → `table.queryRequested` → `GridStateCoordinator.HasViewAggregates`
→ `QueryTableViewRawAsync` → `query.view`。cursor/selection window 与分组结果的 revision
配对继续由现有 coordinator 检查，不能用单个假消费者替代真实打包资格。

## 捕获与只读检查

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_view_oracle --write
uv run --frozen --no-sync python -m contracts.v2.generate_query_view_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_query_view_python_oracle.py -q
```

`--write` 只可 exclusive create，不覆盖现有文件；无参数等同 `--check`，比较规范 JSON，
差异时报错而不写文件。测试不重写仓库语料。以后 owner 切换需有意退役当前 Python replay，
保留固定生产者和原件；不能用迁移后的行为重生成旧 expected。此片没有执行 Go、桌面、
打包 E2E 或发布 smoke，也不宣称 L3A 迁移验收完成。