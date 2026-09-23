# 数据互操作资格矩阵

本页汇总 VibeTable 数据导入/导出（Data IO）跨格式互操作资格：样本 → 语义 → 覆盖层级 → source/run → 缺口。
冻结预期的唯一声明处是 `tests/fixtures/data-io/a5-interop-matrix-corpus.json`（日期 oracle、Unicode 代表值与码点、
拒绝码、生产者元数据）；实现与文档冲突时先核实实现并在同一 PR 修正，不得按输出反改 oracle。

## 分层口径

（关联页：[unicode-data-io.md](unicode-data-io.md)、[xlsx-native-dates.md](xlsx-native-dates.md)、
[relation-lookup-data-io.md](relation-lookup-data-io.md)；本页是跨层汇总，单域细节以各页为准。）

- **source-built 层**：`tests/integration/` 下用源码构建的 Go sidecar 权威 + Python `FileTaskFixture`
  模拟 Host grant（`tests/backend/host_files_fixture.py`）。它验证 Python Data IO、Go import preview/apply
  与权威 query 的语义，**不代表真实 WPF Host grant owner 集成**。
- **Host 边界层**：`desktop/tests/VibeTable.Desktop.Tests` 的 `NativeProductFileRequestControllerTests`、
  `HostSessionFileBrokerTests`（本机长 Unicode 路径、CON/NUL/missing-source 拒绝、过期授权码、导入
  reservation、export abort 保留原文件）。不得用旧 Python fixture 证据替代本层。
- **包 UI 层**：真实发布包内 WPF/WebView2 的产品 E2E（`tests/e2e/`）。表建立与权威核对可经公开
  bridge；声称 UI 完成的导入/确认/导出必须经真实工具栏与 Host picker，不能注入 bridge 替代。

## 样本 → 语义 → 层级 → source/run → 缺口

本片当前复核：PR #362 的 Codex 修订，基于 `aee561bc`；source-built 相邻集合 43/43、
导出校验与 runner 180/180、生成索引 55/55、Node 2/2；修订后的 S35 run
`h350-codex-exact/20260923T034221Z` 为 1/1、15/15 断言、43.6 秒，实际长路径 296 字符。
本地复用同工作树既有完整包，产品源码/lock 与 base `3abaaf89` 无 diff，包 freshness 检查通过；
此本地样本不冒充绑定新 head 的 CI 候选，完整当前资格以 PR checks/review 为准。

| 样本（冻结语义） | source-built | Host 边界 | 包 UI | source/run | 缺口 |
|---|---|---|---|---|---|
| falsy/container：空白数字 `null` vs 空文本 `""`、`0`/`false`/`{}`/`[]`、select 稳定 optionId、公式样文本 | `test_data_io_interoperability_roundtrip.py` + `a5-falsy-container-corpus.json`（CSV BOM→authority→CSV/XLSX） | — | S07 原导入故障修复（历史） | #318 语料；2026-09-23 本片重跑通过 | 无新增；互操作导出把空白与空文本都写空单元格，不作为无损备份 |
| Unicode：NFC/NFD、Emoji 肤色/ZWJ、CJK U+20000、RTL U+200F、I/i/İ/ı/ß/SS 码点 | `test_unicode_data_io_roundtrip.py`（csv/csv-bom/xlsx 三输入→authority→两格式导出） | — | S35（2026-09 本片新增）：BOM CSV 代表值经真实 UI 导入并两格式导出 | 2026-09-23 本片通过 | 数据保真已证明；不声称 RTL 排版、locale 排序/搜索或本地数字解析 |
| 日期/时间戳：`2026-08-29`；无时区 `2026-08-29 14:05:06.123` 按 UTC civil time→preview `2026-08-29T14:05:06.123Z`；`2026-08-29T00:00:00+08:00`→`2026-08-28T16:00:00Z` | `test_unicode_data_io_roundtrip.py::test_xlsx_native_dates_...`（preview/apply/query，本片补齐 CSV/XLSX 导出断点：导出器保留 query wire 文本，XLSX 为字符串单元格） | — | S35（本片）：XLSX 原生日期/毫秒经真实 UI 导入 + 两格式导出 | 2026-09-23 本片通过 | 不猜本机时区；导出为文本而非原生日期是既有契约 |
| 1900 系统原生 serial 59/60 歧义、原生 time/timedelta | `test_import_service.py`（`import_ambiguous_excel_date`、`import_unsupported_excel_time`） | — | —（错误路径不进 UI 成功矩阵） | 既有覆盖，语料 `rejections` 段引用 | 不扩大支持；可改用 ISO 文本 |
| 关系/Lookup：唯一 Code→稳定关系 ID、Lookup 文本导出、非唯一/无匹配拒绝、计算列排除 | `test_data_io_interoperability_roundtrip.py`（关系 corpus） | — | S34 `34-relation-lookup-data-io`（#349：bridge + 真实 UI 闭环） | #349 head `e5d22a80`、CI run 35739549990 全部 13 jobs 成功（历史 run，非本片声明）；配置 UI 见 [relation-lookup-data-io.md](relation-lookup-data-io.md) | S34 证据属 #349 合并时点；本片不重跑其完整资格 |
| 系统字段：id/autoDate 导入排除、真实 authority ownership、两格式导出 | `test_data_io_system_fields.py` + system corpus | — | 随 S34/S35 导出核对间接覆盖 | 既有覆盖 | 不把排除列改成伪造字段报错 |
| 路径/grant：过期/不同/消耗/replay 拒绝、280 字符 Unicode 长路径两格式 | `test_data_io_path_grants.py`（**Python FileTaskFixture grant 模拟，非真实 WPF Host owner 集成**） | `NativeProductFileRequestControllerTests`/`HostSessionFileBrokerTests`：真实 native callback 过期码、导入 reservation、export abort 保留原文件 | S35（本片）：>260 字符深路径经真实 Host picker grant 导入 | integration 层 2026-09-23 本片重跑通过；Host 层既有 | 真实 Host grant 的完整 owner 集成证据归 #348 边界；本片不复制另一套 authority |
| 取消/原子性：预览确认前取消、拒绝前后权威不变、commit 前 export abort 旧文件保留 | `test_data_io_interoperability_roundtrip.py`（stale lookup revision 拒绝 + 原输出保留） | `test_host_export_cleanup.py`（确定性取消因果） | S35（本片）：一次真实 UI 取消（authority 与 revision 不变）后重新选择确认 | 2026-09-23 本片通过 | 取消语义按既有契约；不为制造取消引入任意 sleep |

