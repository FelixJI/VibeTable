# field.settings.describe 原 Python 契约冻结

固定 producer：`2c211088a682163bfdd126eda4528b69cd8419f8`。以下记录原 Python 捕获阶段的历史入口、边界和验证；原29例加授权追加第30例 JSON 保持不变。当前 Python 专属执行路径及捕获器已退役，默认和 `--check` 仅验证保留的原始输入/producer/边界元数据，`--write` 始终拒绝，不重新捕获或覆盖结果。

## 实际入口与边界

原 producer 将该方法放在 `WORKSPACE_CATALOG_METHODS` 历史排除集合中；这个名称不代表实际 Workspace registry/manifest 成员。原执行经 `PRODUCT_RPC_REGISTRY` 的实际闭字段 DTO 与 `PocketBaseProductRpc` 执行，不因此变成 Product current-owner 清单的一员。实际路径为 `RpcDispatcher` → `FieldSettingsDescribeParams` → `PocketBaseProductRpc` → `ProductQuerySchemaRpc._describe_field_settings` → recording transport。捕获器没有调用另一个模型代替真实方法，也没有从 Go 或 expected 推导输入。

`backend/contracts/product_rpc.py` 的实际特例只允许 tableId、fieldId，必需 tableId。两者存在时必须是非空字符串；fieldId 省略合法，null、空字符串、数字均前置拒绝。Product 先执行 JSON/深度/递归凭据/紧凑 UTF-8 1 MiB 校验。RpcRequest 自身先于方法 DTO：数组 params 在真实 dispatcher 返回 -32600；本原件其余闭字段错误为 -32602。

专属 handler 先对 tableId 执行 `_text` 与 `_path_segment`，只允许 `[A-Za-z0-9][A-Za-z0-9_-]{0,127}`，无 trim、Unicode 规范化或百分号解码。合法边界 128 字符通过，129 字符、首字符下划线、空白、非 ASCII、斜杠或百分号编码失败为公开 -32603，HTTP 不执行。fieldId 只经过非空文本检查，不是路径段：空白、组合 Unicode、斜杠和 query 符号均作为 query 值保留。原件锁定“非法 table 路径 + fieldId=null”先返回 -32602，以及“非法 table 路径 + 合法 fieldId + 脚本领域错误”在 HTTP 前返回 -32603。

每次成功访问仅为 GET `/api/vibetable/v2/field-settings/{tableId}`：无 fieldId 时 query 为 `{}`，存在时为 `{fieldId: 原字符串}`；body=null、expectedStatus=[200]，使用既有 session header。本方法不经过 PocketBaseClient 的查询或写入方法；client 仅作为根 adapter 组装依赖。脚本记录 header 之外的完整 method/path/query/body/status，header 本身由协议断言核对，不记录真实凭据。

响应经 `_result_object`，只要求根对象及 JSON 值，随后原样公开；没有套用 `FieldSettingsDescribeResult` 验证。空对象、额外成员、错误字段形状及任意动态 JSON 对象可成功返回。null/array 根为公开 -32603。领域 `field.not_found` 错误由真实 `PocketBaseProductError` 注册映射为 -32150 / product_data_error，保留 code/path/details/retryable；REST occurredAt 不公开。transport 故障同为 -32150，但 kind=product_data_unavailable、code=sidecar.unavailable，不能混为领域错误。

## 原件与 typed Go 范围

30 例完整记录 request、authorityFixture、authorityRequests、公开 response，以及逐例 typedGoBoundary。原 29 例实际捕获为 9 个成功、8 个 -32602、9 个 -32603、1 个 -32600、2 个 -32150；第 30 例追加一个有效非空字段设置成功响应。

正常八字段响应使用 `vibetable.schema.v2`、tableId、fieldId、schemaRevision、dataRevision、definition、capabilities、recommendedDefaultsVersion；definition=null、capabilities=[] 是 Go struct 可表达形状。显式 fieldId 的脚本响应仍可为 null definition，这仅冻结 Python 透传，并不证明真实 schema catalog 会为已有 fieldId 产生 null。

6 例明确边界：空响应对象；开放 definition/额外根成员；错误类型与缺字段对象；null 根；array 根；Python HTTP transport 故障。参考的 typed 结构是 `sidecar/internal/contracts/schemav2wire/generated.go` 的 `FieldSettingsDescribeResult`，生产 REST 当前实际组装 map 并读取 schemaCore/catalog。只有选定更窄 typed port 后这些才成为实现需明确处理的表达边界，不是默认丢弃原行为的豁免。公开领域错误可由既有 field error 映射表达，未标成 transport；动态 JSON 不得一概豁免。

本冻结阶段未调用真实 PocketBase 存储、Go port 或产品 UI，不构成数据读写/发布资格。后续独立 catalog 准入先明确 Product policy 与 Python owner；本次 owner 迁移才将此单方法交给 Go，见 [Go 迁移资格](../../docs/quality/field-settings-describe.md)。

