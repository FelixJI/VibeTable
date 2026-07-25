# VibeTable 公式引擎选择：浏览器、Directus 与 PocketBase

日期：2026-07-24  
范围：VibeTable 单机桌面应用、Vue 浏览器前端、Directus Node API 扩展，以及 PocketBase Go / JSVM 扩展。  
资料边界：只采用项目官方文档、官方源码仓库和包元数据。

## 结论

### 1. 前端不需要成为公式执行方

VibeTable 是本机桌面应用，后端也运行在本机。对于保存后的计算字段，推荐只有后端是权威执行方：

```text
编辑源字段
  -> POST/PATCH 源字段
  -> 后端 Hook 在保存事务内同步计算
  -> 保存普通、只读的结果字段
  -> API 返回持久化后的结果
```

如果产品希望用户在还没保存时看到试算结果，也不必在前端复制一套 evaluator。前端可以 debounce 调用本机后端的：

```text
POST /vibetable/formulas/evaluate
{
  "formula": "...",
  "draft": { ...当前未保存的行数据... }
}
```

该端点只试算、不持久化，并与写入 Hook 调用同一个解析和执行模块。这样同时获得即时反馈和单一语义来源。公式执行本身通常不是主要耗时，实际体验需要用“前端到 localhost API 的完整往返”基准确认；在有数据前，不应为了假设中的延迟先维护两套运行时。

只有在以下需求成立时，才值得增加浏览器本地计算：

- 必须逐按键、无网络往返地更新结果；
- 后端可能暂时未启动或产品要求离线编辑；
- 公式编辑器需要本地 AST 补全或复杂静态诊断。

即使增加，本地结果也只能标为“预览”：客户端不提交权威计算值，后端仍重新计算，并以保存响应覆盖预览。

### 2. Formula.js 不是公式解析器

