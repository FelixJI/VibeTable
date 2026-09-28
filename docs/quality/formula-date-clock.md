# 日期公式与时钟

这些函数由 Go 公式目录提供；在字段设置的公式编辑器中可查签名、说明及示例。日期函数返回 timestamp（可筛选、排序），只有 `TEXT` 返回文本。原有 `dateAdd/dateSubtract/formatDate` 的 UTC 和固定 duration 语义不变。

| 函数 | 语义 |
|---|---|
| `DATE(year, month, day)` | 严格合法公历，年 1–9999，返回 UTC 零点。无效日期不会自动进位。 |
| `YEAR/MONTH/DAY(timestamp [, timezone])` | 提取日历分量，默认 UTC。 |
| `DATEADD(timestamp, integer, unit [, timezone])` | `year/month/day` 日历增减；默认 UTC；月末钳制。当地不存在的 DST 时间报错，重叠时间取较早一次（增量 0 也如此）。 |
| `DATEDIFF(start, end, unit [, timezone])` | `day` 为当地日界差；`month/year` 为按同一月末规则计算的完整单位，反向为相反数。 |
| `TEXT(number, format)` | `0`、`0.0` 到 15 位小数，可加 `%`；中点远离零，大整数保留精确文本。 |
| `TEXT(timestamp, format [, timezone])` | 默认 UTC，只接受下列格式码。 |
| `TODAY([timezone])` | 默认 Windows 已选择的本地时区 `system`，可用字符串常量显式选择 `UTC` 或 IANA 时区；返回对应日历日期的 UTC 零点。 |
| `NOW()` | 当前分钟起点的 UTC timestamp。 |

时区可用 `UTC`、`system` 或 IANA 名称（例如 `Asia/Shanghai`、`America/New_York`）。IANA 数据嵌入程序，断网可用。`TODAY` 的时区必须是字符串常量，使其日界依赖能在求值前确定。已有时间显示设置只改变呈现，不改变旧公式解释。

日期格式闭集：`YYYY-MM-DD`、`YYYY/MM/DD`、`YYYY-MM`、`YYYY`、`MM`、`DD`、`YYYY-MM-DD HH:mm`、`YYYY-MM-DD HH:mm:ss`、`HH:mm`、`HH:mm:ss`。`MM` 是月，`mm` 是分钟；未支持的货币、千分位及其他格式码明确报错，不当作 Go layout 透传。空值沿用已有 nullable/null 错误语义，不转换为零日期。

示例：

```text
到期日期 = DATEADD(DATE(2024, 1, 31), 1, "month")
距到期天数 = DATEDIFF(TODAY("UTC"), {到期日期}, "day")
到期状态 = IF({距到期天数} > 0, "未到期", "已到期")
日期文本 = TEXT({到期日期}, "YYYY/MM/DD")
```

每个计算、预览和查询批次只捕获一次时间。持久计算值沿用既有 dependency watermark，并在有时钟依赖时附加可读日期/分钟周期；普通公式不附加时钟周期。内部表元数据用 `clock_revision` 记录缓存刷新次数，依赖以 `data_revision - clock_revision` 跟踪业务输入；时钟刷新仍推进查询修订，但不会令无时钟依赖的跨表值失效。过期结果显示“计算中”，不会继续作为有效筛选值。时钟变更通过既有后台任务、写协调器及 data.changed 通知生效；只更新过期计算缓存，保留仍有效的普通公式，不改变业务行版本或自动日期，不为时间流逝新增逐行历史。每个表保留当前时钟回填及此前最近两个已完成任务供检查；运行中、失败、取消及普通回填不清理。批量补算使用同一时间，跨页查询/导出不能悄悄混合不同时间周期。

打开/切换工作区时立即检查；运行中每秒只比较一次本机墙钟分钟，只有分钟变化才检查公式定义。睡眠恢复后的首个检查校准当前时间；TODAY 只在所选时区日期变化时过期。关闭工作区取消并等待其任务及定时器，不向其他工作区写回。

验证入口：`go test ./internal/formula ./internal/relatedcomputation`，集成 `TestFormulaClock*`（101 行跨批次、跨分钟/日、时钟回拨、恢复与关闭），正式包 S05 `05-formula-lifecycle`（离线编辑/预览/保存、日期链、排序筛选、CSV 与真实关闭重开）。完整门禁仍以相应 PR 的 required 结果为准；本文件不代替提交绑定的验收证据。