## 声明式语料与生产者元数据

`a5-interop-matrix-corpus.json` 记录：日期 case 的独立预期（preview DTO、query wire、导出文本，且
导出文本恒等于 query wire）、Unicode 代表值与逐码点序列（复用 #318 冻结码点，不另造矩阵）、拒绝码
及其既有覆盖入口、生产者（openpyxl 3.1.5 + et-xmlfile 2.0.0，uv.lock 锁定；CSV UTF-8 BOM；XLSX 1900
epoch）。`test_unicode_data_io_roundtrip.py::test_interop_matrix_corpus_matches_frozen_oracles` 在每次
运行时核对语料与冻结码点、openpyxl/et-xmlfile 锁定版本不漂移。S35 使用 runner 的
`sys.executable` 调用 `tests/e2e/data_io_workbook.py`，由锁定 openpyxl 按目标字段 physicalName
生成原生日期、毫秒和字符串公式样文本源；不手写 ZIP、OOXML 或 Excel serial。
每次运行的 `35-producer-metadata.json` 记录实际 Python、openpyxl、et-xmlfile、locale、编码、
时区以及源文件路径。locale/时区仅为环境记录，不代表支持区域排序、本地数字解析或时区猜测。

S35 对 CSV 使用标准库 csv，对 XLSX 使用 openpyxl 独立读取，按物理列名比较完整行多重集合，
保留重复次数且拒绝缺行、多行、日期串行或重复 note；XLSX 非空单元格还必须是字符串，不能是
公式、数值或原生日期。`test_data_io_interoperability.py` 用交换日期、重复 note、缺行、多行和
错误单元格类型证明拒绝路径，Node 相邻测试覆盖 authority 行集合检查。

历史 GLM 样本绑定 head `aee561bc4b73bfbff3243527c75d30234da556f9`，本地 run
`h350-s35-first/20260923T030010Z`（1/1、15 个断言）；该结果早于精确导出断言修订，不能替代
修订后的新 head CI 和 S35 验证。

## 历史段

本页首版属于 PR #140 的 A5：固定 falsy/container corpus 通过 source-built sidecar 验证一行带 UTF-8
BOM 的 CSV 依次经过 Python Data IO、Go import preview、原子 apply、权威 query，再导出 CSV 与 XLSX。
当时明确“不覆盖日期、datetime、时区、Excel serial、relation、lookup、RTL、locale case、路径、grant、
取消或 packaged UI”。上述缺口由后续资格片与 2026-09 本片补齐（见上表）；早期 `dataIoService 固定
空数组` 等描述属当时实现快照，不代表当前行为。

## 验证入口

- source-built 相邻集合：`uv run --frozen --no-sync python -m pytest tests/integration/test_unicode_data_io_roundtrip.py tests/integration/test_data_io_interoperability_roundtrip.py tests/integration/test_data_io_system_fields.py tests/integration/test_data_io_path_grants.py tests/backend/application/test_import_service.py --no-cov -q`
- 源文件与导出校验契约：`uv run --frozen --no-sync python -m pytest tests/e2e/test_data_io_interoperability.py --no-cov -q`；Node：`node --test --test-concurrency=1 tests/e2e/data_io_interoperability.test.mjs`
- 包 UI：`uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --scenario 35-data-io-interoperability`；完整 CI 由独立的 `data-io` lane 对 S34/S35 执行 `product-e2e-data-io` stage，其余场景在 `resilience` lane。两个分片的成功证据必须精确覆盖当前 manifest（见 `qa/release_eligibility.py`）。

聚焦入口不统计全后端覆盖率；完整 CI 仍执行仓库既有的 85% 覆盖率门禁。
