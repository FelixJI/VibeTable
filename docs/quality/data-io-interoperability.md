# 数据互操作 falsy/container 资格边界

本资格片属于 PR #140 的 A5。固定 corpus 通过 source-built sidecar 验证一行带 UTF-8 BOM 的 CSV
依次经过 Python Data IO、Go import preview、原子 apply、权威 query，再导出 CSV 与 XLSX。

已证明的产品值区分为：

- 空白数字为 `null`，空文本为已提供的 `""`，权威 query 必须区分二者；`0`、`false`、空对象 `{}`
  和空数组 `[]` 不得按 truthiness 折叠为空白；
- select label 只在导入边界解析，权威值与导出值均为稳定 optionId；
- `=1+1` 作为普通文本保持原样，XLSX 单元格必须是字符串而不是公式；互操作导出允许把空白与空文本
  都表示为空单元格，因此不作为无损备份。

本片不覆盖日期、datetime、时区、Excel serial、relation、lookup、RTL、locale case、路径、grant、
取消或 packaged UI。日期容器由独立 A5a 资格片负责；更宽字段组合先继续在 source-built 层收敛，再选择
少量代表组合进入 packaged E2E。

验证入口：`tests/integration/test_data_io_interoperability_roundtrip.py`。
