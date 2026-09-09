# A5：公开关系 ID 导入与 Lookup 文本导出

本片修复合法公开 `relationId` 无法通过导入预检的问题，并以固定 corpus 验证单值 Relation 导入及 Lookup 只读导出。初始六文件候选基于 `main@3cd6f83202ea0977d690ad71cf7dff25595e4f02`；随后正常合入 `main@25b260394a0a01e8432d23fa3d1a6e8b9b65922f`，补充 S32 真实打包协议资格。fresh CI 结论仍以对应 PR 的最新 head 为准。

## 范围与契约

真实 `relation.describe` 返回 `tableId.fieldId`，原 `collection_profile_from_definition` 却把 `fieldId` 单独作为关系身份。ImportService 的预检比较因而拒绝合法目录 ID；改传单独字段 ID 又无法通过关系目录匹配。修复只改变 Python profile 的身份投影与专用验证器，不改变 Go、owner、路由或导入写入流程。

专用关系身份验证保留既有单段标识符兼容；公开复合形式要求一个表段与一个 `fld_[A-Za-z0-9_-]{8,}` 字段段，总长不超过当前导入 DTO 的 128 字符。它支持 Schema V2 字段 ID 中的连字符，拒绝空段、多点、非法字符和超长值。其他字段标识符继续使用原严格规则。Go 层可能接受的更长身份不在本片扩大到整个导入协议。

初始身份修复的变更文件为 `backend/contracts/data_profile.py`、其相邻 profile/ImportService 测试、`tests/fixtures/data-io/a5-relation-lookup-corpus.json`、现有数据 IO 跨栈测试及本页。固定 corpus 包含两个唯一 Code、中文标签、`=文本` 和空关系。测试通过实际字段 plan/apply 建表、显式 `relationId` 与唯一 Code 匹配导入，随后通过权威 query 检查稳定目标 ID 和 `null`。

Lookup 导出显式携带 `lookupIds` 与 `lookupRevision`，后者来自目录的 `schemaRevision`。CSV/XLSX 中关系原列保留目标 ID，Lookup 列输出标签；XLSX 的 `=文本` 单元格为字符串类型 `s`，不是公式。此证据不承诺原 Code 无配置回灌，也不增加多值关系匹配、猜测显示字段或写 Lookup 的能力。

拒绝路径按实际公开边界分别验证：

- 非唯一匹配字段为 `relation_match_field_not_unique`；无匹配目标为 `relation_match_not_found`。
- Python 导入将计算列列入 `unmatched_columns`，不生成该列写入值；这不是字段值错误响应。
- 直接 Go import-preview 返回 `field.value.invalid`、只读说明及空可提交值；底层说明包含 `field.value.read_only at value`。实际 `mutation.apply` 则返回 HTTP 422 / `mutation.field.read_only`。
- 过期 Lookup revision 返回 `lookup_revision_mismatch`，已有导出文件内容不变。导出和拒绝操作前后的权威行、表标识及 schema/data revision 不变；每次新建的查询 snapshot ID 不作稳定身份比较。

## 初始身份修复验证（历史）

日志均位于 `build/qa/relation-lookup-data-io/`。Python 复用锁定共享环境，执行 `uv run --frozen --no-sync`；未安装依赖或改变锁文件。`--no-cov` 仅用于聚焦测试，不替代完整 CI 覆盖率门禁。

| 验证 | 命令与结果 |
| --- | --- |
| 公开身份旧实现 RED | `python -m pytest tests/backend/adapters/test_pocketbase_data_io.py tests/backend/application/test_import_service.py -k 'public_identity or public_relation_identity or public_catalog_identity or identifiers_still' -q --no-cov`：3 FAIL / 12 PASS，0.94s；`public-id-red.log` |
| 身份边界 RED | `python -m pytest tests/backend/adapters/test_pocketbase_data_io.py -k public_identity -q --no-cov`：3 FAIL / 12 PASS，0.56s；`identity-boundary-red.log`。区分连字符、短字段 ID 与 129 字符总长 |
| 最终相关集合 | `python -m pytest tests/backend/adapters/test_pocketbase_data_io.py tests/backend/application/test_import_service.py tests/backend/adapters/test_pocketbase_relation_io.py tests/backend/contracts/test_lookup_contract.py tests/backend/application/test_export_service.py -q --no-cov`：65 PASS，1.00s；`related-main-complete.log` |
| 最终跨栈集合 | `python build/qa/relation-lookup-data-io/reuse_main_candidate.py`：2/2 PASS，4.37s；`integration-main-complete.log`。运行完整 `test_data_io_interoperability_roundtrip.py`，包含原 falsy/container 用例 |
| 格式 | 四个变更 Python 文件的 `python -m ruff format --check` 与 `python -m ruff check` 均 EXIT 0；`format-complete.log`、`ruff-complete.log` |
| 类型 | `python -m mypy` / `python -m pyright` 检查 production profile、相邻 profile 测试、跨栈测试三个文件均 EXIT 0；`mypy-complete.log`、`pyright-complete.log` |

