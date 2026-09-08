# relation.previewDelta 原 Python 契约

固定 producer 为 `6ed36810f3753caed5e2e8ca27a4d4ad2117d41d`。37例独立输入经过
真实 `RpcDispatcher`、`PRODUCT_RPC_REGISTRY`、`PocketBaseProductRpc` 和
`ProductRelationLookupFileRpc`，仅 authority HTTP transport 使用脚本响应。生成器不读取
expected 构造输入。每例保留原请求、脚本响应/失败、实际 authority 请求和完整 Python
响应；这不是 Go 领域或实际 UI 的执行记录。

从该工作树根目录运行：

```powershell
$env:PYTHONPATH = (Get-Location).Path
uv run --frozen --no-sync python -m contracts.v2.generate_relation_preview_oracle --check
uv run --frozen --no-sync python -m pytest tests/contract/test_relation_preview_python_oracle.py -q --no-cov
```

不传参数同样完整重捕获并比较，任何差异都失败且不写入。仅首次 `--write` 使用
exclusive create，已有文件拒绝覆盖。来源 guard 核验 dispatcher、Product DTO 和根
adapter及专属ProductRelationLookupFileRpc handler的实际源码来自本工作树，防止复用
uv editable 环境时导入另一个 checkout。
本次首次直接脚本入口曾导入共享环境所属工作树；该未提交、不合格输出已明确废弃，
改用目标 PYTHONPATH 与模块入口重新 exclusive 捕获。此后 JSON 是保留原件，
不能用新行为刷新。owner 迁移后应退役重新捕获入口，保留原件及只读来源检查。

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

补充测试不把巨大字符串写入JSON：Product采用紧凑UTF-8 JSON的1MiB预算、
depth32和递归凭据限制，拒绝孤立surrogate且保留组合Unicode。实际翻译可能扩容，
也可能因expectedDateUpdated被丢弃而缩小。测试测量真实Python发出的body，
明确区分Product边界与既有Go REST独立1MiB上限；脚本transport不执行Go REST，
不将越过第二层预算的脚本成功称为领域接受。

Go Service的PreviewDelta只prepareDelta、调用Kernel.Preview并返回原current，
不应用变更或创建持久token；真实成功canApply=true。ReadRows内部可生成临时query
snapshot但不在preview公开输出。实际many关系编辑器加载、提交前预览和创建后关联
资格属于后续实施与真实包验证，本冻结变更不切owner、不改变生产或CI。
