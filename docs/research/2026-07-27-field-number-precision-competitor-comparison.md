# 数值字段精度：飞书多维表格、WPS 多维表格与 NocoDB 对标调研

> 调研日期：2026-07-27。范围仅限各产品的第一方帮助中心、开放 API/脚本 API 与官方 GitHub 仓库；未将社区用户帖、第三方博客或推测当作产品事实。除特别注明外，下文的“支持”均指访问日可查的官方文档，不代表兼容版本永久不变。

## 结论摘要

三家都把 **数字、百分比、货币** 暴露给最终用户，并普遍把小数位、千分位、货币符号/币种作为字段格式或显示选项。飞书的开放 API 最清楚地说明：货币与普通数字同为 `type: 2`，以 `ui_type` 和 formatter/currency code 区分；这强烈表明货币首先是数字字段的展示/元数据变体，而非另一种精确金额值类型。NocoDB 的官方文档则同时给出了单独的 `Number`、`Decimal`、`Currency`、`Percent` UI 类型，以及脚本 API 返回 JavaScript `number | null` 的契约。

三家公开资料均**没有**作出“金额/小数以十进制精确运算、没有二进制浮点误差”的承诺，也没有公开统一的舍入模式（例如银行家舍入或半向上）说明。WPS 已公开多维表字段与记录 API，但文档没有给出数值的底层编码、精确十进制保证或舍入模式，因此不能据此判断是否由二进制浮点保存。

对 VibeTable 的克制建议是：第一阶段保持一个通用数值值域（现有 binary64）并把 **整数 / 小数位显示 / 百分比 / 货币符号或币种** 作为字段类型或格式元数据；空值必须独立于 `0`；明确把这定位为常规业务数值而非财务精确计算。不要仅因存在“货币”外观就宣称十进制精确，也不必现在就引入一套完整金融会计模型。若产品确定承接对账、计税或逐分不丢失的金额计算，再单独立项增加精确十进制/定标整数能力，并先定义迁移与运算语义。

## 证据口径

* **官方明确说明**：来源文字直接表达的能力或限制。
* **API/源码推断**：来源暴露的类型、选项或返回值所能支持的有限结论；不把它扩大为未明说的存储实现或算术保证。
* **未验证**：在限定的一方资料中没有足够证据。缺证不等于产品不具备该能力。

## 横向比较

| 产品 | 用户可见类型/格式 | 小数位、精度、舍入 | API/源码所见实际表示 | 十进制精确承诺 | 空值与 `0` | 导入导出相关说明 |
| --- | --- | --- | --- | --- | --- | --- |
| 飞书多维表格 | 数字字段可格式化为整数、1–9 位小数、千分位、百分比；另有货币字段。 | 数字字段默认 1 位小数；货币 API 可选 0–4 位小数。公开资料未说明舍入算法。 | 字段 API：普通数字、进度、货币、评分同为 `type: 2`；货币由 `ui_type: "Currency"` 与 `currency_code` 区分。记录值的精确 JSON 标量类型、后端存储格式未在本次资料中核实。 | 未发现。 | 未发现数字单元格 `null` 与 `0` 的官方 API 证据，不能断言。 | 可导入/导出 `.base`、`.xlsx`、`.csv`；导入会自动识别数字等类型。未见数值精度保真承诺。 |
| WPS 多维表格 | 有数字、百分比、货币字段；字段 API 分别使用 `Number`、`Percentage`、`Currency`，并携带 `number_format`。 | 官方教程说明格式限定统一显示位数，但实际保存的数据仍与输入相同；不同资料中的可配置位数上限不一致，且均未说明舍入模式。 | 记录 API 可选择返回 `original`、`text` 或二者组合，但公开文档没有为原始数值给出稳定的 JSON 类型、最大安全范围或十进制精确保证。 | 未发现。 | 未验证。 | 未找到一方的数值精度导入导出承诺。 |
| NocoDB | `Number`（整数）、`Decimal`、`Currency`、`Percent` 是独立 UI 类型；货币的 locale 与 ISO 4217 code 影响显示，百分比以整数输入并加 `%` 显示。 | `Decimal` 可配置 1–8 位 precision，默认 1；`Number` 的官方 JS 安全整数范围为 ±(2^53−1)。未说明舍入模式。 | 官方脚本 API 规定 Number/Decimal/Currency/Percent 的 cell value 为 `number \| null`。因此脚本接口没有传递十进制字符串或 scale；但不能据此反推每一种外部数据库列的物理存储。 | 未发现。 | **有 API 证据**：数值字段为 `number \| null`，因此 API 层 `null` 与数值 `0` 可区分。 | 分隔符只影响显示，底层数值不带分隔符且不受影响，可用于排序、筛选、公式与导出；未见 decimal 精度跨导出格式的保证。 |

