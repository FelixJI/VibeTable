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
			Description: "返回两个数值中较大者，支持 int 与 double 混算；对关系的聚合请继续使用 MAX({关系}.{字段})。",
			Example:     `MAX(3, 7.5)`},
		{Name: "MIN", Category: "数值", Signature: "MIN(number1: number, number2: number): number",
			Description: "返回两个数值中较小者，支持 int 与 double 混算；对关系的聚合请继续使用 MIN({关系}.{字段})。",
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
	}
	sort.Slice(catalog, func(i, j int) bool {
		if catalog[i].Category != catalog[j].Category {
			return catalog[i].Category < catalog[j].Category
		}
		return catalog[i].Name < catalog[j].Name
	})
	return catalog
}
