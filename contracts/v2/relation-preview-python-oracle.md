# relation.previewDelta 原 Python 契约

固定 producer 为 `6ed36810f3753caed5e2e8ca27a4d4ad2117d41d`。37例独立输入经过
真实 `RpcDispatcher`、`PRODUCT_RPC_REGISTRY`、`PocketBaseProductRpc` 和
`ProductRelationLookupFileRpc`，仅 authority HTTP transport 使用脚本响应。生成器不读取
expected 构造输入。每例保留原请求、脚本响应/失败、实际 authority 请求和完整 Python
响应；这不是 Go 领域或实际 UI 的执行记录。

## 冻结与迁移状态

原件在固定 producer 工作树通过真实 Python 捕获，来源 guard 当时核验 dispatcher、
Product DTO、根 adapter 和专属 handler 均来自该工作树。首次直接脚本入口曾导入共享
editable 环境所属旧工作树；该未提交、不合格输出已废弃，随后明确目标 PYTHONPATH
和模块入口重新 exclusive 捕获。冻结时47项测试、默认与check完整比较通过，原件不再更新。

本次 owner 迁移后，capture/capture_case 和 --write 均明确拒绝；默认及 --check 仅核对
原 producer、37项独立输入与typed边界，不调用已删除的Python handler，也不改写JSON。
Go Product HTTP tests 完整消费26个可表达案例，11项无法表达的DTO/transport边界逐项列明；
额外Go测试保留预算、Unicode/深度、翻译及上下文取消验证。冻结阶段的捕获实现保留在Git历史。

```powershell
uv run --frozen --no-sync python -m contracts.v2.generate_relation_preview_oracle --check
uv run --frozen --no-sync python -m pytest tests/contract/test_relation_preview_python_oracle.py -q --no-cov
```

参数必须提供 relationId、sourceItemId、expectedSchemaRevision、adds、removes、
idempotencyKey；expectedDateUpdated 可省略。此方法没有 field_types 映射：
缺字段、未知字段、递归凭据、深度及预算失败为 Product 的 -32602；
params本身不是对象则先由JSON-RPC envelope拒绝为-32600；
空/错误类型 ID、null adds、非对象目标等会进入 handler 后返回 -32603。
顶层 sourceRecordId、actor、expectedDigest 等旧 authority 字段不属于公开闭集。

翻译后固定 POST /api/vibetable/v1/relations/preview-delta，sourceItemId改为
sourceRecordId，expectedSchemaRevision改为schemaRevision，requestId取idempotencyKey，
expectedDigest为null，actor为local-user。expectedDateUpdated只保留在回显delta，
不传下游，即使值是对象也不会按日期解释。数组顺序、重复目标和空白文本不预先重写。
目标target为对象时优先使用内层，否则使用外层；tableId/recordId优先于collection/itemId，
取首个非空字符串。label省略或空串回退record ID，空白保留，null/错误类型报handler错误。

输出固定为delta、current、diagnostics、canApply。delta保留完整原始参数，
包括目标额外字段和expectedDateUpdated；diagnostics总为空数组。current必须是数组，
非对象成员被过滤，对象投影为collection/itemId/label/secondaryLabel。前三项必须为
非空字符串；secondaryLabel按Python真值取原值或null，真值对象/数组/数字不会被字符串化。
canApply只在下游值严格为true时为true，数字1也为false；下游result/adds/removes及
diagnostics不公开。公开错误与transport错误保留原-32150族，畸形响应为-32603。

JSON中的typedGoBoundaries逐例标注Go DTO无法表达的authority形状：非对象根、
混合/非数组current、非字符串label/secondaryLabel、canApply缺失或非bool，以及
Python HTTP transport失败。这不是允许跳过所有对应输出断言的总豁免；nil/空切片、
空字符串和bool false仍可表达。原delta回显也不能从翻译后的DeltaRequest重建。
成功脚本中的重复adds、空白ID或canApply=false不代表真实Service会接受/产生它们。

冻结时补充测试及迁移后的Go测试不把巨大字符串写入JSON：Product采用紧凑UTF-8 JSON的1MiB预算、
depth32和递归凭据限制，拒绝孤立surrogate且保留组合Unicode。实际翻译可能扩容，
也可能因expectedDateUpdated被丢弃而缩小。测试测量真实Python发出的body，
明确区分Product边界与既有Go REST独立1MiB上限；脚本transport不执行Go REST，
不将越过第二层预算的脚本成功称为领域接受。

Go Service的PreviewDelta只prepareDelta、调用Kernel.Preview并返回原current，
不应用变更或创建持久token；真实成功canApply=true。ReadRows内部可生成临时query
snapshot但不在preview公开输出。S28实际覆盖many编辑器加载、增加本地选择与取消；
提交前预览和创建后关联仅已定位为消费者，本次未分别执行其UI流程。Go真实authority测试
覆盖预览成功与拒绝的零写入；当前进展见[预览迁移资格](../../docs/quality/relation-preview.md)。