## 飞书多维表格

### 官方明确说明

1. [字段概览](https://www.feishu.cn/hc/zh-CN/articles/541575577400-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E5%AD%97%E6%AE%B5)（访问：2026-07-27）把“数字”列为支持小数、百分比展示的字段，把“货币”列为可按市场/对象选择币种的业务字段。
2. [数字字段帮助](https://www.feishu.cn/hc/zh-CN/articles/909323330538-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E6%95%B0%E5%AD%97%E5%AD%97%E6%AE%B5)（访问：2026-07-27）说明默认格式为保留 1 位小数，并列出整数、千分位、千分位（2 位小数）、保留 1–9 位小数、百分比、百分比（2 位小数）等格式。该页还提醒长银行卡号会超过系统可记录的数位上限，建议改用文本；它是容量/精度风险证据，但没有公布阈值、二进制浮点格式或舍入方式。
3. [字段编辑指南（服务端 API）](https://open.feishu.cn/document/server-docs/docs/bitable-v1/app-table-field/guide)（访问：2026-07-27）明确：数字、进度、货币、评分共享 `type: 2`；普通数字的 `formatter` 可为整数、小数、千分位、百分比以及人民币/美元格式；货币的 `ui_type` 为 `Currency`，属性为小数 formatter（0、1、2、3、4 位）和 `currency_code`。公式字段也使用同类数值/货币 formatter。
4. [导出或导入多维表格](https://www.feishu.cn/hc/zh-CN/articles/360049067854-%E5%AF%BC%E5%87%BA%E6%88%96%E5%AF%BC%E5%85%A5%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC)（访问：2026-07-27）确认可导出 `.base`、`.xlsx`、`.csv`，可导入同三种格式，导入会自动识别文本、数字等数据类型；`.base` 旨在保留在线多维表格的全部配置。该页没有承诺 CSV/Excel 往返后的小数精度、显示格式或计算语义不变。

### API/源码推断（有限）

`type: 2` 的共享与 `Currency` UI type 的分离说明，飞书的“货币”不是 API 一级基础数值类型，而是与普通数字共用数字 type、额外携带 UI/格式和币种元数据的变体。这是字段模型推断，不等价于“其内部一定以 binary64 存储”，更不等价于“金额不精确”。官方 API 文档没有在本次可访问内容中给出记录数字的 JSON schema、数据库列类型或精确十进制保证。

### 未验证范围

* 货币与普通数字在服务端的物理存储和计算表示；是否使用 IEEE-754 binary64、定标整数或十进制。
* 格式化时的舍入规则，以及格式小数位是否会回写/截断原值。
* 数字字段空值与 `0` 的记录 API 表示及其筛选/聚合语义。

## WPS 多维表格

### 官方明确说明

1. [WPS 多维表格官方社区教程：基础字段详解](https://bbs.wps.cn/topic/18232)（访问：2026-07-27；作者标注为“金山办公”）明确区分存储值与显示格式：数字格式会让单元格统一显示指定小数位，但“实际保存的数据仍与输入时相同”。这能证明显示位数不会直接截断原输入；仍不能证明底层采用 decimal 或 binary64。
2. [WPS 多维表格官方社区教程：业务字段详解](https://bbs.wps.cn/topic/18635)（访问：2026-07-27；页面作者标注为“WPS多维表格 @金山办公”）将百分比、货币列为业务字段；百分比输入会自动转成百分数，可显示整数或 1–4 位小数；货币可录入整数和小数，支持人民币、美元、欧元，带千分位分隔符并可显示整数或 1–2 位小数。
3. [创建字段 API](https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/fields/create-field)（访问：2026-07-27）给出 `Number`、`Currency`、`Percentage` 三种字段类型，均通过 `number_format` 描述显示形式，例如整数、三位小数货币和两位小数百分比。
4. [按页列举记录 API](https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/records/list-record-by-page)（访问：2026-07-27）允许用 `text_value` 选择 `original`、`text` 或 `compound` 返回值；示例为文本返回，未定义原始数字的稳定 JSON 标量类型、精度上限或空值语义。
5. [WPS 365：数字字段常见问题](https://plus.wps.cn/blog/p112094.html)（访问：2026-07-27）称数字字段可设置小数位数（0–6）、千分位、人民币/美元等货币符号和百分比格式，并可用于数学运算与聚合。该页面明确注明“部分内容由 AI 匹配目标关键词，结合 WPS 365 官方知识库智能生成”，故将其作为 WPS 官方站点发布的现行线索，而非比上项人工官方教程更强的规格证据。
6. [WPS 365：数字字段使用说明](https://plus.wps.cn/blog/p112099.html)（访问：2026-07-27）同样称小数位为 0–6、可用货币符号/百分比格式；其示例建议金额用 2 位小数与货币符号、数量用 0 位小数。此页也有上述 AI 匹配声明。

### API/源码推断

字段 API 证明 WPS 在产品模型中区分 `Number`、`Currency`、`Percentage`，但它们都使用 `number_format` 控制数字外观；记录 API 又明确区分原始值和文本值。这支持“字段语义/显示格式与记录原始值分层”的判断，但不能继续推导其原始值究竟是通用浮点、十进制定点还是其他表示。也不能因可配置“保留小数位”就把显示精度误写成存储精度。

### 未验证范围

* 数字、百分比、货币是否为互不相同的物理存储类型，或只是产品层数值子类型。
* 实际数值精度、最大安全整数、十进制精确性、舍入模式。
* 空单元格与 `0` 的区分，以及导入/导出对数值精度的承诺。
* 上述 2024 与 2026 页面显示的小数位上限差异，是否源于版本、字段种类或页面内容生成方式；本调研不擅自统一为某一个产品常数。

## NocoDB

### 官方明确说明

1. [Number 字段文档](https://nocodb.com/docs/product-docs/fields/field-types/numerical/number)（访问：2026-07-27）把 Number 定义为整数值，明确其 JavaScript 范围为 `-9,007,199,254,740,991` 到 `9,007,199,254,740,991`；千分位/小数分隔符只影响显示，保存前会被剥离，底层数值不受影响，仍用于排序、筛选、公式和导出。
2. [Decimal 字段文档](https://nocodb.com/docs/product-docs/fields/field-types/numerical/decimal)（访问：2026-07-27）说明 Decimal 用于小数值，precision 最多 8 位、默认 1 位；同样说明分隔符只影响显示，底层数值不受影响。这里的“8 digits of precision”按该文的字段设置语境应读作可配置的小数显示精度上限，**不能**提升为数据库 `DECIMAL(p,s)` 的总有效数字保证。
3. [Currency 字段文档](https://nocodb.com/docs/product-docs/fields/field-types/numerical/currency)（访问：2026-07-27）明确货币格式“only for display”，保存的值仍为数值供计算和公式使用；locale 与 currency symbol/code 是显示配置，支持 ISO 4217 代码。
4. [Percent 字段文档](https://nocodb.com/docs/product-docs/fields/field-types/numerical/percent)（访问：2026-07-27）说明百分比是数值字段：以整数输入，显示加 `%`（例如 `75` 显示 `75%`），可选进度条表现。
5. [NocoDB Scripts Field API](https://nocodb.com/docs/scripts/api-reference/field)（访问：2026-07-27）列出 `UITypes.Number`、`Decimal`、`Currency`、`Percent`；创建 Decimal 的 options 包含 `precision: 2`，Currency 的 options 包含 locale/code；并明确 Number、Decimal、Currency、Percent 的 `record.getCellValue()` 返回 `number or null`。这同时提供了 API 层面空值与 `0` 可区分的直接证据。

### 关于“依赖底层数据库列类型”的核实

**结论：只能作有边界的肯定，不能泛化为所有场景的已证实事实。** NocoDB 官方产品资料明确它能连接 PostgreSQL、MySQL、Microsoft SQL Server、Oracle 等外部数据源，例如其 [Oracle 支持公告](https://nocodb.com/docs/changelog/2026.06.2)（访问：2026-07-27）称可以连接既有 Oracle 数据库及其表。对于这种“连接既有数据库表”的路径，列的 integer/decimal/float/double 物理类型当然由外部数据库 schema 决定；NocoDB 的 UI 不会改变数据库原有列的数值语义。这是数据库连接模型的直接含义。

但本次一方资料没有给出一张完整、版本固定的 NocoDB 映射表，能逐项证明“每个 UI Number/Decimal/Currency/Percent 在每个受支持数据库中分别创建/映射为 integer、decimal、float、double 的哪一种”。因此不能声称：

* NocoDB 的所有数值行为都只由底层数据库类型决定；其内部管理的数据源、公式、脚本 API 仍有自己的 UI/API 语义。
* Currency/Percent 在所有数据库路径中“纯粹只是” UI 格式。现有证据足以证明 Currency 的格式仅用于显示、其保存值仍是数值；Percent 也是数值展示语义；但不足以证明每条建表路径的 DDL 都相同。

较稳妥的表述是：**NocoDB 同时存在数据库 schema 层和 UI 类型/显示层。接入既有表时，物理 integer/decimal/float/double 必须以该表 schema 为准；Currency/Percent 的公开文档强调数值值加显示语义，而不是十进制精确金额契约。**

### 未验证范围

* 各数据库/各版本的完整 DDL 映射，以及 Decimal 8 位设置与物理列 precision/scale 的确切关系。
* `number | null` 在脚本运行时是 JavaScript binary64 的实现细节虽高度符合 JS 语言常识，但官方页面未用“IEEE-754 binary64”措辞；本报告因此只陈述其公开 API `number` 契约，不把语言常识写成 NocoDB 的精度承诺。
* Decimal/Currency/Percent 的舍入模式、公式/聚合的跨数据库一致性，以及导出到 CSV/XLSX 后的无损保证。

## 不应从对标中得出的结论

1. “有货币字段”不等于“以 decimal/定标整数精确保存并精确运算”。飞书 API 的同 type 模型和 NocoDB 的显示说明恰恰提示，应把外观/币种元数据与数值算术保证分开。
2. “可设置保留 N 位小数”不等于输入会按 N 位十进制保存，也不等于展示/计算采用特定舍入规则；三家资料均未公开该规则。
3. NocoDB 的 `Decimal` UI 类型也不能单靠名称当作跨数据库的精确十进制担保；需查看正在连接的实际数据库列定义。
4. WPS 官方站点和开放 API 可证明它有这些用户功能及字段/返回模式，但文档没有公布底层数值编码；不能把一般 JavaScript/数据库经验写成 WPS 的产品规格。

## 对 VibeTable 的最小可行对标结论

行业常见、且本次三家都有证据的能力是：用一个可计算的数值值，叠加整数/小数位/千分位/百分比/货币符号或币种等字段级显示设置；提供适度的小数显示配置，而非在面向普通多维表用户的界面中暴露复杂的精度模型。

建议 VibeTable 当前只承诺以下最小集合：

* **通用数值**：作为常规计算值域；在文案中不宣称其适合无误差的十进制金额计算。
* **整数、百分比、货币**：作为明确的字段子类型或格式预设；货币至少保存币种/符号元数据，不把格式名当作精确性保证。
* **小数位显示设置**：仅表达显示精度；在未定义规则前，不把它说成存储 scale 或舍入策略。
* **空值**：以 `null`/未填独立表示，绝不与 `0` 混同；这是 NocoDB API 也明示采用的可区分模型。

暂不纳入当前方案：多币种换算、会计舍入策略选择、任意 precision/scale DDL、财务审计承诺、完整 decimal 运算引擎。它们需要明确的业务授权和额外的迁移/公式/导入导出验证，不能由“竞品有货币格式”推导出来。

## 来源清单（全部访问于 2026-07-27）

* 飞书帮助中心：[字段概览](https://www.feishu.cn/hc/zh-CN/articles/541575577400-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E5%AD%97%E6%AE%B5)、[数字字段](https://www.feishu.cn/hc/zh-CN/articles/909323330538-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E6%95%B0%E5%AD%97%E5%AD%97%E6%AE%B5)、[导入导出](https://www.feishu.cn/hc/zh-CN/articles/360049067854-%E5%AF%BC%E5%87%BA%E6%88%96%E5%AF%BC%E5%85%A5%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC)。
* 飞书开放平台：[字段编辑指南](https://open.feishu.cn/document/server-docs/docs/bitable-v1/app-table-field/guide)。
* WPS 官方社区：[基础字段详解](https://bbs.wps.cn/topic/18232)、[业务字段详解](https://bbs.wps.cn/topic/18635)。
* WPS 开放平台：[创建字段](https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/fields/create-field)、[按页列举记录](https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/records/list-record-by-page)。
* WPS 365 官方站点（含页面自行声明的 AI 匹配说明）：[数字字段常见问题](https://plus.wps.cn/blog/p112094.html)、[数字字段使用说明](https://plus.wps.cn/blog/p112099.html)。
* NocoDB 官方文档：[Number](https://nocodb.com/docs/product-docs/fields/field-types/numerical/number)、[Decimal](https://nocodb.com/docs/product-docs/fields/field-types/numerical/decimal)、[Currency](https://nocodb.com/docs/product-docs/fields/field-types/numerical/currency)、[Percent](https://nocodb.com/docs/product-docs/fields/field-types/numerical/percent)、[Scripts Field API](https://nocodb.com/docs/scripts/api-reference/field)、[外部 Oracle 数据源公告](https://nocodb.com/docs/changelog/2026.06.2)。