最终跨栈运行显式复用 PR #312 的真实包内 `resources/sidecar/vibetable-pb.exe`，构建源为 `7a2891033bef34cc04b7f791da3f65d4950b3ffe`。运行前 `git diff --quiet 7a2891033bef34cc04b7f791da3f65d4950b3ffe HEAD -- sidecar` 为 EXIT 0，证明该候选与当前 main 的 Go 源码一致。测试使用本工作树的 Python 修改；不声称包含该 Python 修改的完整产品包已构建。

本地复用入口在已收集的真实 `FixtureDef` 中，将原 `_build_source_sidecar` 替换为立即失败保护，再显式返回已知二进制并打印路径；`--setup-show` 记录模块 fixture 命中。正常仓库 pytest 入口仍每次构建新 sidecar，没有隐式缓存或跳过 CI 构建。module fixture 的输出改为保留在仓库 `build/qa/a5-data-io-*` 新目录，供资格取证。

扩大类型检查到旧 `test_import_service.py` 时，mypy/Pyright 各报两项：既有 `FakeProductMutationPort` 缺少 `preview_paste`，以及旧测试传入可空 `schema_revision`。以 main 原文件进行 mypy shadow 检查得到相同两项，记录在 `mypy-main-baseline.log`；完整四文件检查不能记为通过（`mypy-final.log`、`pyright-final.log`）。未修改该旧夹具或降低规则。

## 保留的失败与限制

原始失败均保留，后续通过不覆盖它们：

| 日志 | 实际失败 |
| --- | --- |
| `integration.log` | corpus 目标 recordId 初写成 16 字符，被 PB 拒绝；已修正为 15 字符 |
| `integration-fixed-id.log` | 合法 Code 预检 3 行中两条非空关系被拒，后确认公开关系身份投影缺陷 |
| `import-diagnostic.log` | 首次尝试本地 fixture 替换未命中收集后的模块，误又构建一次；计划输出也被 pytest 截断 |
| `import-diagnostic-explicit-reuse.log` | 修正为真实 FixtureDef 显式复用旧 `cadf5153` 源候选；两条诊断均为 `relation_id_mismatch` |
| `integration-main-candidate.log` | 已通过导入及两格式导出；夹具误比较整份查询 snapshot，包含每次新生成的 ID/digest |
| `integration-main-stable-revisions.log` | 后续预检误复用已被 apply 消费的 grant；已重新注册独立预检 grant |
| `integration-main-fresh-grant.log` | Go import-preview 期望误写成底层 `field.value.read_only`，实际公开码为 `field.value.invalid` |
| `integration-main-readonly-contract.log` | 只读 message 期望漏掉底层 code/path 前缀；已按完整公开字符串断言 |
| `integration-main-green.log` | 文件名不代表成功：实际 FAIL，拒绝用例漏掉 Mutation 请求必要 `recordId`，尚未到只读检查；已补合法请求 |

初次 source-built 候选被旧 TemporaryDirectory fixture 清理后，进行了一次允许的重建；随后上述 fixture 替换失误造成一次额外构建。旧候选只归属 `cadf5153`，没有冒充最新 main。最终 #312 显式复用阶段有构建 fail-fast 保护，没有继续重建。

初始六文件阶段未执行完整 Python 质量/覆盖率、完整 Go、桌面构建、真实 WPF/WebView2 操作或本候选 fresh CI；后续完整包资格见下节。本片不代表全部 A5 数据互操作验收完成。

## S32 真实打包协议资格

S32 复用同一 `a5-relation-lookup-corpus.json`，经真实 WPF/WebView2 的 Host 文件选择测试入口签发 grant，再调用公开 `data.previewImport`、`task.create`/`task.status`。测试不替换桥接处理器，也不直接写入数据库。两个目标及关系/Lookup 定义通过原有字段 plan/apply 和 mutation 建立。

代表组合覆盖唯一 Code 匹配的两条非空关系与一条空关系、无匹配和非唯一字段拒绝、Lookup 计算列排除导入及 authority 直接写入拒绝；CSV 原关系列保留稳定目标 ID，Lookup 列精确输出中文和 `=文本`。预检不写入源表，导出及拒绝操作前后两端权威记录和 schema/data revision 不变。S32 独立注册为 `32-relation-lookup-data-io`；原有场景及门禁均保留。

