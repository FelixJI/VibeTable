package formula

import "sort"

// FunctionInfo is one entry of the authoritative Go function catalog. The
// frontend receives this catalog through the existing field-settings /
// formula responses; it never keeps a second copy of the semantics.
type FunctionInfo struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Signature   string `json:"signature"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

// FunctionCatalog returns every uppercase formula function actually
// implemented by this package, ordered by category then name so transports
// get a stable list.
func FunctionCatalog() []FunctionInfo {
	catalog := []FunctionInfo{
		{Name: "TABLE", Category: "集合与跨表", Signature: "TABLE(table): records", Description: "来源选择器绑定的静态表范围，只能用于筛选、投影或计数。", Example: "COUNT(TABLE({出货}))"},
		{Name: "FILTER", Category: "集合与跨表", Signature: "FILTER(range, predicate): range", Description: "按来源 CurrentValue 与外层字段的类型化谓词筛选完整范围。", Example: "FILTER([1.0, 2.0], CurrentValue > 1.0)"},
		{Name: "PROJECT", Category: "集合与跨表", Signature: "PROJECT(records, CurrentValue.field): list", Description: "投影一个静态来源字段，保留空值与元素顺序。", Example: "SUM(PROJECT(TABLE({出货}), CurrentValue.{金额}))"},
		{Name: "SUMIF", Category: "集合与跨表", Signature: "SUMIF(records, predicate, CurrentValue.field): number", Description: "筛选后对数值字段求和；空集合返回 0。", Example: "SUMIF(TABLE({出货}), CurrentValue.{金额} > 0.0, CurrentValue.{金额})"},
		{Name: "COUNTIF", Category: "集合与跨表", Signature: "COUNTIF(range, predicate): int", Description: "统计完整范围内满足谓词的元素/记录数量。", Example: "COUNTIF([1.0, 2.0], CurrentValue > 1.0)"},
		{Name: "SUM", Category: "集合与跨表", Signature: "SUM(numbers): number", Description: "数值列表求和，忽略空值；空集合为 0。", Example: "SUM([1.0, 2.0])"},
		{Name: "AVERAGE", Category: "集合与跨表", Signature: "AVERAGE(numbers): number?", Description: "数值列表求平均，忽略空值；无数值返回 null。", Example: "AVERAGE([1.0, 2.0])"},
		{Name: "COUNT", Category: "集合与跨表", Signature: "COUNT(range): int", Description: "统计列表元素或记录数量，包含空值。", Example: "COUNT([1.0, 2.0])"},
		{Name: "COUNTA", Category: "集合与跨表", Signature: "COUNTA(values): int", Description: "统计非 null、非空字符串元素；0 和 false 计入。", Example: "COUNTA([false, true])"},
		{Name: "UNIQUE", Category: "集合与跨表", Signature: "UNIQUE(values): list", Description: "按首次出现顺序去重，保留空值。", Example: "UNIQUE([\"A\", \"A\", \"B\"])"},
		{Name: "ARRAYJOIN", Category: "集合与跨表", Signature: "ARRAYJOIN(values, separator): string", Description: "用指定分隔符连接标量列表，空值保留为空项。", Example: "ARRAYJOIN([\"A\", \"B\"], \", \")"},
		{Name: "TODAY", Category: "日期与时间", Signature: "TODAY([timezone: string]): timestamp", Description: "按 Windows 本地时区返回今天（UTC 零点日期）；可用字符串常量 UTC、system 或 IANA 时区覆盖，按当地日界更新。", Example: `TODAY("UTC")`},
		{Name: "NOW", Category: "日期与时间", Signature: "NOW(): timestamp", Description: "返回当前分钟起点（UTC）；同批次固定时间，每分钟刷新。", Example: `NOW()`},
		{Name: "AND", Category: "逻辑与错误", Signature: "AND(condition1: bool, ..., condition8: bool): bool",
			Description: "从左到右短路求值全部布尔参数，全部为真返回 true；2 到 8 个参数。",
			Example:     `AND(1 < 2, 2 < 3)`},
		{Name: "IF", Category: "逻辑与错误", Signature: "IF(condition: bool, then: T, else: T): T",
			Description: "condition 为真返回 then，否则返回 else；分支惰性求值，同型分支，int/double 统一为 double。",
			Example:     `IF(1 > 0, "是", "否")`},
		{Name: "IFERROR", Category: "逻辑与错误", Signature: "IFERROR(value: T, fallback: T): T",
			Description: "value 在运行期发生除零或数值溢出时返回 fallback；引用、类型、取消、资源等错误不被吸收。",
			Example:     `IFERROR(1 / 0, 0.0)`},
		{Name: "IFS", Category: "逻辑与错误", Signature: "IFS(condition1: bool, value1: T, ..., condition4: bool, value4: T): T",
			Description: "按顺序返回第一个为真条件对应的值，条件与值均惰性求值；最多 4 组，全部未命中返回 null。",
			Example:     `IFS(2 > 3, "大", 2 < 3, "小")`},
		{Name: "ISBLANK", Category: "逻辑与错误", Signature: "ISBLANK(value: any): bool",
			Description: "仅当值为 null 或空字符串时返回 true；0、false、空白字符串都不是空。",
			Example:     `ISBLANK("")`},
		{Name: "ISERROR", Category: "逻辑与错误", Signature: "ISERROR(value: any): bool",
			Description: "仅检测运行期除零与数值溢出并返回 true；其他错误原样向外传播。",
			Example:     `ISERROR(1.0 / 0.0)`},
		{Name: "NOT", Category: "逻辑与错误", Signature: "NOT(condition: bool): bool",
			Description: "对布尔值取反。",
			Example:     `NOT(false)`},
		{Name: "OR", Category: "逻辑与错误", Signature: "OR(condition1: bool, ..., condition8: bool): bool",
			Description: "从左到右短路求值全部布尔参数，任一为真返回 true；2 到 8 个参数。",
			Example:     `OR(1 > 2, 2 > 3)`},
		{Name: "ABS", Category: "数值", Signature: "ABS(number: number): number",
			Description: "返回数值的绝对值，沿用既有溢出保护。",
			Example:     `ABS(-3)`},
		{Name: "MAX", Category: "数值", Signature: "MAX(number1: number, number2: number): number",
			Description: "返回两个数值中较大者，支持 int 与 double 混算；一个数值列表可用 MAX(list)，空列表返回 null；Relation 快捷式保持原义。",
			Example:     `MAX(3, 7.5)`},
		{Name: "MIN", Category: "数值", Signature: "MIN(number1: number, number2: number): number",
			Description: "返回两个数值中较小者，支持 int 与 double 混算；一个数值列表可用 MIN(list)，空列表返回 null；Relation 快捷式保持原义。",
			Example:     `MIN(3, 7)`},
		{Name: "ROUND", Category: "数值", Signature: "ROUND(number: number, digits: int): number",
			Description: "按 digits（-15 到 15）四舍五入，中点远离零；保留 int/double 类型。",
			Example:     `ROUND(3.14159, 2)`},
		{Name: "CONCATENATE", Category: "文本", Signature: "CONCATENATE(value1: any, ..., value8: any): string",
			Description: "拼接 2 到 8 个值的文本形式，null 被跳过，与既有 concat 语义一致。",
			Example:     `CONCATENATE("Vibe", "Table")`},
		{Name: "LEFT", Category: "文本", Signature: "LEFT(text: string, count: int): string",
			Description: "返回 text 开头 count 个 Unicode 码点；负数报错，超出长度返回全文。",
			Example:     `LEFT("VibeTable", 4)`},
		{Name: "LEN", Category: "文本", Signature: "LEN(text: string): int",
			Description: "返回 text 的 Unicode 码点数。",
			Example:     `LEN("VibeTable")`},
		{Name: "LOWER", Category: "文本", Signature: "LOWER(text: string): string",
			Description: "按 Unicode 规则转换为小写，沿用既有语义。",
			Example:     `LOWER("VibeTable")`},
		{Name: "MID", Category: "文本", Signature: "MID(text: string, start: int, count: int): string",
			Description: "从 1 起始的 start 位置截取 count 个 Unicode 码点；start 小于 1 或负数 count 报错，越界返回空串或剩余字符。",
			Example:     `MID("VibeTable", 5, 5)`},
		{Name: "RIGHT", Category: "文本", Signature: "RIGHT(text: string, count: int): string",
			Description: "返回 text 末尾 count 个 Unicode 码点；负数报错，超出长度返回全文。",
			Example:     `RIGHT("VibeTable", 5)`},
		{Name: "TRIM", Category: "文本", Signature: "TRIM(text: string): string",
			Description: "去除首尾 Unicode 空白，沿用既有语义。",
			Example:     `TRIM("  VibeTable  ")`},
		{Name: "UPPER", Category: "文本", Signature: "UPPER(text: string): string",
			Description: "按 Unicode 规则转换为大写，沿用既有语义。",
			Example:     `UPPER("VibeTable")`},
		{Name: "TEXT", Category: "文本", Signature: "TEXT(value: number | timestamp, format: string [, timezone: string]): string",
			Description: "数值格式支持 0、0.0 至多 15 位小数及同形百分号，中点远离零舍入；时间戳格式支持 YYYY-MM-DD、YYYY/MM/DD、YYYY-MM、YYYY、MM、DD、YYYY-MM-DD HH:mm、YYYY-MM-DD HH:mm:ss、HH:mm、HH:mm:ss（MM 为月、mm 为分钟）。分组、货币与其他 Excel 代码及未知格式报错。",
			Example:     `TEXT(3.14159, "0.00")`},
		{Name: "DATE", Category: "日期", Signature: "DATE(year: int, month: int, day: int): timestamp",
			Description: "按严格合法公历（1–9999 年）构造 UTC 零点时间戳；月、日超出当月范围不自动进位，直接报错。",
			Example:     `DATE(2024, 2, 29)`},
		{Name: "DATEADD", Category: "日期", Signature: "DATEADD(timestamp: timestamp, amount: int, unit: string [, timezone: string]): timestamp",
			Description: "按 year/month/day 日历单位加减时间戳，默认 UTC，可显式传 \"UTC\"、\"system\" 或 IANA 时区。月/年加法钳制到目标月末，日加法保留本地墙钟时间；结果落入 DST 缺失时间段报错，DST 重叠墙钟取较早一次出现（0 量同样适用）；与旧 dateAdd 的固定时长语义不混用。",
			Example:     `DATEADD(DATE(2024, 1, 31), 1, "month")`},
		{Name: "DATEDIFF", Category: "日期", Signature: "DATEDIFF(start: timestamp, end: timestamp, unit: string [, timezone: string]): int",
			Description: "计算两个时间戳的日历差，默认 UTC，可显式传 \"UTC\"、\"system\" 或 IANA 时区。day 为当地日历日界差；month/year 为带方向的完整日历单位，与 DATEADD 同一月末钳制规则判定，交换参数结果恰为相反数。",
			Example:     `DATEDIFF(DATE(2024, 1, 1), DATE(2024, 3, 1), "day")`},
		{Name: "DAY", Category: "日期", Signature: "DAY(timestamp: timestamp [, timezone: string]): int",
			Description: "返回时间戳在指定时区的当地日（1–31），默认 UTC，可显式传 \"UTC\"、\"system\" 或 IANA 时区。",
			Example:     `DAY(DATE(2024, 5, 6))`},
		{Name: "MONTH", Category: "日期", Signature: "MONTH(timestamp: timestamp [, timezone: string]): int",
			Description: "返回时间戳在指定时区的当地月（1–12），默认 UTC，可显式传 \"UTC\"、\"system\" 或 IANA 时区。",
			Example:     `MONTH(DATE(2024, 5, 6))`},
		{Name: "YEAR", Category: "日期", Signature: "YEAR(timestamp: timestamp [, timezone: string]): int",
			Description: "返回时间戳在指定时区的当地年，默认 UTC，可显式传 \"UTC\"、\"system\" 或 IANA 时区。",
			Example:     `YEAR(DATE(2024, 5, 6))`},
	}
	sort.Slice(catalog, func(i, j int) bool {
		if catalog[i].Category != catalog[j].Category {
			return catalog[i].Category < catalog[j].Category
		}
		return catalog[i].Name < catalog[j].Name
	})
	return catalog
}
