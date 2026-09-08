# lookup.valuePage 原 Python 契约

固定 producer 为 `a19ccd5366d62be6338f628d06b6c5a37484f20f`。39 个独立输入执行真实
`RpcDispatcher`、`PRODUCT_RPC_REGISTRY`、`PocketBaseProductRpc`、专属
`ProductRelationLookupFileRpc` 和 `PocketBaseClient`；仅 authority HTTP transport 使用脚本。
每项保存请求、两阶段脚本响应/错误、实际请求顺序及完整 Python 响应。输入不从 expected 反推。
目录 revision 使用原生产 `_lookup_revision` 计算，这是公开 revision 契约，未新增本地文件摘要。

## 捕获与原件保护

以下为迁移前固定 producer 的历史捕获方式；当前分支捕获已退役。从该历史工作树根运行，显式设置 `PYTHONPATH` 为该根，再使用模块入口。
可复用共享 uv 环境；source guard 检查根 adapter、专属 handler、client、dispatcher、
Product DTO 和 revision helper 的实际模块来源，拒绝误导入共享 editable 环境的其他工作树。

```powershell
$env:PYTHONPATH=(Get-Location).Path
uv run --frozen --no-sync python -m contracts.v2.generate_lookup_value_page_oracle --write
uv run --frozen --no-sync python -m contracts.v2.generate_lookup_value_page_oracle --check
uv run --frozen --no-sync python -m pytest tests/contract/test_lookup_value_page_python_oracle.py -q --no-cov
```

固定 producer 的 `--write` 仅首次 exclusive 创建。当前迁移分支已删除专属 Python handler/client 路径，
`capture`/`capture_case` 与 `--write` 无条件拒绝；默认与 `--check` 只验证保留的 producer、独立输入
和 typed 边界，不重新捕获或覆盖原件。完整公开输出由 Go Product HTTP 回放消费；Go 领域与 CI 契约不变。

## 执行顺序与错误层次

八个参数全部必需：`collection`、`fieldRef`、`sourceRecordId`、`offset`、`limit`、
`schemaRevision`、`permissionRevision`、`lookupRevision`。六项文本必须为非空字符串；
offset/limit 必须为整数，bool 和 float 均不接受。缺字段、未知字段、类型错误、空文本、
递归凭据、非 Unicode scalar 或超过紧凑 UTF-8 JSON 1 MiB 为 Product `-32602`；
params 非对象先由 JSON-RPC envelope 返回 `-32600`。没有隐含 offset/limit 默认值。

通过 Product 校验后先 GET `/api/vibetable/v1/relations/describe?tableId=collection`。
catalog 必须为 JSON object，lookups 必须为数组，schemaRevision 必须为非空文本。
请求的 schemaRevision 和 permissionRevision 分别等于目录 schemaRevision，lookupRevision
等于原 helper 对 schemaRevision 与完整 lookups 数组的计算结果。三项任一过期均 `-32603`，
只发生目录请求，不再 POST。完整目录顺序和非对象成员均参与 revision；后续查找跳过非对象成员。

按 `physicalName == fieldRef` 找第一个匹配对象，不按稳定 fieldId 查找。
paging 在目录/revision/匹配对象检查之后：offset 必须 >= 0，limit 为 1–500；
负 offset、0 或 501 limit 为 handler `-32603`，仍已 GET。paging 通过后构造分页调用参数时，
才读取匹配对象的非空 `fieldId`；第一项缺 fieldId 时不继续寻找后项。
文本不 trim，组合 Unicode、空白 collection/sourceRecordId 原样传递；脚本接受不代表领域接受。

第二跳 POST `/api/vibetable/v1/lookups/value-page`，body 仅包含 tableId、目录 schemaRevision、
sourceRecordId、映射 fieldId、offset、limit。fieldRef/permissionRevision/lookupRevision 不下传。
目录错误只能记录一跳，分页错误记录两跳；公开错误与 transport 错误分别保留原 `-32150` 投影。

## 透传与 Go DTO 边界

Python client 只要求分页结果为有限 JSON object，原样透传；不按 `lookup.CellValue` 校验。
空对象、额外字段、错误 state/count/provenance 类型也能透传，数组根则 `-32603`。
正常案例保留 value 中 false/0/空串/null/数组/对象/Unicode、provenance 字段、null/空切片、
分页标记、diagnostic 与 state；负结果计数也不被 Python 改写。成功脚本不是 Go calculator
会产生对应领域状态的证明。

原件 `typedGoBoundaries` 逐项标注 CellValue/CatalogResult 无法精确表达的历史 wire 形状：
缺字段、额外字段、畸形根、类型冲突，以及 Python HTTP transport 错误。Go struct 会输出其
必需字段，不能把缺字段当成零值后声称原件等价；动态 value 能表达的 JSON 值不能一并豁免。
nil provenance、bool false、负 int、空数组等仍有明确可表达形态。

补充测试在内存测量恰好 1 MiB 的真实 Product 请求和删减 revision 后的 POST body，验证
超预算/孤立 surrogate 在目录读取前被拒绝，不把巨大字符串写入原件。transport 为脚本，
不把已转发请求称为真实 REST 或领域成功。实际 UI、分页零写入和后续 owner 迁移资格不在
本冻结变更中声明完成。
