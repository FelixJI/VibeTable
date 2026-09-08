# relation.searchTargets 原始 Python 契约语料

固定生产者：`8cf989c5873830d28d61067a9afb82ddb06db124`。

本片只冻结迁移前行为，未切换 owner 或修改生产代码。专用生成器独立定义30例 request
与 authority 输入，经真实 `RpcDispatcher`、`PRODUCT_RPC_REGISTRY` 的 `ProductParams`、
`PocketBaseProductRpc`、`ProductRelationLookupFileRpc` 和 `PocketBaseProductContext`
捕获完整公开响应。只脚本化 HTTP transport，每例至多一次 authority 请求。
输入不读取 expected response，也不调用未来 Go Product handler。

实际 Product 使用闭合的 relationId/query/offset/limit 参数，不执行
`relation_admin.RelationSearchParams` 或 `RelationSearchResult`。省略 query 会发送空字符串，
offset/limit 默认0/50；显式空 query 被 Product 拒绝，原始空白 query 不裁剪。
现有 UI 在 `relationLookupService.searchTargets` 中 trim 搜索文本并省略空 query，
因此 UI 初次空搜索不等于向 Product 发送显式空字符串。

公开结果仅保留 items 中的对象成员，并将 tableId/recordId/label 投影为
collection/itemId/label。secondaryLabel、snapshot 和额外 authority 成员不公开。
total 不随过滤重新计算，且原 Python 接受负整数；bool或非整数拒绝。
缺失/空/错误类型的必要 item 字段公开为 Internal error，不补默认值。

| 原件范围 | 迁移时必须区分的边界 |
| --- | --- |
| 默认、显式分页、Unicode/NFD/ZWJ/RTL、空白 query、空结果 | 证明 Python 请求解释和公开投影；不证明真实关系解析、查询排序或分页。 |
| 非对象 items 过滤 | Python 可接收混合 JSON 数组；Go `[]relation.TargetRef` 不能表达这些非对象成员。 |
| secondaryLabel/snapshot/额外成员 | 原 handler 丢弃它们。Go `SearchResult` 有 snapshot、`TargetRef` 有 secondaryLabel，不应直接 marshal 为 Product 结果。额外成员和脚本化 snapshot 不是完整 typed Go authority 结果。 |
| 负 total | Go int64 可表达，但脚本化成功不证明真实 authority 可生成；不得为 parity 放松领域约束。 |
| limit101、负offset/零limit、257字符query、空白relationId | Product原样转发；真实Go领域会在解析关系后检查offset及limit（最大100），不能把脚本化成功写成领域接受，也不能套用未执行的Python模型上限200。 |
| malformed authority 与公开错误 | 区分Python投影拒绝、Go typed不可表达和领域 `ProductError`；原transport错误是Python HTTP边界的 `sidecar.unavailable`。 |

闭合字段、bool冒充int、递归凭据在30例中覆盖。1 MiB紧凑UTF-8参数边界和非法Unicode
通过独立测试验证，避免将巨型字符串放入语料。搜索只允许标量字段，独立测试其继承的
`ProductParams` 32层深度边界，不把嵌套搜索query伪称为合法领域输入。

```text
uv run --frozen --no-sync python -m contracts.v2.generate_relation_search_oracle --write
uv run --frozen --no-sync python -m contracts.v2.generate_relation_search_oracle --check
uv run --frozen --no-sync python -m pytest --no-cov tests/contract/test_relation_search_python_oracle.py -q
```

`--write` 只可 exclusive create，不覆盖已有原件；默认及 `--check` 比较完整捕获文本，
差异时报错且不写文件。聚焦 `--no-cov` 不代替完整CI覆盖率与发布门禁。
后续 owner 迁移应退役对应 Python replay并保留原件；新 owner 独立消费语料，并另补真实
Go Port、现有关系编辑器搜索/候选/空结果/迟到响应的产品证据。本片没有执行 Go、桌面、
打包 E2E 或发布 smoke，不宣称关系查询迁移完成。