当前的 [`@formulajs/formulajs`](https://github.com/formulajs/formulajs) 是 Excel 风格函数实现集合。它可以执行：

```js
formulajs.SUM([1, 2, 3])
formulajs.DATE(2026, 7, 24)
```

但它本身不负责把 `SUM({{price}}, {{tax}})` 解析为 AST，也不负责提取字段依赖、检测循环或限制语法。其包元数据提供浏览器 UMD、Node ESM 和 CJS 构建，并采用 MIT 许可证，但没有 parser/AST API。[`package.json`](https://github.com/formulajs/formulajs/blob/master/package.json)

Directus Labs Calculated Fields 也证明了这一点：它另外使用 Peggy grammar 生成 parser，把公式解析成自有 AST，再由一个 `switch` 解释器递归执行，只有函数节点才调用 Formula.js。[grammar](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/src/lib/parser/grammar.pegjs)、[evaluator](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/src/lib/evaluate-formula.ts)、[依赖提取](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/src/lib/extract-fields-from-ast.ts)

因此“采用 Formula.js”至少还缺：

- 语法和 parser；
- AST 类型；
- 字段依赖提取；
- 类型、空值、错误和日期语义；
- 函数白名单；
- 计算成本限制；
- 公式版本和历史数据重算。

### 3. 跨 Directus / PocketBase 的首选取决于 PocketBase 扩展方式

#### 推荐路径 A：自有 PocketBase Go 二进制 + CEL

如果允许 VibeTable 打包自己的 PocketBase 可执行文件，推荐把 **CEL 作为公式语言**：

- PocketBase 使用 [`cel-go`](https://github.com/cel-expr/cel-go)；
- Directus Node 和浏览器工具使用 [`@bufbuild/cel`](https://github.com/bufbuild/cel-es)；
- VibeTable 在 CEL 上注册一组固定、版本化的表格函数；
- 两种实现使用同一套 golden / conformance 测试。

CEL 是非图灵完备、无副作用的表达式语言；官方工作流明确包含 parse、type-check、AST 和 evaluate，并建议预先编译、缓存程序。[cel-go README](https://github.com/cel-expr/cel-go#common-expression-language)、[CEL language definition](https://github.com/cel-expr/cel-spec/blob/master/doc/langdef.md)

PocketBase 官方把 Go 扩展定位为需要更多控制和任意第三方 Go 库时的方案，并支持编译成可移植静态可执行文件。这与桌面应用打包后端的模式相符。[PocketBase as framework](https://pocketbase.io/docs/use-as-framework/)、[Go overview](https://pocketbase.io/docs/go-overview/)

注意：当前 `@bufbuild/cel` 0.6.0 的 TypeScript 编译目标是 ES2023，不能直接假定可在 PocketBase Goja 中运行；PocketBase 端应使用 `cel-go`，而不是把这个 npm 包塞进 `pb_hooks`。[`@bufbuild/cel` package](https://github.com/bufbuild/cel-es/blob/main/packages/cel/package.json)、[编译配置](https://github.com/bufbuild/cel-es/blob/main/tsconfig.base.json)

CEL 的代价是它不是 Excel 方言。`SUM`、`ROUND`、`DATE`、`TEXT` 等表格函数需要由 VibeTable 定义，并保证 Go/TypeScript 两端语义一致。适合第一版的公式范围是同一行、确定性标量表达式，而不是完整 Excel 工作簿。

#### 推荐路径 B：原版 PocketBase + JSVM

如果必须使用 PocketBase 官方预编译二进制，只能在 `pb_hooks` 中执行 JavaScript，则更适合建立一个小型的 `@vibetable/formula-runtime`：

- 自有、受限的 grammar 和 AST；
- AST 解释执行，不使用 `eval` / `new Function`；
- 构建 Node ESM、浏览器 ESM 和面向 Goja 的 ES5 CommonJS；
- 只暴露 VibeTable 明确允许的函数；
- Formula.js 只作为可选函数实现来源，不承担解析。

PocketBase 官方说明 JSVM 是 Goja 驱动的 ES5 环境，虽然实现了大部分 ES6，但并不完全兼容规范；它只支持 CommonJS，并且不是 Node 或浏览器，依赖 `window`、`fs`、`fetch`、`buffer` 等运行时 API 的 npm 包可能失败。[PocketBase JS overview](https://pocketbase.io/docs/js-overview/)

因此，任何 npm 公式库在 JSVM 中是否可用，都必须以真实 PocketBase 进程的 POC 为准，不能仅凭“支持 Node/浏览器”推断。

## 候选对比

| 候选 | Parser / AST / 依赖 | 函数覆盖 | 浏览器 / Node | PocketBase JSVM | 许可证 | 判断 |
|---|---|---|---|---|---|---|
| Formula.js 4.6 | 没有 parser、AST 或依赖提取 | Excel 风格数学、日期、文本、逻辑较丰富 | 官方提供 UMD、ESM、CJS | 打包后可能可用，但不能单独执行公式文本；必须 POC | MIT | 只作为函数库 |
| HyperFormula 3.3 | 完整 AST、工作簿依赖图、循环与增量重算 | 418 个函数，最完整 | 官方支持浏览器和 Node | 无官方 Goja 支持，模型和依赖也偏重 | GPL-3.0-only 或商业授权 | 不适合当前内核 |
| mathjs 15.2 | 公开 AST，支持 `filter` / `traverse` / `SymbolNode` | 数学、逻辑和数值类型强；不是表格日期函数库 | 官方声明兼容 ES5 浏览器和 Node | 有 POC 价值，但 PocketBase 不是受支持的浏览器/Node 环境 | Apache-2.0 | 可做 JSVM 技术验证 |
| JEXL 2.3 | 内部有 AST；公开 API 没有稳定依赖提取 | 基础运算、逻辑、字符串拼接；日期/表格函数需自定义 | CJS，Babel 目标 IE11 | `evalSync` 可能运行，未获官方验证 | MIT | 不宜把私有 AST 当产品协议 |
| expr-eval 2.0.2 | `variables({withMembers:true})` 可提取依赖 | 数学/逻辑为主，日期和表格函数不足 | 零依赖，CJS/ESM，代码偏 ES5 | 候选中最容易做 Goja POC | MIT | 适合原型，不宜直接定为长期语言 |
| CEL / cel-go | 公开 AST、类型检查、跨语言规范 | 逻辑、列表、映射、字符串、timestamp/duration；Excel 函数需扩展 | `@bufbuild/cel` 支持 ECMAScript | npm 包不适合 Goja；`cel-go` 适合自有 Go 二进制 | Apache-2.0 | 跨后端长期首选 |

## 候选详析

### Formula.js

优点：

- Directus Labs 已采用，产品语法容易接近用户熟悉的 Excel；
- 日期、文本、逻辑和统计函数覆盖比通用数学 parser 更适合表格；
- MIT，Node 和浏览器构建齐全。

问题：

- 它接收已经求值的 JavaScript 参数，不解析公式文本；
- 不提供 AST 和字段依赖；
- 日期语义与 Excel 并不完全相同。例如官方 README 明确说明 `DATE` 等函数返回 JavaScript `Date`，而不是 Excel 序列号，日期相减结果也因此不同。[Formula.js README](https://github.com/formulajs/formulajs#differences-between-excel-functions-and-formulajs)
- Node CJS/ESM 构建没有经过面向 ES5 的 Babel 输出；浏览器 UMD 才经过 Babel。即使使用 UMD，也不能据此推断 PocketBase Goja 兼容。[Rollup config](https://github.com/formulajs/formulajs/blob/master/rollup.config.js)

结论：Formula.js 可作为允许函数的实现来源，但不应成为 VibeTable 公式语言架构本身。

### HyperFormula

HyperFormula 是真正完整的 headless spreadsheet engine：官方描述包含公式 parser、AST、依赖图和增量求值，可用于浏览器和 Node。[官方文档](https://hyperformula.handsontable.com/docs/)、[key concepts](https://hyperformula.handsontable.com/docs/guide/key-concepts.html)、[dependency graph](https://hyperformula.handsontable.com/docs/guide/dependency-graph.html)

它内置 418 个函数，包含日期、文本、逻辑、查询和聚合。[built-in functions](https://hyperformula.handsontable.com/docs/guide/built-in-functions.html)

但它的核心模型是工作簿、sheet、cell address 和 range。VibeTable 的计算字段是“每条记录按命名字段计算”，若为每次 API 写入维护工作簿坐标，会引入不必要的状态映射。再加上 GPL-3.0-only / 商业授权，以及没有 PocketBase Goja 支持，不推荐作为双后端共享内核。[package metadata](https://github.com/handsontable/hyperformula/blob/master/package.json)

### mathjs

mathjs 的 `parse()` 返回公开 expression tree，节点支持遍历和筛选，因此可以从 `SymbolNode` 提取字段依赖。[expression trees](https://mathjs.org/docs/expressions/expression_trees.html)

安全方面，它从 v4 起不再使用 `eval`；但官方仍提醒：

- 用户表达式可能触发未知安全漏洞；
- `import`、`createUnit`、`evaluate`、`parse` 等高风险能力需要禁用；
- 大型矩阵或重计算可能耗尽 CPU / 内存。[security](https://mathjs.org/docs/expressions/security.html)

mathjs 更像数学和单位计算环境，不是 Excel 函数集合。VibeTable 必须自建函数白名单，并补日期和表格文本语义。它可以作为“原版 PocketBase JSVM 能否复用同一 JS runtime”的优先 POC，但在通过真实 Goja 测试前不能定案。

### JEXL

JEXL 支持对象字段、数组过滤、条件表达式、自定义 function / transform，并能同步或异步执行。[JEXL README](https://github.com/TomFrost/Jexl)

源码内部确实先 parse AST，再由 evaluator 逐节点执行，而不是把字符串交给 JavaScript `eval`。[Parser](https://github.com/TomFrost/Jexl/blob/master/lib/parser/Parser.js)、[Evaluator](https://github.com/TomFrost/Jexl/blob/master/lib/evaluator/Evaluator.js)

但 AST 是内部实现，公开 API 没有字段依赖提取契约。若 VibeTable 依赖私有 `_ast`，升级和安全审查都会受制于 fork。它也没有表格日期函数库，因此不优于自有小型 AST。

### expr-eval

expr-eval 公开提供：

- `parse()` / `evaluate()`；
- `variables({withMembers:true})`，可返回 `x.y.z` 形式的依赖；
- 可关闭逻辑、比较、赋值等 operator；
- 自定义函数。[expr-eval README](https://github.com/silentmatt/expr-eval)

它零依赖、MIT、CJS/ESM，代码年代和目标环境使它成为最容易在 Goja 里验证的候选。[package metadata](https://github.com/silentmatt/expr-eval/blob/master/package.json)

不过必须禁用赋值和函数定义，不使用 `toJSFunction()`：后者会生成原生 JavaScript 函数，而普通 `evaluate()` 才是解释执行路径。[expression source](https://github.com/silentmatt/expr-eval/blob/master/src/expression.js)、[evaluator source](https://github.com/silentmatt/expr-eval/blob/master/src/evaluate.js)

它的函数能力主要是数学，日期、文本和空值语义仍需 VibeTable 自己定义。适合 POC 或作为自有 parser 的起点，不适合未经收缩就接受任意用户表达式。

### CEL

CEL 的特点与“用户可编辑的数据库计算字段”高度匹配：

- 非图灵完备、无副作用；
- host 只暴露允许的变量和函数；
- parse 后得到 AST；
- cel-go 支持静态 type-check；
- 编译后的 program 可缓存并复用；
- 支持列表、映射、JSON 风格对象、字符串、时间戳和 duration。[cel-go README](https://github.com/cel-expr/cel-go)、[CEL spec](https://github.com/cel-expr/cel-spec)

`@bufbuild/cel` 也公开 `parse`，结果是可序列化 `ParsedExpr`，可以遍历 identifier / select 节点提取依赖。[ECMAScript parse API](https://github.com/bufbuild/cel-es/blob/main/packages/cel/src/parse.ts)

对 VibeTable 的限制：

- 它不是 Excel 语法；
- ECMAScript evaluator 仍很新；
- Go 和 ECMAScript 的自定义函数必须以共同测试向量锁定；
- 当前 ECMAScript 构建目标 ES2023，不直接兼容 Goja；
- host 自定义函数仍可能引入高成本或副作用，必须保持白名单和成本限制。

如果 PocketBase 走自有 Go 二进制，这是综合安全性、依赖分析和长期跨语言能力最好的候选。

## 单一真相源的具体规则

“只有后端计算”还不够，还需要把以下约束固定为产品协议：

1. **公式定义是唯一配置源**  
   公式文本、返回类型、engine version、时区/locale 和允许函数集合保存在 VibeTable 自有元数据中，不能分别散落成 Directus 和 PocketBase 两套隐式配置。

2. **当前 workspace 只有一个活动后端 evaluator**  
   Directus workspace 由 Directus Hook 权威执行；PocketBase workspace 由 PocketBase Hook 权威执行。它们不是同时写同一数据库的两个真相源。

3. **客户端无权写结果字段**  
   POST/PATCH 中出现计算结果字段时应忽略或拒绝。Hook 使用源字段和数据库当前记录重新计算。

4. **同一行计算必须与源字段写入处于同一事务**  
   API 成功时，源字段和计算结果必须一起成功；公式错误则整体失败，除非产品显式定义“保存错误状态”的另一种策略。

5. **响应返回后端保存值**  
   前端即使展示过试算，也必须使用保存响应替换它。

6. **公式有版本**  
   保存公式时生成规范化 AST hash / formula version。修改公式后触发全表 backfill，并能识别尚未重算的旧版本记录。

7. **第一版只允许确定性函数**  
   `NOW`、`TODAY`、`RAND`、外部请求和自定义脚本函数会让“什么时候重算”变得不明确。第一版应禁用；日期函数使用显式输入和固定时区。

8. **依赖必须可静态提取**  
   只允许直接字段引用，例如 `row.quantity * row.price`。动态索引 `row[fieldName]`、反射式字段访问或运行时生成公式应拒绝，否则无法可靠决定何时重算。

## 安全和资源限制

无论最终选 CEL 还是自有 AST，均应：

- 禁止 `eval`、`new Function`、脚本语句、赋值和用户定义函数；
- field access 只允许 schema 中声明的字段，禁止访问 prototype / constructor；
- function access 只允许固定注册表；
- 限制公式字符数、AST 节点数、嵌套深度、数组/集合大小；
- 限制单次计算成本和批量重算总量；
- 检测公式字段之间的循环依赖；
- 对除零、null、类型不匹配和日期解析给出稳定错误码；
- 使用固定时区和 locale，不依赖宿主进程默认值；
- 对 Directus Node、PocketBase Go 和试算端点运行相同 golden cases。

## 最终建议

### 产品方向建议

1. 第一版不做浏览器权威计算。
2. 前端公式编辑器通过本机后端试算端点实时预览；保存时只提交源字段。
3. 结果作为普通只读字段持久化，确保 API filter / sort / export 正常。
4. 第一版仅支持同一行、确定性标量公式。
5. 先定义 VibeTable 公式契约：number、string、boolean、date、null、error、timezone 和函数白名单。

### 技术选择建议

- 如果接受自有 PocketBase Go 二进制：选择 CEL，PocketBase 用 cel-go，Directus / 浏览器工具用 `@bufbuild/cel`，并通过共同测试保证语义。
- 如果坚持 PocketBase 官方预编译二进制：先做两项真实 JSVM POC：
  1. `mathjs` 最小定制 bundle；
  2. 小型 AST runtime（可用 expr-eval 做验证，但长期应拥有自己的稳定 AST 契约）。
- Formula.js 只作为函数实现候选；不要把它误当 parser。
- 不采用 HyperFormula 作为当前双后端内核。

### 决策前必须完成的 POC

用同一组公式在真实环境中执行：

```text
quantity * unit_price
IF(active, ROUND(amount * tax_rate, 2), 0)
CONCAT(first_name, " ", last_name)
DATE_DIFF(end_date, start_date, "day")
COALESCE(discount, 0)
```

验证：

- Directus Node Hook；
- PocketBase Go hook（CEL 路径）或真实 `pb_hooks` Goja（JS 路径）；
- 字段依赖提取；
- null / date / decimal 一致性；
- 公式错误是否阻止事务；
- 单行、百行和一万行 backfill 性能；
- API 保存响应是否包含最终计算值。

POC 通过前，不应承诺“支持全部 Formula.js/Excel 函数”或“任意 npm 公式库可在 PocketBase JSVM 运行”。
