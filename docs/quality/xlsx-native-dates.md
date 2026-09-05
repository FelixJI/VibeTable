# XLSX 原生日期的互操作边界

Python `SourceFile` 仅把 XLSX 容器值投影为 JSON 标量；目标字段转换、空白、约束和持久化继续由 Go
import preview 与 FieldValueKernel 决定。本片属于 PR #140 的 A5，不代表完整互操作或 packaged 资格通过。

- 原生日期输出 ISO 日期文本。日期格式的无时区午夜 `datetime` 可输出 `YYYY-MM-DD`；带时间格式或
  非午夜的值保留完整时间，即使 Excel 界面只显示日期，也不静默丢弃时间成分。
- 原生无时区时间戳保留为空格分隔文本；已有 Go 规则将它按 UTC civil time 解释，不猜测本机或
  workspace 时区。带时区的文本保持原样交给 Go；日期对象若携带 offset，也不得截为日期。
- OpenPyXL 对数值日期读取使用毫秒精度；不承诺恢复原始 serial 的更细精度。`dateTime` 的毫秒与
  显式 offset 通过 source-built sidecar 的 preview、apply、权威 query 回归核对。
- Excel 1900 日期系统的 serial 59 与 60 均被 OpenPyXL 解析为 `1900-02-28`。因此该系统下解析到
  这一天的原生值统一拒绝，返回 `import_ambiguous_excel_date` 和 sheet/row/column；这包括合法但
  无法区分的 serial 59，并非声称识别出了 serial 60。可改用 ISO 日期文本。1904 系统及文本不受此拒绝影响。
- 原生纯时间与时长暂不支持，返回 `import_unsupported_excel_time` 及单元格位置；可以使用符合
  目标字段契约的 ISO 文本，不把时长隐式变成日期。更完整的时间/时长策略仍留后续 A5。
- 继续以 `data_only=True` 读取公式缓存，不计算公式；没有缓存的公式仍为空白。普通文本、数值、
  布尔和空白保持原值，公式样文本不被执行。

验证入口：`tests/backend/application/test_import_service.py` 的真实 Workbook/HTTP 序列化用例，及
`tests/integration/test_unicode_data_io_roundtrip.py` 的 source-built sidecar 日期用例。CSV 行为、
通用 JSON transport、Go 字段规则、依赖与 lock 均未调整；未把本机验证写成正式 packaged 验收。