当前 UI 的 `dataIoService` 固定发送 `columnMapping: []`、`lookupIds: []`，尚未提供关系匹配配置或 Lookup 导出选列。本场景证明这些公开桥接协议在真实完整包中可用，不证明对应 UI 已闭环。

导出参数明确分开取值：`lookupIds` 来自公开 `lookup.list.definitions[].lookupId`，`lookupRevision` 按现有 Data IO 契约取公开 `schema.describe.schema.schemaRevision`。`lookup.list.lookupRevision` 是另一个计算查询 revision，不能直接传入 `data.export`。两种 revision 的命名和来源差异列为接口语义待办；本片不改变该协议，也不在测试中把两者混称为相同 revision。

### 候选与证据边界

完整候选的运行时源码来自合入 main 的 `fa3253e339f8848fb145d3e057f8c97d4cfb6895`，已包含本片 Python 身份修复，输出为本工作树 `dist/VibeTable.Next/`。后续仅修改场景、manifest 和文档，没有改动包内运行时代码。

`uv run --frozen --no-sync python scripts/build_next.py --release` 完成 Go/Web/Python 后，在 .NET restore 因本机附加 NuGet 源不存在而失败；原始 `build-packaged.log` 保留。删除该命令级无效来源后，本地 `resume_build.py` 只继续现有 staging 的 `_build_desktop`、`verify_sidecar_package`、workspace contracts、manifest、release manifest、自更新 smoke 和 atomic swap；执行的是 `build_next` 原有尾部函数，没有重建已完成的 Go/Web/Python，也没有跳过包校验或自更新 smoke。`build-resume.log` 记录 EXIT 0，scratch 保留作证据。不能把首次完整构建命令写成成功。

CSV/XLSX 的原有两个集成测试复用这份完整包内 sidecar，Python 使用当前工作树模块，fixture 显式 fail-fast 禁止重建 sidecar。它们保留 falsy/container、Unicode、XLSX `=文本` 字符串类型 `s`、过期 revision 拒绝及文件不变等断言。XLSX 证据属于该跨栈集成；真实打包 S32 仅选择 CSV，不合称“双格式打包 UI 通过”。

### 本次验证记录

命令均在本工作树执行，Python 使用锁定共享环境的 `uv run --frozen --no-sync`，`PYTHONPATH` 指向本工作树。新增输出和缓存均留在本工作树。

| 验证 | 结果与证据 |
| --- | --- |
| 相关 Python、manifest/index 与 runner 测试 | 首轮 229 PASS / 2 FAIL；两项 runner 因 Node 依赖尚未安装而失败。按 lock 执行 `npm ci` 后仅重跑两项均 PASS；`related-packaged-fixed.log`、`runner-with-dependencies.log` |
| S32 真实打包协议 | `python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 32-relation-lookup-data-io --evidence-root build/qa/a5-packaged-s32`：1/1 PASS、19 项断言、13.353s；`20260909T124612Z/product-e2e-report.json`，bridge failures/pending 均为零，Host exit 0、进程组及后代为空、端口释放；`s32-schema-revision.log` |
| 固定 corpus CSV/XLSX 集成 | `python build/qa/relation-lookup-data-io/reuse_packaged_candidate.py`：2/2 PASS，18.88s；`integration-packaged.log`，无 sidecar 重建 |
| 索引和语义 | `python scripts/generate_product_e2e_capability_index.py --write`、两个场景模块 `node --check`、`git diff --check` 通过；保留正式 main 证据 gap |

### 保留的失败

- `related-packaged.log` 首次测试命令引用了不存在的测试文件，未收集测试；修正后才有上述结果。
- `build-packaged.log` 记录不存在的本机 NuGet 附加源导致的 NU1301；修正命令级环境后继续已有 staging，没有覆盖原日志。
- S32 首次报告 `build/qa/a5-packaged-s32/20260909T124425Z/product-e2e-report.json` 中合法关系导入已通过，导出因误用 `lookup.list.lookupRevision` 失败。按既有契约改用公开 schema revision 后复用同包验证，不修改 production provider 使测试通过。
- PR #318 旧 CI run `34305718618` 的 S12 恢复后搜索未命中仍保留在 `build/qa/ci-318-resilience/`；本片未修改该搜索路径，正常同步 main 或 S32 通过均不代表该失败已修复。

尚未执行本 head 的完整 Python 覆盖率、完整 Go 测试和 fresh PR CI。正式 main E2E 证据与全部 A5 的日期/时区、locale、路径/grant 等资格仍需各自验收。