## 原 producer 的捕获完整性（历史）

来源 guard 先检查实际导入符号的文件属于当前工作树 backend，再对这些 handler/client/dispatcher/DTO/error registry/辅助函数/生成 capability 来源执行固定 producer 的 `git diff --quiet --no-ext-diff --no-textconv`。源码漂移、Git 不可用或验证非零都在 capture 入口 fail closed；不新增普通 hash。

transport 所有访问尝试先记账，再检查准确路径、方法、query、header、body、次数。违规独立记录；即使 AssertionError 被真实 dispatcher 转成普通内部错误，捕获器也会在 dispatcher 返回后直接报错。测试覆盖错误路径、第二次访问、外国模块来源、模拟 Git diff 漂移/失败、已有原件禁止覆盖、默认/check 的差异拒绝且不写。

## 原 producer 的验证记录（历史，不适用于当前退役捕获器）

在本冻结工作树根复用已存在共享环境，不重建：

```powershell
# UV_PROJECT_ENVIRONMENT 预先指向现有共享 .venv；不在此创建环境。
$env:UV_NO_SYNC='1'
$env:PYTHONPATH=(Get-Location).Path
$env:PYTHONUTF8='1'
uv run --frozen --no-sync python -m contracts.v2.generate_field_settings_describe_oracle --check
uv run --frozen --no-sync python -m contracts.v2.generate_field_settings_describe_oracle
uv run --frozen --no-sync python -m pytest tests/contract/test_field_settings_describe_python_oracle.py -q --no-cov
uv run --frozen --no-sync ruff check contracts/v2/generate_field_settings_describe_oracle.py tests/contract/test_field_settings_describe_python_oracle.py
uv run --frozen --no-sync ruff format --check contracts/v2/generate_field_settings_describe_oracle.py tests/contract/test_field_settings_describe_python_oracle.py
uv run --frozen --no-sync pyright --venvpath (Split-Path -Parent $env:UV_PROJECT_ENVIRONMENT) contracts/v2/generate_field_settings_describe_oracle.py tests/contract/test_field_settings_describe_python_oracle.py
```

首次 --write EXIT0；首次聚焦 pytest 42 passed（9.07 秒）。显式 venvpath 的 Pyright 0 errors/0 warnings/0 informations。初次原件写入前，Pyright 曾报 requests 列表的 JsonValue 不变性类型错误，改为上下文推断的列表副本后通过；Ruff import 排序提示已自动修正。一次非提升权限 uv 只读统计调用遭 OS 拒绝，未执行任何测试或捕获；改用 PowerShell 读取现有 JSON 完成统计。没有通过重写原件/改 expected 修测试，也没有声称本次有生产修复红→绿。

日志仅放本工作树 build：field-settings-oracle-write.log、field-settings-oracle-pytest-first.log、field-settings-oracle-pyright-first.log。首次 29 例写入后，在下述授权追加之外没有覆盖原件；后续 default/check 只读比较。


## 授权追加的非空设置案例

审查指出原可表达成功响应全部 definition=null/capabilities=[]，无法约束核心设置载荷。因此只追加第 30 例 populated-valid-definition-and-capability。输入按既有 `contracts/schema-v2/fixtures/field-definition.json` 与 `capability.json` 的契约形状独立构造并固化在生成器，包含 number 字段的 identity/lifecycle/value/constraints/storage/display 和一项完整 Capability；displayName/help 保留 Unicode。没有读取 Go 执行输出或旧 expected 生成输入。

第 30 例经固定 producer 的真实 Python capture_case 捕获，typedGoBoundary=null；测试另用 FieldSettingsDescribeResultV2 校验输入有效性，但此模型没有进入捕获执行链。测试断言完整 authority 调用、response 与 fixture 相等，以及 identity、Unicode、默认 null、false 存储选项、精度和能力集合。这仅证明有效 typed 形状可被 Python 透传，不等于真实 PocketBase 存储或产品 UI 资格。

追加前严格检查原文件是 29 例，追加时验证原 cases 文本前缀逐字保持、解析后的前 29 例完全相等；仅追加一个新条目，没有更改原 response、输入、元数据或 producer。此前 42 passed（9.07 秒）属于原 29 例版本，保留为历史证据。

第 30 例增补后的精确复验：

```powershell
uv run --frozen --no-sync python -m pytest tests/contract/test_field_settings_describe_python_oracle.py -q --no-cov -k 'populated or complete_capture or forwarding_projection'
```

结果 4 passed / 40 deselected（1.93 秒）；完整捕获测试在此复验中重新核对全部 30 例。default 与 --check 均 EXIT0，Ruff check/format --check 与显式共享 venvpath Pyright 均 EXIT0、0 errors。未重跑未变化的其余 40 项，也不把历史 42 PASS 写成当前 44 项全绿。日志为本工作树 build/field-settings-oracle-populated-{pytest,check,default,quality}.log。
