# PocketBase v0.39.9 字段设置、默认值与桌面多维表格暴露方案

> 适用对象：单人桌面多维表格软件（下文暂称 **VibeTable**）  
> PocketBase 版本：**v0.39.9**  
> 调研日期：**2026-07-27**  
> 依据：PocketBase v0.39.9 标签对应的 Go 源码、PocketBase 管理后台源码、官方 Collections 文档，以及飞书、WPS 多维表格、NocoDB 的第一方资料。
> 决策状态：已结合 2026-07-27 的产品讨论收敛；第 15 节记录当前已确认边界。

---

## 1. 结论先行

PocketBase v0.39.9 在源码层注册了 14 种字段类型：

1. `text`
2. `bool`
3. `number`
4. `date`
5. `autodate`
6. `editor`
7. `email`
8. `url`
9. `select`
10. `relation`
11. `file`
12. `geoPoint`
13. `json`
14. `password`

面向多维表格产品，最重要的并不是把这些属性原样搬到界面，而是先处理以下六个语义差异：

### 1.1 PocketBase 没有通用的“字段默认值”属性

除 `autodate` 和 `text.autogeneratePattern` 这种特殊动态行为外，各字段结构体中没有统一的 `default` 属性。创建记录时未提供值，PocketBase 会使用字段类型的零值，例如：

- 数字：`0`
- 布尔：`false`
- 文本、邮件、网址、日期：`""`
- 多选、多个文件、多关系：`[]`
- 地理坐标：`{"lon":0,"lat":0}`
- JSON：`null`

因此，用户看到的“默认值”应由 VibeTable 在创建记录时注入，而不是误认为它是 PocketBase 字段原生设置。

### 1.2 PocketBase 没有字段级 `unique` 属性

唯一性属于集合的数据库索引。VibeTable 可以在常规选项中提供“唯一值”，但底层应创建或删除唯一索引；对于允许空白的文本类字段，应使用“忽略空值”的部分唯一索引。

### 1.3 `number` 是 binary64 通用数字，不是精确十进制

PocketBase 的 `number` 值在 Go 层按 `float64` 处理，原生只有：

- `min`
- `max`
- `onlyInt`
- `required`

VibeTable 首版沿用这一值域，只开放一个**通用数字**存储语义。整数、小数位、千分位、货币、百分比和自定义单位是字段格式或预设，不另建精确小数存储引擎。

- 显示小数位只影响渲染，不回写、不截断原值；
- 货币保存币种/符号元数据，但不承诺十进制无误差或会计级计算；
- 百分比按比例值解释，例如存储 `0.125`、显示 `12.5%`；
- 整数预设启用 `onlyInt`，并限制在 JavaScript 安全整数范围；
- 首版不提供“写入时舍入”“存储小数位”“舍入模式”等会改变数据的字段设置。

飞书、WPS 和 NocoDB 的公开资料同样主要暴露数字值与显示格式，未发现任一家公开承诺端到端十进制精确计算。详见[竞品调研](./research/2026-07-27-field-number-precision-competitor-comparison.md)。

### 1.4 空白与零值默认无法区分

这是多维表格中最需要提前决定的问题：

| 用户语义 | PocketBase 原生落值 | 是否丢失语义 |
|---|---:|---:|
| 数字为空 | `0` | 是：无法区分空白与真实的 0 |
| 复选框为空 | `false` | 是：无法区分未填写与明确否定 |
| 地理位置为空 | `{lon:0,lat:0}` | 是：无法区分空白与真实坐标 `(0,0)` |
| 日期为空 | `""` | 是：无法区分空白与显式空值 |
| 文本为空 | `""` | 是：无法区分未填写与明确空文本 |
| 多选/多关系/多文件为空 | `[]` | 是：无法区分未填写与明确空集合 |
| JSON 为空 | `null` | 不丢失，JSON 是例外 |

VibeTable 已确认必须严格区分这些状态。对 PocketBase 无法原生保留空白的可空字段，使用固定的字段级存在性列，详见第 7 节。

### 1.5 字段显示名与 PocketBase `name` 必须分离

PocketBase 字段 `name`：

- 长度 1–100；
- 必须匹配 `^\w+$`；在 Go 正则中可视为 ASCII 字母、数字和下划线；
- 不能使用部分保留名称；
- 不能包含 `_via_`；
- 同一集合内必须唯一。

因此，中文名称、空格、标点以及频繁重命名都不应直接映射到物理字段名。推荐：

```text
用户显示名：合同金额（含税）
PocketBase name：f_a8k3m2p9
VibeTable fieldId：fld_a8k3m2p9
```

用户重命名只改显示名，不改物理列名。

### 1.6 不建议把所有设置平铺在同一层

推荐三级认知，但界面仍可只呈现“常规 / 高级”两个页签：

- **常规选项**：用户能理解并高频使用的行为；
- **高级选项**：校验、格式、索引、底层 PocketBase 属性；
- **系统危险区**：放在高级选项底部，仍然可见，但需要警告或二次确认。

### 1.7 所有产品写入必须经过 VibeTable 权威边界

正常模式下，表数据和 Schema 的创建、编辑、粘贴、导入、插件写入、撤销与迁移必须经过同一个 VibeTable 写入服务。该边界负责：

1. 保留原始输入直到完成权威解析；
2. 校验字段契约、空白存在性、唯一性和跨字段约束；
3. 在同一事务中写入业务值与存在性列；
4. 返回服务端实际保存的规范值，由界面用该值更新本地状态；
5. 对任何拒绝、转换或冲突给出结构化错误，不允许“尽力写入后继续”。

PocketBase 自带的 `/_/` 是**PocketBase 管理后台**，不是 VibeTable 仪表盘，也不是产品表数据或 Schema 的常规入口。它应作为显式的开发者危险入口；绕过产品写入边界的直接修改不享有 VibeTable 的无损与一致性保证。

### 1.8 创建时间与最后更新时间可以复用现有链路，但不能直接上线

仓库已经具备 `autoDate` 类型、只读系统字段、查询日期映射和日期格式化等大部分基础设施；关键缺口是编译器当前把所有 `autoDate` 都写成 `onCreate=true/onUpdate=true`，因此名称为 `created_at` 的现有样例实际上也是“最后更新时间”行为。

本方案将 `autoDate` 收敛为两个不可变产品角色：`createdAt -> true/false`、`updatedAt -> true/true`。两者在 VibeTable 中是只读系统字段，但底层 PocketBase `System` 必须保持 `false`，以兼容当前 Schema 重放流程。首版只允许在新表或空表创建；非空表必须先有可信回填来源，禁止把迁移时刻伪装成历史时间。

---

## 2. “默认值”必须区分三层

同一个属性可能同时存在三种默认概念：

| 层次 | 示例 | 含义 |
|---|---|---|
| PocketBase 原始默认值 | `file.maxSize = 0` | Go 结构体零值或 Schema JSON 中省略后的值 |
| PocketBase 有效默认值 | `5 MiB` | PocketBase 运行时看到 `0` 后采用的实际限制 |
| VibeTable 推荐默认值 | 显式保存 `5_242_880` | 产品界面显示并写入 Schema 的明确值 |

### 推荐原则

对存在“0 表示采用内置值”的属性，VibeTable 尽量保存明确的有效值，而不是继续保存哨兵值 `0`：

| 属性 | PocketBase 原始值 | PocketBase 有效值 | 建议写入值 |
|---|---:|---:|---:|
| `text.max` | `0` | 5000 字符 | `5000` |
| `editor.maxSize` | `0` | 5 MiB | VibeTable 建议 `1 MiB`，可一键恢复为 `5 MiB` |
| `json.maxSize` | `0` | 1 MiB | `1_048_576` |
| `file.maxSize` | `0` | 5 MiB/文件 | `5_242_880` |
| `password.max` | `0` | 71 字符 | 系统密码建议显式 `71` |
| `password.cost` | `0` | bcrypt 默认成本 10 | 系统密码建议显式 `10` |
| `select.maxSelect` | `0` | 单选，最多 1 个 | `1` |
| `relation.maxSelect` | `0` | 单关系，最多 1 个 | `1` |
| `file.maxSelect` | `0` | 单文件，最多 1 个 | `1` |

这样做的优点是：Schema 可读、界面无歧义，并可避免以后升级 PocketBase 时因其内置默认改变而静默改变产品行为。

---

## 3. 全字段总览

| PocketBase 类型 | 记录零值 | 主要原生设置 | VibeTable 建议初始形态 |
|---|---|---|---|
| `text` | `""` | min、max、pattern、autogeneratePattern、required、primaryKey | 单行文本；最大 5000 字符 |
| `editor` | `""` | maxSize、convertURLs、required | 富文本；建议上限 1 MiB |
| `number` | `0` | min、max、onlyInt、required | binary64 通用数字；格式变化不改原值 |
| `bool` | `false` | required | 三态复选框；默认空白 |
| `date` | `""` | min、max、required | 仅日期；默认空白 |
| `autodate` | `""`，随后自动赋值 | onCreate、onUpdate | “创建时间/最后更新时间”双预设 |
| `email` | `""` | onlyDomains、exceptDomains、required | 无域名限制；默认空白 |
| `url` | `""` | onlyDomains、exceptDomains、required | VibeTable 默认仅允许 HTTP/HTTPS |
| `select` | 单选 `""`；多选 `[]` | values、maxSelect、required | 默认单选；不自动选择第一项 |
| `relation` | 单关系 `""`；多关系 `[]` | collectionId、minSelect、maxSelect、cascadeDelete、required | 默认单关系；级联删除关闭 |
| `file` | 单文件 `""`；多文件 `[]` | maxSize、maxSelect、mimeTypes、thumbs、protected、required | 单文件；5 MiB/文件；类型不限 |
| `geoPoint` | `{lon:0,lat:0}` | required | 默认空白语义；显示 6 位小数 |
| `json` | `null` | maxSize、required | 默认 `null`；最大 1 MiB |
| `password` | 哈希存储；普通读取为空 | min、max、pattern、cost、required | 不进入普通字段菜单；仅系统/实验功能 |

---

## 4. 所有普通字段共有的设置

并非每一种字段都拥有全部公共属性；例如 `autodate` 没有 `help` 和 `required`。下表给出大多数普通字段的共有方案。

| 设置 | PocketBase 原始默认 | VibeTable 推荐默认 | 分组 | 产品说明 |
|---|---:|---:|---|---|
| 显示名称 `label` | 无，VibeTable 自有 | `字段` 或按类型命名 | 常规 | 支持中文、空格、标点；不要直接用作 PB `name` |
| 描述 `help` | `""` | `""` | 常规 | PocketBase 最多 300 字符 |
| 必填 `required` | `false` | `false` | 常规 | 必须按字段类型改写文案；数字和布尔尤其特殊 |
| 默认值 | 无通用原生属性 | 默认关闭 | 常规 | 创建记录时由 VibeTable 注入 |
| 唯一值 | 无字段级属性 | `false` | 常规 | 底层创建集合唯一索引；建议默认忽略空值 |
| 视图中隐藏 | 无，VibeTable 自有 | `false` | 常规 | 仅隐藏当前视图的列，不影响 API |
| PocketBase 物理名 `name` | 新字段按类型自动生成 | 自动生成稳定名称 | 高级 | 1–100，仅 ASCII 字母、数字、下划线；用户通常不改 |
| PocketBase 字段 ID `id` | 自动生成 | 自动生成、只读 | 高级/系统 | 稳定标识，不应让普通用户编辑 |
| `presentable` | `false` | `false` | 高级 | 仅提示 PocketBase 管理后台如何显示关系预览；VibeTable 应有自己的“关系显示字段” |
| PocketBase `hidden` | `false` | `false` | 高级/危险 | 会从 JSON API 响应中隐藏，不等同于隐藏表格列 |
| `system` | `false` | `false` | 高级/危险 | 设为 true 后禁止重命名和删除；普通字段不要开启 |

### 必填设置的正确命名

不要对所有字段都显示同一个“必填”文案：

| 字段 | PocketBase `required=true` 的真实含义 | 推荐界面文案 |
|---|---|---|
| 文本、邮件、网址、日期 | 不能为空 | 必填 |
| 单选、关系、文件 | 至少有一个值 | 必须选择/关联/上传 |
| 数字 | 不能为 `0` | **非零必填（PocketBase）** |
| 布尔 | 必须为 `true` | **必须勾选** |
| GeoPoint | 不能同时为经度 0、纬度 0 | **必须提供非 `(0,0)` 坐标** |
| JSON | 不能是 `null`、空字符串、空数组或空对象 | **必须为非空 JSON** |

VibeTable 已采用独立的空白存在性；普通“必填”由 VibeTable 校验，不能盲目映射到数字、布尔和 GeoPoint 的 PocketBase `required`。

---

## 5. 各字段的完整设置与推荐分组

## 5.1 `text`：文本

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效默认 | VibeTable 推荐 | 分组 |
|---|---|---:|---:|---:|---|
| `min` | integer | `0` | 无最小限制 | `0` | 高级 |
| `max` | integer | `0` | 5000 个 Unicode 字符 | 显式 `5000` | 高级 |
| `pattern` | string | `""` | 不校验正则 | `""` | 高级 |
| `autogeneratePattern` | string | `""` | 不自动生成 | `""` | 高级 |
| `required` | boolean | `false` | 允许空字符串 | `false` | 常规 |
| `primaryKey` | boolean | `false` | 普通字段 | `false` | 系统危险区 |

### 计量与精度

- `min`、`max` 按 Unicode rune/字符计数，不按 UTF-8 字节计数；中文字符通常计为 1。
- `pattern` 使用 Go 正则语法，不应直接假定与 JavaScript 正则完全相同。
- `primaryKey=true` 时字段必须名为 `id`，且集合只能有一个主键；不应作为普通设置使用。

### 常规选项

- 显示名称
- 描述
- 必填
- 默认文本
- 唯一值
- 单行/多行输入外观（VibeTable 自有，不改变 PB 类型）

### 高级选项

- 最小字符数
- 最大字符数
- 正则校验
- 自动生成正则
- 物理字段名
- PocketBase hidden / presentable
- 主键（危险区）

### 推荐默认

```jsonc
{
  "type": "text",
  "min": 0,
  "max": 5000,
  "pattern": "",
  "autogeneratePattern": "",
  "required": false,
  "primaryKey": false
}
```

---

## 5.2 `editor`：富文本

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效默认 | VibeTable 推荐 | 分组 |
|---|---|---:|---:|---:|---|
| `maxSize` | integer，字节 | `0` | 5 MiB | 1 MiB | 高级 |
| `convertURLs` | boolean | `false` | 不做编辑器 URL 转换 | `false` | 高级 |
| `required` | boolean | `false` | 允许空字符串 | `false` | 常规 |

### 计量与精度

- `maxSize` 按 UTF-8/HTML 内容的**字节数**限制，不是字符数。
- 5 MiB = `5_242_880` 字节；1 MiB = `1_048_576` 字节。
- PocketBase 存储的是 HTML 字符串。

### 为什么 VibeTable 建议 1 MiB，而不是直接采用 5 MiB

多维表格通常一条记录有很多列。允许每个富文本单元格达到 5 MiB，会快速增大 SQLite 数据库、备份和同步体积。1 MiB 对普通笔记已经很宽松，同时保留“PocketBase 默认 5 MiB”预设供高级用户选择。

### 常规选项

- 显示名称
- 描述
- 必填
- 默认富文本

### 高级选项

- 最大内容大小，界面使用 KiB/MiB，持久化为字节
- URL 转换
- 物理字段名、hidden、presentable

### 推荐默认

```jsonc
{
  "type": "editor",
  "maxSize": 1048576,
  "convertURLs": false,
  "required": false
}
```

---

## 5.3 `number`：数字

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `min` | `number \| null` | `null` | 不限制最小值 | `null` | 高级 |
| `max` | `number \| null` | `null` | 不限制最大值 | `null` | 高级 |
| `onlyInt` | boolean | `false` | 允许小数 | `false` | 常规，通常由“数字格式”派生 |
| `required` | boolean | `false` | 允许 0；开启后 0 非法 | PB 保持 `false`，由 VibeTable 处理普通必填 | 高级 |

### PocketBase 没有的数值设置

以下都必须由 VibeTable 增加：

- 显示小数位数
- 输入步长
- 千分位
- 单位、前缀、后缀
- 货币格式
- 百分比格式
- 空白与 0 的区分

其中只有**显示与输入元数据**进入首版。写入小数位、写入舍入和舍入模式暂不开放，避免一个看似格式化的操作实际改写已有数据。

### 推荐的常规选项

| 选项 | 推荐默认 | 说明 |
|---|---:|---|
| 数字样式 | 普通数字 | 普通数字 / 整数 / 货币 / 百分比 / 自定义单位 |
| 显示小数位 | 最多 2 位 | 显示 `1.2` 而不是强制 `1.20`；可切换固定小数位 |
| 隐藏末尾 0 | 开启 | 仅在“最多 N 位”模式有效 |
| 千分位 | 开启 | 仅影响显示 |
| 币种 | `CNY` | 仅货币样式生效；保存 ISO 4217 code |
| 单位 | 空 | 仅自定义单位样式生效 |
| 默认值 | 关闭 | 开启后默认可为 0 |
| 最小值/最大值 | 不限制 | 建议放常规区的“更多限制”折叠项 |

### 推荐的高级选项

| 选项 | 推荐默认 | 说明 |
|---|---:|---|
| 输入步长 | `any`；键盘增减 1 | 不作为存储约束 |
| PB `onlyInt` | 由整数预设派生 | 只在整数预设下开启 |
| PB `required` | 关闭 | 因其真实含义是“不能为 0” |
| 空白策略 | `preserve` | 由第 7 节的存在性方案实现 |

以下不作为首版字段选项：写入时舍入、存储小数位、舍入模式、任意 precision/scale、多币种换算。它们只有在明确承接对账、计税等场景时才单独立项。

### 重要的 PocketBase 0.39.9 校验特性

PocketBase 在验证数字时，遇到 `0` 会先处理 `required`，随后直接结束验证。因此：

```text
required = false
min = 1
value = 0
```

PocketBase 仍会接受 `0`。VibeTable 若把 `min` 作为用户可见约束，必须在自己的写入层再次校验，不能只依赖 PocketBase。

### 通用数字的精度边界

首版数字契约是 IEEE 754 binary64：

- 绝大多数日常数量、测量值、价格和金额足够使用；
- 安全范围内的整数可以逐位精确表示；整数预设限制为 `[-9_007_199_254_740_991, 9_007_199_254_740_991]`；
- `0.1` 等十进制小数通常保存为最接近的 binary64 值，但按最短往返十进制格式仍可稳定显示和再次写入；
- 货币样式不改变上述值域，因此不能宣传为“逐分精确”“会计级”或“无浮点误差”。

### 不静默出错的写入规则

1. 编辑器、粘贴和导入保留原始数字文本，禁止在前端先用 `Number(...)` 转换后再校验；
2. 权威写入层只接受有限数，拒绝 `NaN`、正负无穷、解析溢出以及非零输入下溢为 `0`；
3. 整数预设拒绝小数和超出安全整数范围的值；
4. 若原始十进制文本与服务端规范化后的用户可见数值不等价，例如 `9007199254740993` 被变为 `9007199254740992`，必须拒绝并指出安全范围；
5. 保存成功后返回实际持久化的规范值，界面不得继续保留未经确认的乐观值；
6. 批量导入/粘贴先预检并汇总错误；失败行不得悄悄变成空白、`0` 或近似值。

这里的“用户可见数值等价”按规范十进制值比较，不比较无意义的词法差异；例如 `1.2300` 与 `1.23` 等价。

### 货币与百分比

货币只是在通用数字上增加币种和显示格式：

```text
输入/保存：123.45
显示：¥123.45
```

显示位数可以根据币种给出推荐值，但只影响显示，不会把 `123.456` 改写成 `123.46`。

百分比统一按比例值保存：

```text
输入/显示：12.5%
底层保存：0.125
```

首版不提供“保存比例 / 保存百分数”两套口径。切换普通数字与百分比样式只改变解释和显示，不重写底层值；界面必须在确认前显示转换后的预览。

### 推荐默认

```jsonc
{
  "type": "number",
  "min": null,
  "max": null,
  "onlyInt": false,
  "required": false,
  "_vibe": {
    "style": "decimal",
    "displayScale": 2,
    "displayScaleMode": "max",
    "trimTrailingZeros": true,
    "useGrouping": true,
    "inputStep": "any",
    "currencyCode": "CNY",
    "unit": null,
    "percentStorage": "ratio",
    "nullPolicy": "preserve"
  }
}
```

---

## 5.4 `bool`：布尔/复选框

### 原生设置

| 设置 | 类型 | PB 默认 | 真实含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `required` | boolean | `false` | 开启后值必须为 `true` | `false` | 高级，文案为“必须勾选” |

记录零值和数据库默认值均为 `false`。

### VibeTable 建议增加的设置

| 设置 | 默认 | 分组 |
|---|---:|---|
| 显示形式 | 复选框 | 常规 |
| 默认值 | 关闭（保持空白） | 常规 |
| 允许未填写/三态 | `true` | 常规 |
| `true` 标签 | 是 | 高级 |
| `false` 标签 | 否 | 高级 |

可空布尔必须使用存在性列，否则 `null` 会被 PocketBase 转为 `false`。若用户关闭三态，Schema 变为非空布尔；现有空白记录必须通过自动迁移显式填为 `false` 或 `true`，不能静默处理。

### 推荐默认

```jsonc
{
  "type": "bool",
  "required": false,
  "_vibe": {
    "appearance": "checkbox",
    "allowIndeterminate": true
  }
}
```

---

## 5.5 `date`：日期/日期时间

### 原生设置

| 设置 | 类型 | PB 默认 | 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `min` | DateTime/string | `""` | 不限制最早时间 | `""` | 高级 |
| `max` | DateTime/string | `""` | 不限制最晚时间 | `""` | 高级 |
| `required` | boolean | `false` | 允许空日期 | `false` | 常规 |

PocketBase 的规范输出格式为：

```text
YYYY-MM-DD HH:mm:ss.SSSZ
```

即存储和序列化精度为**毫秒**。

### PocketBase 没有“仅日期”物理类型

`date` 实际是 DateTime。VibeTable 应增加逻辑模式：

| 逻辑模式 | 推荐存储与显示策略 |
|---|---|
| 仅日期 | 存为该日期 `00:00:00.000Z`，显示时按“浮动日历日期”处理，不做本地时区换日 |
| 日期时间 | 写入 UTC，按用户/系统时区显示 |
| 仅时间 | 不建议直接复用 `date`；可保存为分钟数整数或规范文本 |

### 推荐常规选项

- 包含时间：默认关闭
- 默认值：关闭；可选固定值、今天、当前时间
- 日期格式：跟随系统区域
- 时间格式：24 小时制，跟随系统

### 推荐高级选项

- 时区策略：`floating-date` / `system` / 指定 IANA 时区
- 时间显示精度：分钟，默认；秒、毫秒为高级选项
- 最早/最晚时间
- 一周起始日、日期格式模板，仅影响显示

### 推荐默认

```jsonc
{
  "type": "date",
  "min": "",
  "max": "",
  "required": false,
  "_vibe": {
    "mode": "date",
    "timezone": "floating-date",
    "dateFormat": "system",
    "timeFormat": "system-24h",
    "datetimeDisplayPrecision": "minute"
  }
}
```

---

## 5.6 `autodate`：创建时间与最后更新时间

### 原生事实

| 设置 | 类型 | PB 结构体默认 | Schema 合法要求 | 含义 |
|---|---|---:|---|---|
| `onCreate` | boolean | `false` | 与 `onUpdate` 至少一个为 true | 记录首次保存时写入当前时间 |
| `onUpdate` | boolean | `false` | 与 `onCreate` 至少一个为 true | 记录以后每次成功保存时刷新 |

`false/false` 是 Go 零值，但不是合法配置。PocketBase v0.39.9 的 `AutodateField` 会拒绝普通 `record.Set(...)` 对该字段赋值，并在记录的 create/update execute 阶段写入服务器当前时间。事务失败时记录和时间一起回滚。

PocketBase v0.39.9 的 `NewBaseCollection` 默认只补 `id`，不会自动为普通 base collection 增加 `created` 和 `updated`。但是 PocketBase 自身的系统集合已经给出可复用模式：

```go
&core.AutodateField{Name: "created", OnCreate: true}
&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true}
```

因此 VibeTable 必须显式创建这两个字段，不能假定 PocketBase 已经提供。

### 产品只暴露两个角色

普通用户不直接编辑两个裸开关，也不开放 `false/true` 的“仅更新时间”组合：

| VibeTable 角色 | 推荐显示名 | `onCreate` | `onUpdate` | 产品语义 |
|---|---|---:|---:|---|
| `createdAt` | 创建时间 | `true` | `false` | 当前基础记录首次成功保存的时间 |
| `updatedAt` | 最后更新时间 | `true` | `true` | 当前基础记录最近一次成功保存的时间；创建后立即有值 |

`updatedAt` 必须采用 `true/true`，不能使用 `false/true`。否则新记录在第一次更新前是空值，既不符合“最后更新时间”的直觉，也与 PocketBase 自身的 `updated` 字段模式不一致。

每张 base table 每个角色最多一个。显示名可以修改，但角色创建后不可原地切换：

- `createdAt -> updatedAt` 会让旧值先表示创建时间、以后逐行变成更新时间；
- `updatedAt -> createdAt` 无法恢复已经被覆盖的原始创建时间。

需要改变角色时，必须新建另一个字段并执行显式迁移，不能只翻转 PocketBase 开关。

### 规范化字段模型

`onCreate/onUpdate` 是存储实现，不能放进 `editor.config`。在规范化 `FieldDefinition` 中增加可选的专用配置：

```jsonc
{
  "fieldId": "fld_created_at",
  "physicalName": "created_at",
  "displayName": "创建时间",
  "kind": "system",
  "dataType": "autoDate",
  "storageType": "autodate",
  "nullable": false,
  "defaultValue": null,
  "constraints": [],
  "editor": {
    "kind": "readonly",
    "config": {
      "timezone": "system",
      "displayPrecision": "minute"
    }
  },
  "readOnly": true,
  "autoDate": {
    "role": "createdAt"
  }
}
```

建议 Go 模型使用独立的深模块：

```go
type AutoDateRole string

const (
    AutoDateRoleCreatedAt AutoDateRole = "createdAt"
    AutoDateRoleUpdatedAt AutoDateRole = "updatedAt"
)

type AutoDateSpec struct {
    Role AutoDateRole `json:"role"`
}
```

`FieldDefinition.AutoDate *AutoDateSpec` 作为类型专用接口；`autoDate` 字段必须且只能出现在 `dataType="autoDate"` 的字段上。它在 v1 中以可选属性加入，先让所有严格解码器接受，再让生产端输出，符合 `contracts/v1/README.md` 的兼容规则。

### “系统字段”有两层含义

- VibeTable `kind="system"` + `readOnly=true`：禁止用户、粘贴、导入和插件直接赋值；
- PocketBase `AutodateField.System`：禁止删除、重命名，并冻结 `onCreate/onUpdate`。

VibeTable 管理的创建/更新时间字段必须采用第一层，但底层 PocketBase `System` 保持 `false`。当前 `schemaapi.ApplyChange` 会先移除旧的非 PocketBase-system 字段，再按稳定字段 ID 重编译；若把这些字段错误地设为 PocketBase `System=true`，后续 Schema 重放、删除和重建会与现有流程冲突。

### 最后更新时间的精确范围

首版采用 PocketBase 原生的“基础记录成功保存”语义：

| 操作 | `createdAt` | `updatedAt` |
|---|---:|---:|
| 新建记录 | 写入 | 写入 |
| 编辑标量字段、直接关系 | 不变 | 刷新 |
| 归档、恢复、附件变更 | 不变 | 刷新 |
| 公式回填/扇出导致基础记录保存 | 不变 | 刷新 |
| 写入相同值但仍执行成功保存 | 不变 | 刷新 |
| 查询、排序、筛选、导出 | 不变 | 不变 |
| Schema 修改 | 不变 | 不变 |
| 校验失败、事务回滚 | 不变 | 不变 |
| 仅修改 M2M/M2A 中间表 | 不变 | **不刷新源记录** |

最后一行是必须公开的语义边界：当前 `relation.Service` 对 junction 关系只向中间表提交 MutationKernel 请求，没有在同一事务中保存源记录。首版界面帮助文案应写“记录最后保存时间”，不得声称它覆盖所有可见关系投影的变化。若以后需要“逻辑行最后变更时间”，应新增明确能力，并先设计跨表原子 touch、并发守卫、审计和公式扇出，不能在关系服务里临时补一次无保护保存。

### 旧字段识别与旧记录

已有规范化 Schema 只有 `dataType="autoDate"`、没有 `autoDate.role`。兼容迁移必须读取底层 PocketBase 字段开关，不得按 `created_at`、`updated` 或显示名猜测：

| 底层组合 | 识别结果 |
|---|---|
| `true/false` | `createdAt` |
| `true/true` | `updatedAt` |
| `false/true` | legacy `updateOnly`；只读保留并要求显式迁移 |
| `false/false` | 非法 Schema，阻止写入 |

特别注意：仓库当前 `sidecar/internal/schema/compiler.go` 把所有 `autoDate` 固定编译为 `true/true`。因此现有 fixture 中名为 `created_at` 的字段并不能证明它具有创建时间语义；迁移时应按实际底层开关识别为 `updatedAt`，不能为了匹配名称静默改写历史含义。

向非空表新增时间字段时，PocketBase 只会给新增物理列填 `""`，不会反推旧记录时间。首版应采用严格边界：

- 只允许在新表或空表创建这两个角色，统一使用 `nullable=false`；
- 对非空表新增时返回稳定错误 `schema.field.autodate_backfill_required`，不能把迁移时刻伪装成旧记录的创建时间或最后更新时间；
- 后续开放时必须要求显式回填来源，例如另一个日期字段，或已证明覆盖全部记录生命周期的审计数据；
- 回填流程必须预检全部来源值、用 `SetRaw` 写入、逐行验证、失败回滚；不得根据字段名、记录 ID 或当前修改时间猜测；
- 若产品未来接受“启用跟踪前未知”，需要先为系统生成字段定义独立的历史未知状态，不能借用普通可空字段语义临时绕过。

### 已有代码的复用与缺口

| 位置 | 已有能力 | 本功能需要补齐 |
|---|---|---|
| `sidecar/internal/schema/types.go`、`capabilities.go` | 已有 `DataTypeAutoDate`、`StorageAutodate`、`FieldKindSystem` | 增加 `AutoDateRole/AutoDateSpec`，严格 JSON 往返 |
| `sidecar/internal/schema/validate.go` | 已强制 autoDate 为 system/readOnly，拒绝静态默认值 | 校验角色必填、每角色最多一个、配置互斥、角色不可原地改变 |
| `sidecar/internal/schema/compiler.go` | 已能生成 `core.AutodateField` | 按角色编译为 `true/false` 或 `true/true`，保持 PB `System=false` |
| `sidecar/internal/queryschema/source.go` | 已把 autoDate 映射成日期查询类型 | 补排序、范围筛选、空白旧记录和公式引用测试 |
| `sidecar/internal/mutation/kernel.go`、`apply.go` | system 字段已禁止客户端写入；保存后 `productRow` 包含服务器值 | 补创建、更新、回滚、幂等重放、附件与公式保存的时间行为测试 |
| `desktop/web-grid/src/services/schemaFieldDraft.ts` | 已有 `autoDate` 类型、system kind、readonly editor | 增加角色草稿；当前不能再输出无角色的空配置 |
| `desktop/web-grid/src/components/panels/SchemaFieldEditor.vue` | 创建表单已能选择“自动日期” | 改成“创建时间/最后更新时间”预设，隐藏 required/nullable/unique/default 开关 |
| `desktop/web-grid/src/grid/createGrid.ts` | 日期/日期时间已有只读格式化路径 | 明确 autoDate 列只读并复用日期格式；补时区与空白显示测试 |
| `contracts/v1/fixtures/table-definition.json` | 已有一个 autoDate 样例 | 为样例增加角色，并补一对 created/updated golden fixture |
| `backend/application/identifier_mapping_service.py` | 已把 `id/created/updated` 视为系统名，并跳过 `kind=system` 的映射 | 验证新的稳定物理名不会进入普通字段重命名流程 |

### 推荐默认

```jsonc
{
  "createdAt": {
    "type": "autodate",
    "onCreate": true,
    "onUpdate": false,
    "_vibe": {
      "role": "createdAt",
      "timezone": "system",
      "displayPrecision": "minute",
      "readOnly": true
    }
  },
  "updatedAt": {
    "type": "autodate",
    "onCreate": true,
    "onUpdate": true,
    "_vibe": {
      "role": "updatedAt",
      "timezone": "system",
      "displayPrecision": "minute",
      "readOnly": true
    }
  }
}
```

---

## 5.7 `email`：电子邮箱

### 原生设置

| 设置 | 类型 | PB 默认 | 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `onlyDomains` | string[] | `null`/`[]` | 不设允许名单 | `[]` | 高级 |
| `exceptDomains` | string[] | `null`/`[]` | 不设禁止名单 | `[]` | 高级 |
| `required` | boolean | `false` | 允许空字符串 | `false` | 常规 |

两种域名列表互斥，不能同时配置。

### 产品建议

- 保存前去除首尾空白。
- 至少将域名部分规范为小写，再执行名单判断；PocketBase 源码按字符串列表精确比较域名。
- “唯一邮箱”作为常规开关，底层使用忽略空字符串的唯一索引。
- 默认值通常关闭，不建议自动填充虚假邮箱。

### 推荐默认

```jsonc
{
  "type": "email",
  "onlyDomains": [],
  "exceptDomains": [],
  "required": false,
  "_vibe": {
    "trim": true,
    "normalizeDomainCase": true
  }
}
```

---

## 5.8 `url`：网址

### 原生设置

| 设置 | 类型 | PB 默认 | 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `onlyDomains` | string[] | `null`/`[]` | 不设允许名单 | `[]` | 高级 |
| `exceptDomains` | string[] | `null`/`[]` | 不设禁止名单 | `[]` | 高级 |
| `required` | boolean | `false` | 允许空字符串 | `false` | 常规 |

两种域名列表互斥。

### PocketBase 没有的设置

PocketBase 没有字段级“允许协议”选项。VibeTable 建议默认只允许：

```text
http
https
```

其他协议如 `mailto:`、`file:`、自定义协议应由高级用户明确开启，避免点击时产生意外行为。

### 推荐默认

```jsonc
{
  "type": "url",
  "onlyDomains": [],
  "exceptDomains": [],
  "required": false,
  "_vibe": {
    "allowedSchemes": ["http", "https"],
    "trim": true,
    "normalizeHostCase": true
  }
}
```

---

## 5.9 `select`：单选/多选

### 原生设置

| 设置 | 类型 | PB 原始默认 | 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `values` | string[] | `null`/`[]` | 空列表时 Schema 无法保存 | 草稿允许空；正式保存前必须至少 1 项 | 常规 |
| `maxSelect` | integer | `0` | `<=1` 均为单选，实际最多 1 项 | 单选显式 `1` | 常规 |
| `required` | boolean | `false` | 允许空字符串/空数组 | `false` | 常规 |

### 记录值

- 单选：字符串；空值为 `""`。
- 多选：去重后的字符串数组；空值为 `[]`。
- `maxSelect` 不能大于 `values.length`。
- 原生没有 `minSelect`；只能通过 `required` 表示“至少 1 项”。

### 推荐的选项数据模型

不要直接把显示文本同时作为底层值。建议：

```jsonc
{
  "pbValue": "opt_k82m3",
  "label": "进行中",
  "color": "blue",
  "order": 2,
  "archived": false
}
```

PocketBase `values` 保存稳定的 `pbValue`；VibeTable 元数据保存标签、颜色和顺序。这样把“进行中”改为“处理中”时，不需要重写全部记录。

### 多选的推荐上限语义

PocketBase 没有真正的“无限”值；`maxSelect<=1` 会变成单选。建议 VibeTable 提供两种模式：

1. **允许选择全部选项**：底层始终同步 `maxSelect = values.length`；
2. **固定上限**：用户输入上限，默认 `min(10, values.length)`。

默认从单选切到多选时，可选择“允许全部”，比硬编码 10 更符合用户直觉。

### 默认选中值

不要自动把第一项设为默认。默认值应保持关闭；用户明确选择后才启用。

### 推荐默认

```jsonc
{
  "type": "select",
  "values": [], // VibeTable 草稿可为空；提交 PB Schema 前必须至少有一项
  "maxSelect": 1,
  "required": false,
  "_vibe": {
    "mode": "single",
    "defaultEnabled": false,
    "multiLimitMode": "allOptions",
    "optionsUseStableIds": true
  }
}
```

---

## 5.10 `relation`：关系

### 原生设置

| 设置 | 类型 | PB 原始默认 | 有效含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `collectionId` | string | `""` | 必填；空时 Schema 无法保存 | 创建时必须选择目标表 | 常规 |
| `minSelect` | integer | `0` | 无最少数量限制 | `0` | 高级 |
| `maxSelect` | integer | `0` | `<=1` 为单关系 | 显式 `1` | 常规 |
| `cascadeDelete` | boolean | `false` | 不级联删除根记录 | `false` | 高级/危险 |
| `required` | boolean | `false` | 允许空关系 | `false` | 常规 |

### 记录值

- 单关系：一个记录 ID 字符串；空值为 `""`。
- 多关系：去重后的记录 ID 数组；空值为 `[]`。

### 关键限制

#### 目标集合保存后不能原地更换

PocketBase 会拒绝修改已存在关系字段的 `collectionId`。VibeTable 若允许用户更换目标表，必须执行迁移：

1. 创建新关系字段；
2. 尝试转换并复制旧值；
3. 验证结果；
4. 更新视图、公式、筛选等引用；
5. 删除旧字段；
6. 重命名元数据映射。

#### `minSelect` 与空值存在语义缺口

当关系值完全为空且 `required=false` 时，PocketBase 会直接放行，不再检查 `minSelect`。因此，要原生保证至少 N 个关系：

```text
required = true
minSelect = N
```

VibeTable 仍应自己做一致校验。

#### `cascadeDelete` 非常危险

其语义不是“删除本记录时顺带删除对方”，而是当所有关联记录被删除时，删除当前根记录。普通用户很容易误解，必须放在高级危险区并明确展示删除方向。

### 推荐关系显示方式

不要依赖 PocketBase `presentable` 作为 VibeTable 的显示字段。VibeTable 元数据应独立保存：

- 主显示字段
- 次显示字段
- 缩略图字段
- 搜索字段

### 推荐默认

```jsonc
{
  "type": "relation",
  "collectionId": "<required-before-save>",
  "minSelect": 0,
  "maxSelect": 1,
  "cascadeDelete": false,
  "required": false,
  "_vibe": {
    "mode": "single",
    "multipleDefaultMax": 10,
    "displayFieldId": null
  }
}
```

---

## 5.11 `file`：附件

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效默认 | VibeTable 推荐 | 分组 |
|---|---|---:|---:|---:|---|
| `maxSize` | integer，字节/文件 | `0` | 5 MiB/文件 | 显式 5 MiB | 常规 |
| `maxSelect` | integer | `0` | 单文件，最多 1 个 | 显式 `1` | 常规 |
| `mimeTypes` | string[] | `null`/`[]` | 不限制 MIME 类型 | `[]` | 常规 |
| `thumbs` | string[] | `null`/`[]` | 无额外缩略图尺寸 | `[]` | 高级 |
| `protected` | boolean | `false` | 文件默认不要求受保护令牌 | 本地单人模式 `false` | 高级 |
| `required` | boolean | `false` | 允许无附件 | `false` | 常规 |

### 单位

- `maxSize` 是**每个文件**的字节上限，不是整个字段的合计上限。
- 界面使用 KiB/MiB；Schema 写入字节。
- 5 MiB = `5_242_880` 字节。

### 多文件默认

- 单文件：`maxSelect=1`。
- 用户切换为多文件时：建议默认 `maxSelect=10`，可修改。

### MIME 类型

常规区使用预设并允许自定义：

- 图片
- 文档
- 视频
- 压缩包
- 不限制

底层仍存标准 MIME 字符串，不建议只存扩展名。

### 缩略图尺寸

PocketBase 支持的常见格式：

| 格式 | 含义 |
|---|---|
| `100x50` | 居中裁剪到 100×50 |
| `100x50t` | 从顶部裁剪 |
| `100x50b` | 从底部裁剪 |
| `100x50f` | 完整适配，不裁剪 |
| `0x50` | 固定高度，等比缩放 |
| `100x0` | 固定宽度，等比缩放 |

`thumbs=[]` 表示不配置额外尺寸；PocketBase 管理后台说明默认 `100x100` 缩略图仍作为基础尺寸存在。

### `protected` 的推荐默认

- PocketBase 仅绑定本机回环地址、单人桌面使用：默认 `false`，配置最简单。
- 允许局域网访问、远程访问或多人访问：建议新附件字段默认 `true`，并完整测试文件令牌与 View API rule。

### 推荐默认

```jsonc
{
  "type": "file",
  "maxSize": 5242880,
  "maxSelect": 1,
  "mimeTypes": [],
  "thumbs": [],
  "protected": false,
  "required": false,
  "_vibe": {
    "sizeUnit": "MiB",
    "multipleDefaultMax": 10
  }
}
```

---

## 5.12 `geoPoint`：地理坐标

### 原生设置

| 设置 | 类型 | PB 默认 | 真实含义 | VibeTable 推荐 | 分组 |
|---|---|---:|---|---|---|
| `required` | boolean | `false` | 开启后经纬度不能同时为 0 | PB 保持 `false`，普通必填由 VibeTable 处理 | 高级 |

记录零值为：

```json
{"lon": 0, "lat": 0}
```

范围：

- 纬度：`-90` 到 `90`
- 经度：`-180` 到 `180`

### 精度建议

PocketBase 没有经纬度小数位设置。VibeTable 建议：

| 设置 | 默认 |
|---|---:|
| 显示小数位 | 6 位 |
| 输入顺序 | 纬度、经度，界面明确标注；底层仍为 `{lon,lat}` |
| 地图拾取 | 开启 |

六位小数足以满足大多数桌面表格中的精细位置显示；该设置只影响显示，不裁剪底层值。

### 空白问题

真实世界中的 `(0,0)` 是合法坐标，而 PocketBase 又把它当作零值。VibeTable 必须用存在性元数据区分：

- 未填写；
- 明确填写 `(0,0)`。

### 推荐默认

```jsonc
{
  "type": "geoPoint",
  "required": false,
  "_vibe": {
    "displayScale": 6,
    "inputOrder": "lat-lon",
    "nullPolicy": "preserve"
  }
}
```

---

## 5.13 `json`：JSON

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效默认 | VibeTable 推荐 | 分组 |
|---|---|---:|---:|---:|---|
| `maxSize` | integer，字节 | `0` | 1 MiB | 显式 1 MiB | 高级 |
| `required` | boolean | `false` | 允许 `null` 或空值 | `false` | 常规 |

JSON 是 PocketBase 常规字段中唯一能自然保留 `null` 的类型。

### `required=true` 的特殊含义

它不只排除 `null`，也会排除多种“空 JSON”，包括空数组和空对象。因此，若用户需要“必须是对象，但允许 `{}`”，不要直接依赖 PocketBase `required`，应由 VibeTable 自行校验。

### VibeTable 建议增加

| 设置 | 默认 | 分组 |
|---|---:|---|
| JSON 根类型 | 任意 | 常规 |
| 默认值 | `null` | 常规 |
| 格式化缩进 | 2 空格 | 常规 |
| JSON Schema | 无 | 高级 |
| 最大大小 | 1 MiB | 高级 |
| 编辑模式 | 代码/树形 | 常规 |

### 推荐默认

```jsonc
{
  "type": "json",
  "maxSize": 1048576,
  "required": false,
  "_vibe": {
    "defaultValue": null,
    "rootType": "any",
    "indent": 2,
    "jsonSchema": null
  }
}
```

---

## 5.14 `password`：密码

### 定位

PocketBase 源码明确说明该类型通常只用于认证集合的内部 `password` 系统字段。PocketBase 管理后台的普通“新建字段”菜单也跳过该类型。

因此，在单人桌面多维表格中：

- 不应把它放进普通字段菜单；
- 可以在“实验性 / 开发者 / 系统字段”中展示；
- 如果用户只是想保存密码管理数据，也不应默认使用这个字段，因为读取后拿不到原始明文，且这不是完整的密码管理器安全模型。

### 原生设置

| 设置 | 类型 | PB 原始默认 | PB 有效默认 | 认证字段推荐 | 分组 |
|---|---|---:|---:|---:|---|
| `min` | integer | `0` | 无显式最低字符数 | `8` | 系统 |
| `max` | integer | `0` | 71 字符 | `71` | 系统 |
| `pattern` | string | `""` | 无正则约束 | `""` | 系统 |
| `cost` | integer | `0` | bcrypt 默认成本 10 | `10` | 系统 |
| `required` | boolean | `false` | 可为空 | 认证字段 `true` | 系统 |

### 行为与计量

- 数据库存储 bcrypt 哈希。
- 普通字段读取在持久化后不会返回原始密码；内部可通过 `fieldName:hash` 取哈希。
- `min`、`max` 的用户提示按 Unicode 字符计数。
- bcrypt 本身存在字节长度限制；多字节字符的“字符数”不等于字节数，因此不能把 71 个字符简单理解为任何语言都同样安全、同样可完整参与哈希。

### 推荐系统配置

```jsonc
{
  "type": "password",
  "hidden": true,
  "pattern": "",
  "min": 8,
  "max": 71,
  "cost": 10,
  "required": true,
  "_vibe": {
    "systemOnly": true,
    "showInAddFieldMenu": false
  }
}
```

---

## 6. 精度、长度和单位的统一规范

“精度”不能只给数字字段设置；所有涉及数量、长度、大小和时间的字段都应明确单位。

| 字段/设置 | PocketBase 实际单位或精度 | VibeTable 推荐显示 |
|---|---|---|
| `text.min/max` | Unicode 字符/rune | “字符数” |
| `password.min/max` | 用户提示按 Unicode 字符；bcrypt 仍受字节限制 | “字符”，另加字节限制说明 |
| `editor.maxSize` | 字节 | KiB/MiB |
| `json.maxSize` | 字节 | KiB/MiB |
| `file.maxSize` | 每个文件的字节 | MiB/文件 |
| `file/select/relation.maxSelect` | 项目数量 | “最多 N 个” |
| `relation.minSelect` | 项目数量 | “至少 N 个” |
| `number` | Go `float64` / IEEE 754 binary64 | 通用数字；只承诺声明的安全边界 |
| 整数 | 仍经 `float64` 数据通道 | 强制 JavaScript 安全整数范围 |
| 货币 | 通用数字 + 币种/格式元数据 | 不宣称精确十进制金额 |
| 百分比 | 比例值 + 显示格式 | 固定按比例值存储 |
| `date` | 毫秒 | 默认显示到分钟；秒/毫秒高级可选 |
| `geoPoint` | 无原生小数位设置 | 默认显示 6 位，默认不裁剪存储 |

### 数字精度建议模型

```ts
interface NumberFormatOptions {
  style: "decimal" | "integer" | "currency" | "percent" | "unit";

  // 显示层；修改后不回写记录
  displayScale: number;                 // 默认 2
  displayScaleMode: "fixed" | "max";  // 默认 max
  trimTrailingZeros: boolean;           // 默认 true
  useGrouping: boolean;                 // 默认 true

  // 语义层
  currencyCode?: string;
  percentStorage?: "ratio";             // 首版固定
  unit?: string;
}
```

界面统一使用“显示小数位”，不使用含糊的“精度”。首版没有字段级存储 scale 或写入舍入，因此修改显示小数位永远不修改已有数据。

未来若新增精确十进制能力，它必须是新的明确存储契约，而不能通过扩展 `displayScale` 偷渡。该能力需要同时定义表示范围、公式运算、舍入、导入导出和自动迁移，并作为独立项目评审。

---

## 7. 空白值设计：已确认采用固定存在性列

VibeTable 的产品契约中，空白值表示 `null`/未填写，必须与 `0`、`false`、`(0,0)`、空文本和空容器区分。

对 PocketBase 不能原生保存该区别的可空字段，在建字段时同步创建固定的内部布尔存在性列：

例如：

```text
f_price                    number
__vt_has_f_price           bool
f_approved                 bool
__vt_has_f_approved        bool
f_location                 geoPoint
__vt_has_f_location        bool
f_note                     text
__vt_has_f_note            bool
f_tags                     select
__vt_has_f_tags            bool
```

存在性列使用稳定物理字段名派生，不使用显示名称；用户重命名字段不会触发物理列重命名。创建成功后，逻辑字段元数据同时保存 `presenceFieldId` 和 `presencePhysicalName`，读取时不靠字符串猜测伴生列。

## 7.1 适用范围

所有允许产品 `null`、但 PocketBase 会把它折叠成零值的用户字段都必须创建存在性列：

- `number`：区分空白和 `0`；
- `bool`：区分空白和 `false`；
- `geoPoint`：区分空白和 `(0,0)`；
- `text`、`editor`、`email`、`url`、`date`：区分空白和 `""`；
- `select`、`relation`、`file`：区分空白和单值 `""` / 多值 `[]`。

JSON 的 `null` 可直接作为产品空白，不额外创建存在性列。`autodate` 由系统生成，不提供用户清空语义。系统 `password` 不进入普通字段菜单，按认证契约单独处理。

不采用：

- 记录级 `__vt_nulls` JSON；
- 先用 JSON、启用索引后再升级存在性列的混合方案；
- 发现空值后才临时创建列的惰性方案。

固定方案虽然增加少量内部列，但避免同一字段在不同表、不同时间出现两种空值协议，也让筛选、索引、公式和备份更容易验证。

## 7.2 读写契约

| 产品值 | 物理值列 | 存在性列 |
|---|---|---:|
| 空白数字 | `0` | `false` |
| 数字 `0` | `0` | `true` |
| 空白复选框 | `false` | `false` |
| 明确 `false` | `false` | `true` |
| 空白位置 | `{lon:0,lat:0}` | `false` |
| 真实 `(0,0)` | `{lon:0,lat:0}` | `true` |
| 空白文本 | `""` | `false` |
| 明确空文本 | `""` | `true` |
| 空白多选 | `[]` | `false` |
| 明确空集合 | `[]` | `true` |

规则：

1. 业务值与存在性列必须在同一权威事务中写入；
2. 读取时先看存在性列；为 `false` 时返回产品 `null`，不得把物理零值暴露为业务值；
3. “必填”校验检查存在性列，不映射到数字、布尔或 GeoPoint 的 PocketBase `required`；
4. “为空”“不为空”、计数、平均值、公式和唯一索引都以产品空白语义为准；
5. 存在性列只在 VibeTable 视图和字段选择器中隐藏，不轻易设置 PocketBase `hidden=true`，以免产品 API 无法读取；
6. 任何缺失、类型错误或与业务值不一致的存在性列都视为数据完整性错误，不能静默猜测。

## 7.3 旧数据接入与修复

若既有字段没有存在性列，系统不能从 PocketBase 零值可靠推断其原始状态。自动升级时：

1. 非零值可安全标记为存在；
2. 零值记录属于歧义数据，必须按表级迁移策略处理；
3. 默认策略是“既有零值视为已提供”，因为把真实 `0/false/(0,0)` 改为空白会丢失明确数据；
4. 迁移前显示歧义记录数量，迁移后校验值列与存在性列；
5. 迁移失败必须回滚 Schema 和数据，不留下半升级状态。

---

## 8. 默认值、唯一值和高级能力的底层实现

## 8.1 应用默认值

建议元数据：

```ts
interface FieldDefault {
  enabled: boolean;
  kind: "static" | "today" | "now" | "expression";
  value?: unknown;
  expression?: string;
}
```

写入规则：

1. 只在创建记录时应用；
2. 仅当用户没有明确提供该字段时应用；
3. 用户明确清空时不要再次补默认值；
4. 导入时提供“应用默认值 / 保留空白”选项；
5. 更新记录时绝不自动重放静态默认值。

## 8.2 唯一值

VibeTable 常规区建议显示：

```text
唯一值：关闭 / 忽略空白 / 包含空白
```

推荐默认是“关闭”；用户开启时默认“忽略空白”。

底层：

- 文本、email、url、date、单选、单关系：可创建部分唯一索引；
- 数字：只有在存在性语义明确后才安全支持“忽略空白”；
- 布尔：通常不提供唯一值；
- 多选、多关系、文件、JSON、富文本：默认不提供，或仅放高级实验区；
- 选项标签重命名不应影响唯一索引，因为底层保存稳定选项 ID。

PocketBase 自带认证集合也使用“空邮箱不参加唯一性”的部分唯一索引思路，说明这种映射方式符合其自身设计。

## 8.3 索引状态同步

每次字段设置变更应保证：

- 索引名称稳定且可推导；
- 字段删除时清理索引；
- 字段物理名迁移时更新索引表达式；
- 唯一索引创建前先扫描重复值，并给出冲突记录；
- 不要在发现重复值时直接丢数据或自动改值。

## 8.4 互操作导出

CSV/XLSX 面向外部查看和处理，不承担 VibeTable 无损恢复：

- 产品空白值导出为空单元格；
- 数值 `0` 导出为 `0`，不得与空白混同；
- 数字导出实际存储值，不按显示小数位截断；
- 货币符号、千分位等显示格式可以作为 XLSX 单元格格式；CSV 默认导出不带装饰的数值文本；
- 百分比按目标格式的常规数值语义输出，但同一导出任务必须保持一致并在导出选项中说明；
- CSV 无法无歧义地区分空白、空文本和部分空容器，不承诺无损往返。

不需要再为 CSV/XLSX 提供两套“`null` 或空白”契约。互操作导出统一为空单元格；真正的无损需求由备份承担。

## 8.5 无损备份

无损备份用于恢复完整 VibeTable 状态，必须包含：

- PocketBase 数据库及文件；
- VibeTable 逻辑 Schema、字段显示元数据和稳定 ID；
- 所有存在性列；
- 默认值、唯一索引、关系、视图、公式等产品元数据；
- 备份格式版本与迁移版本。

恢复后必须校验记录数、Schema、存在性列和关键索引。CSV/XLSX 导出不能称为备份。

## 8.6 字段数据迁移

所有会改变存储契约的字段设置都由 VibeTable 自动完成迁移，用户不需要导出、手工改列或重新导入：

1. **计划**：计算受影响字段、记录数、索引、关系和存在性列；
2. **预检**：扫描不可转换值、重复值、越界值与歧义零值；
3. **影子写入**：创建新物理列/元数据并转换数据，不覆盖原列；
4. **校验**：逐行核对转换结果、记录数、空白语义和索引约束；
5. **原子切换**：只在全部校验通过后切换逻辑字段映射；
6. **清理**：确认切换成功后再删除旧结构；
7. **回滚**：任一步失败时恢复旧映射和旧数据，并返回可操作的错误报告。

仅改变显示小数位、千分位、币种符号或单位时不迁移数据，因为这些设置不得改变原始值。普通数字切换为整数时先自动扫描；全部值均为安全整数才可直接切换，否则中止并列出冲突，不能自动舍入。

---

## 9. 推荐的“常规 / 高级”界面布局

## 9.1 所有字段的常规区

建议按以下顺序：

1. 字段显示名称
2. 字段类型或逻辑格式
3. 该类型最重要的输入设置
4. 默认值
5. 必填
6. 唯一值（适用时）
7. 描述

## 9.2 所有字段的高级区

建议按以下顺序分组：

### 数据校验

- 最小值/最大值
- 最小长度/最大长度
- 正则表达式
- 域名白名单/黑名单
- 最少/最多选择数

### 格式与精度

- 显示小数位
- 千分位、币种、百分比和单位
- 数字安全范围说明
- 时间精度和时区
- GeoPoint 显示精度
- 字节/大小上限

### PocketBase 映射

- 物理字段名
- PocketBase 类型
- 原始 Schema JSON 预览
- `presentable`
- `hidden`

### 危险操作

- `system`
- `primaryKey`
- 关系级联删除
- 关系目标迁移
- 字段物理类型转换
- 删除字段

## 9.3 不应混淆的两组设置

| 用户需求 | 正确实现 | 不应使用 |
|---|---|---|
| 在某个视图隐藏列 | VibeTable 视图元数据 | PocketBase `hidden` |
| 设置关系显示标题 | VibeTable relation display metadata | 单独依赖 `presentable` |
| 设置字段默认值 | VibeTable 创建管线 | 假想的 PB `default` 属性 |
| 设置唯一值 | 集合唯一索引 | 假想的 PB `unique` 属性 |
| 设置数字小数位 | VibeTable 格式元数据 | PB `number` 原生属性 |
| 表示数字为空 | 存在性元数据 | 直接保存 0 |

---

## 10. 建议的逻辑字段层

不要把 VibeTable 字段类型与 PocketBase 类型永久一一绑定。推荐增加逻辑类型层：

| VibeTable 逻辑类型 | PocketBase 物理类型 | 额外元数据 |
|---|---|---|
| 单行文本 | `text` | 输入外观 |
| 多行文本 | `text` | 行数、自动高度 |
| 富文本 | `editor` | 编辑器配置 |
| 通用数字 | `number` | binary64 契约、显示小数位 |
| 整数 | `number` | onlyInt=true、安全整数范围、显示 0 位 |
| 货币 | `number` | 通用数字 + 币种/显示格式；不承诺精确十进制 |
| 百分比 | `number` | 固定比例值 + 显示小数位 |
| 评分 | `number` | min/max、图标 |
| 复选框 | `bool` | 三态、标签 |
| 日期 | `date` | floating-date |
| 日期时间 | `date` | 时区、显示精度 |
| 创建时间 | `autodate` | role=createdAt、onCreate=true、onUpdate=false |
| 最后更新时间 | `autodate` | role=updatedAt、onCreate=true、onUpdate=true |
| 单选 | `select` | 稳定选项 ID、颜色 |
| 多选 | `select` | 稳定选项 ID、最大项数 |
| 附件 | `file` | 预览、MIME 预设 |
| 单向关系 | `relation` | 展示字段、卡片样式 |
| 电子邮箱 | `email` | 规范化规则 |
| 网址 | `url` | 允许协议 |
| 地理位置 | `geoPoint` | 地图、显示精度、空值语义 |
| JSON | `json` | JSON Schema、编辑模式 |
| 电话号码 | `text` | 电话格式与拨号行为 |
| 公式 | 建议物化到兼容 PB 字段或仅存元数据 | 表达式、返回类型 |
| 查找/汇总 | 物化字段或运行时计算 | 来源关系、聚合规则 |

这种分层能避免以后增加货币、百分比、评分、公式时被 PocketBase 的 14 个物理类型限制住。

---

## 11. 推荐默认配置模板（JSONC）

下面的 `_vibe` 属性是 VibeTable 自有元数据，不能原样提交给 PocketBase Schema API。

```jsonc
{
  "common": {
    "system": false,
    "hidden": false,
    "presentable": false,
    "help": "",
    "required": false,
    "_vibe": {
      "label": "",
      "columnHidden": false,
      "default": {
        "enabled": false,
        "kind": "static",
        "value": null
      },
      "uniqueMode": "none",
      "nullPolicy": "preserve",
      "presence": {
        "strategy": "fieldColumn",
        "fieldId": null,
        "physicalName": null
      }
    }
  },

  "text": {
    "min": 0,
    "max": 5000,
    "pattern": "",
    "autogeneratePattern": "",
    "primaryKey": false
  },

  "editor": {
    "maxSize": 1048576,
    "convertURLs": false
  },

  "number": {
    "min": null,
    "max": null,
    "onlyInt": false,
    "_vibe": {
      "style": "decimal",
      "displayScale": 2,
      "displayScaleMode": "max",
      "trimTrailingZeros": true,
      "useGrouping": true,
      "inputStep": "any",
      "currencyCode": "CNY",
      "unit": null,
      "percentStorage": "ratio"
    }
  },

  "bool": {
    "_vibe": {
      "appearance": "checkbox",
      "allowIndeterminate": true
    }
  },

  "date": {
    "min": "",
    "max": "",
    "_vibe": {
      "mode": "date",
      "timezone": "floating-date",
      "dateFormat": "system",
      "timeFormat": "system-24h",
      "datetimeDisplayPrecision": "minute"
    }
  },

  "autodateCreated": {
    "onCreate": true,
    "onUpdate": false,
    "_vibe": {
      "role": "createdAt"
    }
  },

  "autodateUpdated": {
    "onCreate": true,
    "onUpdate": true,
    "_vibe": {
      "role": "updatedAt"
    }
  },

  "email": {
    "onlyDomains": [],
    "exceptDomains": [],
    "_vibe": {
      "trim": true,
      "normalizeDomainCase": true
    }
  },

  "url": {
    "onlyDomains": [],
    "exceptDomains": [],
    "_vibe": {
      "allowedSchemes": ["http", "https"],
      "trim": true,
      "normalizeHostCase": true
    }
  },

  "select": {
    "values": [],
    "maxSelect": 1,
    "_vibe": {
      "mode": "single",
      "multiLimitMode": "allOptions",
      "optionsUseStableIds": true
    }
  },

  "relation": {
    "collectionId": "",
    "minSelect": 0,
    "maxSelect": 1,
    "cascadeDelete": false,
    "_vibe": {
      "mode": "single",
      "multipleDefaultMax": 10,
      "displayFieldId": null
    }
  },

  "file": {
    "maxSize": 5242880,
    "maxSelect": 1,
    "mimeTypes": [],
    "thumbs": [],
    "protected": false,
    "_vibe": {
      "sizeUnit": "MiB",
      "multipleDefaultMax": 10
    }
  },

  "geoPoint": {
    "_vibe": {
      "displayScale": 6,
      "inputOrder": "lat-lon"
    }
  },

  "json": {
    "maxSize": 1048576,
    "_vibe": {
      "defaultValue": null,
      "rootType": "any",
      "indent": 2,
      "jsonSchema": null
    }
  },

  "passwordSystemOnly": {
    "hidden": true,
    "pattern": "",
    "min": 8,
    "max": 71,
    "cost": 10,
    "required": true,
    "_vibe": {
      "showInAddFieldMenu": false
    }
  }
}
```

`presence.fieldId` 和 `presence.physicalName` 在 PocketBase 伴生列创建成功后回填；对 JSON、autodate 和系统字段设为 `null` 且不创建伴生列。

---

## 12. Schema 校验与迁移检查清单

实现字段设置界面时，保存前至少做以下交叉校验：

### 当前代码差距

以下是文档更新时已核实的实现现状，后续改造应优先消除这些静默风险：

| 位置 | 当前行为 | 需要调整 |
|---|---|---|
| `sidecar/internal/schema/compiler.go` | 用 `!Nullable` 等条件直接设置 PB `Required` | 对 number、bool、GeoPoint 等字段不能这样映射；产品必填检查存在性列 |
| `sidecar/internal/mutation/apply.go` | MutationKernel 写入业务字段，但尚未同步字段级存在性列 | 在同一事务中写值列与存在性列，并把规范值放入回执 |
| `desktop/web-grid/src/grid/editorFactory.ts` | `parseValue` 在前端调用 `Number(raw)` | 数字原始文本必须保留到权威写入层，避免安全整数和长小数先被改写 |
| `backend/application/import_service.py` | 数字导入先转 Python `float`；整数路径会再调用 `int(...)` | 使用与在线编辑相同的严格解析器；禁止小数被静默截成整数 |
| `backend/application/paste_service.py` | 缺少数字 Schema 元数据时按未类型化值继续 | 影响安全校验的 Schema 缺失必须阻止提交，不得 best-effort 降级 |
| `backend/application/export_service.py` | `_render_cell` 已将 `None` 输出为空单元格 | 与互操作导出契约一致；需补存在性解码后的集成测试 |
| `sidecar/internal/schema/validate.go`、`compiler.go` | 已拒绝 exact decimal 存储 | 与首版决策一致；前端类型、注释和公共契约也应同步移除或隐藏 decimal |
| `sidecar/internal/schema/compiler.go` | 所有 `autoDate` 都固定编译为 `onCreate=true/onUpdate=true` | 引入明确角色并分别编译为创建时间或最后更新时间；禁止继续从名称推断 |
| `contracts/v1/contracts.schema.json`、`sidecar/internal/schema/types.go` | `FieldDefinition` 没有自动日期角色，严格解码器会拒绝未知属性 | 先让所有消费者接受可选 `autoDate.role`，再更新 fixture 和生产端 |
| `desktop/web-grid/src/components/panels/SchemaFieldEditor.vue` | 自动日期仍显示 required、nullable、unique 等无效开关 | 改为两个预设并锁定 system/readOnly/non-null；隐藏无关选项 |
| `tests/e2e/webview_product_scenarios.mjs` | 只验证自动日期字段形状，没有验证时间行为 | 覆盖创建、更新、不变性、只读、回滚、重启与查询行为 |

这些文件中的现状不是新的平行规范；最终都必须服从本文件定义的产品写入边界。

### 公共

- 显示名可以是 Unicode；物理 `name` 必须符合 PocketBase 规则。
- `help` 不超过 300 字符。
- `hidden=true` 时不要同时依赖 `presentable`。
- 系统字段修改需要单独权限与确认。
- 需要存在性列的字段必须与伴生列同时创建、修改和删除。
- 存储契约变化必须经过预检、影子写入、校验、原子切换和失败回滚。

### 文本

- `max >= min`。
- 正则必须由 PocketBase/Go 正则验证，不能只用浏览器正则检查。
- 自动生成正则生成出的内容仍需满足 min/max/pattern。

### 数字

- `max >= min`。
- `onlyInt=true` 时 min/max 也必须是整数。
- 自行补充对 0 的 min/max 校验。
- 拒绝非有限数、解析溢出和非零值下溢为 0。
- 整数限制在 `[-9_007_199_254_740_991, 9_007_199_254_740_991]`。
- 原始输入在权威写入层解析；发生用户可见数值变化时明确拒绝。
- 显示小数位、千分位、币种和单位变化不得迁移或改写已有值。
- 通用数字切换为整数前自动扫描全列；存在非整数或越界值时中止并返回冲突记录。

### 日期

- `max >= min`。
- 日期模式切换为日期时间时，不要无提示改变已有时区语义。

### 自动日期

- `dataType="autoDate"` 必须且只能携带 `autoDate.role`；角色只能是 `createdAt` 或 `updatedAt`。
- 每张 base table 每个角色最多一个，重复时返回稳定的 Schema 错误。
- `createdAt` 必须编译为 `onCreate=true/onUpdate=false`；`updatedAt` 必须编译为 `true/true`。
- 固定为 `kind=system`、`readOnly=true`、`nullable=false`，不得设置默认值、required 或 unique。
- 底层 PocketBase `System=false`；产品系统字段与 PocketBase 冻结字段不可混淆。
- 已有字段的角色不可原地切换；兼容检查必须比较角色。
- 只允许向新表或空表新增；非空表返回 `schema.field.autodate_backfill_required`。
- 客户端编辑、粘贴、导入和插件写入均不得赋值；服务器回执应返回实际时间。
- `updatedAt` 表示基础记录最后成功保存时间，不覆盖只修改 junction 表的关系变化，也不能作为并发令牌。

### Email / URL

- allowlist 与 blocklist 互斥。
- 保存前规范化域名。

### Select

- 提交 PocketBase Schema 前至少有一个 `values` 项。
- 选项底层值必须唯一。
- `maxSelect <= values.length`。
- 删除仍被记录引用的选项时提供迁移选择。

### Relation

- `collectionId` 必须存在。
- `maxSelect >= minSelect`。
- 已保存后目标集合不能原地变更。
- 开启 `cascadeDelete` 前展示删除方向与影响记录数量。

### File

- `maxSize >= 0`，单位转换无溢出。
- `maxSelect >= 1`。
- MIME 列表使用合法标准值。
- 缩略图格式合法。
- 网络访问模式改变时重新评估 `protected`。

### GeoPoint

- 纬度在 `[-90,90]`。
- 经度在 `[-180,180]`。
- 空白与 `(0,0)` 分开。

### JSON

- 大小在序列化后按字节检查。
- JSON Schema 校验与 PB `required` 语义不要冲突。

### Password

- 仅系统场景使用。
- 成本值在 bcrypt 支持范围内。
- 多字节密码需要额外说明与测试。

---

## 13. 详细实施方案（审查版）

本节把字段能力建设拆成可独立评审、可验证和可回滚的工作包。创建时间与最后更新时间作为第一个纵向切片完整落地；其他字段能力按同一边界逐步推进，不在一次发布中同时改变所有存储语义。

### 13.1 范围、非目标与完成定义

本次纵向切片包含：

- 新表或空表可分别添加“创建时间”和“最后更新时间”，每个角色最多一个；
- 时间由 PocketBase 服务端生成，所有客户端写入入口均只读；
- 创建时间在首次成功保存后不变；最后更新时间在基础记录每次成功保存后刷新；
- Schema、查询、表格显示、导入/粘贴拒绝、插件拒绝、契约 fixture 和 E2E 行为一致；
- 识别旧 `autoDate` 的实际底层开关，不按字段名猜测；
- 非空表没有可信回填来源时明确阻止新增。

本次不包含：

- 从记录 ID、文件时间或迁移时刻猜测历史创建时间；
- 让 junction-only 关系修改刷新源记录 `updatedAt`；
- 将 `updatedAt` 替代 `rowRevision` 或摘要并发守卫；
- 开放 `false/true` 的 update-only 角色；
- 允许用户直接编辑 `onCreate/onUpdate` 或 PocketBase `System`；
- 将创建/更新时间开放给公式引用。当前公式编译器排除 `kind=system`，如需开放必须另行定义循环依赖、回填与重算语义。

满足以下条件才算完成：

1. 公共契约能无损往返两个角色，所有严格解码器一致；
2. 编译结果精确为 `createdAt=true/false`、`updatedAt=true/true`，且 PB `System=false`；
3. 所有写入入口不能覆盖自动时间，成功回执包含实际服务器值；
4. 创建、更新、回滚、幂等重放、重启、筛选和排序都有自动化测试；
5. 非空表新增、重复角色和角色切换均返回稳定错误；
6. 文档、中文/英文界面帮助文案与真实的“基础记录最后保存”边界一致；
7. 升级与降级限制经过发布审查，不依赖手工改数据库兜底。

### 13.2 工作包 A：先扩展契约和领域模型

必须先让消费者接受新属性，再让任何生产端输出；否则 `FieldDefinition.UnmarshalJSON` 和 JSON Schema 的严格校验会直接拒绝新 Schema。

| 顺序 | 文件 | 修改 | 验收 |
|---:|---|---|---|
| A1 | `contracts/v1/contracts.schema.json` | 新增 `AutoDateSpec`；在 `FieldDefinition` 增加可选 `autoDate`，`role` 枚举为 `createdAt/updatedAt`，对象禁止额外属性 | 合法双角色通过；缺角色、未知角色、额外属性失败 |
| A2 | `sidecar/internal/schema/types.go` | 增加 `AutoDateRole`、常量、`AutoDateSpec` 和 `FieldDefinition.AutoDate` | JSON marshal/unmarshal 无损；旧定义仍可读取以便迁移诊断 |
| A3 | `desktop/web-grid/src/contracts/index.ts` | 增加对应 TypeScript 类型和可选属性 | `tsc` 通过；前端不再用字符串散落表达角色 |
| A4 | `contracts/v1/fixtures/table-definition.json` | 将旧样例按实际兼容策略标注，并增加 created/updated 成对 golden fixture | Go、TS、Python 契约测试读取相同 fixture |
| A5 | `contracts/v1/fixtures/rpc-catalog.json` 及生成产物 | 通过仓库生成流程刷新，不手改重复片段 | 生成结果可复现，工作树二次生成无差异 |

兼容落地顺序必须是：

1. reader-first：部署能读取“有/无 `autoDate`”两种形状的所有消费者；
2. producer-second：更新创建表单、fixture 和 Schema API 输出；
3. enforcement-third：最后把新建 `autoDate` 缺少角色改为硬错误。

旧定义缺少 `autoDate` 仅允许进入“旧 Schema 识别”路径，不能作为新建或再次保存时的合法最终状态。建议稳定错误码：

| 场景 | 错误码 |
|---|---|
| 新自动日期缺少角色 | `schema.field.autodate_role_required` |
| 角色未知 | `schema.field.autodate_role_invalid` |
| 同角色重复 | `schema.field.autodate_role_duplicate` |
| 非自动日期携带配置 | `schema.field.autodate_config_forbidden` |
| 已有字段切换角色 | `schema.field.autodate_role_immutable` |
| 非空表需要回填 | `schema.field.autodate_backfill_required` |

### 13.3 工作包 B：Schema 校验、兼容检查与编译

| 顺序 | 文件 | 修改 | 验收 |
|---:|---|---|---|
| B1 | `sidecar/internal/schema/validate.go` | 实现配置互斥、合法角色、每表唯一、system/readOnly/non-null、无默认值/required/unique 等不变量 | 表驱动测试覆盖每个错误分支和稳定错误路径 |
| B2 | `sidecar/internal/schema/compiler.go` | 删除 autoDate 的 `true/true` 硬编码；按角色编译；显式保持 PB `System=false` | 单元测试检查具体 `core.AutodateField` 属性，不只检查类型 |
| B3 | `sidecar/internal/schemaapi/catalog.go` 的 `validateCompatibleAlter` | 比较旧新 `autoDate.role`，拒绝原地切换；删除仍走显式 Schema 变更 | 角色切换在落库前失败，原 Schema 保持不变 |
| B4 | `sidecar/internal/schemaapi/catalog.go` 的 ApplyChange 事务 | 创建字段前检查表是否为空；非空时返回回填错误；空表创建失败必须整体回滚 | PB collection、definition JSON、字段 ID 三者不会部分提交 |
| B5 | Schema 读取/调和路径 | 从实际 PB `OnCreate/OnUpdate` 识别旧角色，记录 update-only/非法组合诊断 | 不按 physicalName/displayName 猜测；重复运行结果一致 |

编译器测试至少固定以下真值表：

| 角色 | `OnCreate` | `OnUpdate` | `System` |
|---|---:|---:|---:|
| `createdAt` | `true` | `false` | `false` |
| `updatedAt` | `true` | `true` | `false` |

表是否为空的检查和 Schema 修改必须在同一受控事务/临界区内完成，避免预检后并发插入第一条记录。若当前 PocketBase API 无法在同一事务中完成，应在 `schemaapi` 层取得表级写锁，阻止检查到提交之间出现记录写入。

### 13.4 工作包 C：创建界面与字段编辑器

| 顺序 | 文件 | 修改 | 验收 |
|---:|---|---|---|
| C1 | `desktop/web-grid/src/services/schemaFieldDraft.ts` | 草稿模型增加角色；由两个预设生成固定 system/readOnly/non-null 定义 | 单测快照精确包含角色，不再产生无角色 autoDate |
| C2 | `desktop/web-grid/src/components/panels/SchemaFieldEditor.vue` | 将“自动日期”改为“创建时间/最后更新时间”入口；隐藏 required、nullable、unique、default | 组件测试确认无效控件不可见且不能通过事件改写 |
| C3 | `desktop/web-grid/src/components/panels/CreateTableModal.vue` | 新表创建时可勾选两个系统列；默认建议同时创建，但用户可分别关闭 | 生成顺序稳定，显示名冲突有明确处理 |
| C4 | `desktop/web-grid/src/i18n/locales/zh-CN.ts`、`en-US.ts` | 增加角色名、只读原因、非空表阻止和 junction 边界说明 | i18n key 完整性测试通过，无回退到裸 key |
| C5 | 表格列菜单/字段详情 | 角色只读展示；允许改显示名和显示格式，但禁止改角色 | UI 与后端双重拒绝，不能只靠禁用按钮 |

帮助文案必须使用：

- 创建时间：“记录首次成功保存的时间”；
- 最后更新时间：“记录最后成功保存的时间；仅修改关联中间表时不会刷新”。

不要使用“任何可见内容变化都会刷新”或“行版本”这类超过当前实现的描述。

### 13.5 工作包 D：写入、读取、查询和显示

| 顺序 | 位置 | 修改/确认 | 验收 |
|---:|---|---|---|
| D1 | `sidecar/internal/schema/value.go`、mutation kernel | 保持 autoDate 对客户端值的硬拒绝；校验创建、编辑、粘贴、导入、插件路径都走同一规则 | 每个入口提交时间值都得到同类结构化错误 |
| D2 | `sidecar/internal/mutation/apply.go` | 保持 PB 保存先发生，再从 `productRow` 返回自动生成值 | 新建回执两列非空；更新回执返回新 `updatedAt` |
| D3 | 附件、归档、直接关系和公式回填路径 | 确认凡是保存基础记录的操作会刷新 `updatedAt` | 行为测试与第 5.6 节矩阵一致 |
| D4 | `sidecar/internal/relation` | 暂不 touch 源记录，明确 junction-only 不刷新 | 回归测试锁定当前语义，防止未来无意改变 |
| D5 | `sidecar/internal/queryschema/source.go`、query compiler | 继续按日期类型暴露，补范围筛选、排序、空白 legacy 值 | 时区/UTC 比较正确，排序稳定 |
| D6 | `desktop/web-grid/src/grid/createGrid.ts` | 复用日期时间格式化，强制只读，支持系统时区和显示精度 | 空值、有效值、时区切换和复制显示值测试通过 |
| D7 | 并发冲突路径 | 继续使用 row revision/digest；客户端传入 `updatedAt` 时拒绝整次 mutation | 相同时间粒度下的并发写入仍能被现有守卫检测 |

`updatedAt` 采用 PB 的“成功 Save”语义，因此写入相同业务值但实际调用 Save 仍会刷新。这不是差异检测时间。幂等键重放则必须返回首次提交的同一回执，不得再次 Save 或再次刷新。

### 13.6 工作包 E：旧 Schema 与非空表迁移

升级扫描按底层开关分类：

1. `true/false`：补记为 `createdAt`；
2. `true/true`：补记为 `updatedAt`；
3. `false/true`：标为 legacy `updateOnly`，保持只读并阻止普通编辑；
4. `false/false`：标记损坏，阻止写入并要求修复。

扫描必须先生成只读报告，报告至少包含 table ID、field ID、physical name、display name、底层组合、建议角色和冲突。只有无重复角色、无非法组合时才能自动提交元数据补全。当前 fixture 的 `created_at` 因编译器历史硬编码会落入 `true/true`，必须识别为 `updatedAt`，不得为迎合名称改成 `createdAt`。

非空表首版处理：

- 不创建物理字段，不修改 definition JSON；
- 返回 `schema.field.autodate_backfill_required` 和记录数量；
- UI 解释“现有记录没有可信历史时间”，不提供“使用现在”快捷按钮；
- 用户可取消，不产生残留列、索引或半完成迁移。

后续回填能力单独立项时，采用“预检—影子字段—全量写入—逐行校验—原子切换—清理”流程。允许的来源必须由用户明确选择并记录审计；通过 `SetRaw` 写历史值后再启用自动更新。任何失败保留原字段和原定义。

### 13.7 工作包 F：自动化测试与审查矩阵

| 层级 | 建议文件 | 必测场景 |
|---|---|---|
| 契约 | contracts/contract tests | 新旧 JSON、合法角色、未知属性、fixture 跨语言一致 |
| Schema 单元 | `sidecar/internal/schema/schema_test.go` | 不变量、重复角色、编译真值表、PB System=false |
| Schema 集成 | `sidecar/internal/schemaapi/*_test.go` | 空表新增、非空表阻止、角色不可变、事务回滚、重启后定义一致 |
| Mutation 单元/集成 | `sidecar/internal/mutation/*_test.go`、`sidecar/tests/integration/mutation_kernel_test.go` | 首次写入、created 不变、updated 刷新、只读拒绝、幂等回执、失败回滚 |
| Query | `sidecar/internal/queryschema/source_test.go`、query tests | 等值/范围/排序、legacy 空值、UTC 边界 |
| 前端服务 | `schemaFieldDraft.test.ts` | 两个预设和固定属性 |
| 前端组件 | `SchemaFieldEditor.test.ts`、`CreateTableModal.test.ts` | 控件隐藏、默认选择、错误展示、i18n |
| 适配器 | `tests/backend/adapters/test_pocketbase_mutation.py`、plugin mutation tests | 导入、粘贴、插件不能覆盖系统时间 |
| 产品 E2E | `tests/e2e/webview_product_scenarios.mjs` | 创建表—写记录—更新—查询—重启的完整行为 |

关键断言：

1. 新建后两个时间均为可解析的 UTC 时间，但不强制二者字节级相等；
2. 更新普通字段后 `createdAt` 字节值不变，`updatedAt` 严格晚于旧值；
3. 为避免易碎 sleep，单元层以编译真值表为主；需要真实时钟的集成层使用可轮询条件和合理超时；
4. 校验失败或事务回滚后两个值都不改变；
5. 同一幂等键重放返回完全相同的时间和 receipt；
6. 写入相同值但产生新的成功 Save 时 `updatedAt` 刷新；
7. 公式回填、附件和归档保存基础记录时刷新；
8. junction-only 修改不刷新源记录；
9. Schema 修改、查询和导出不刷新；
10. 客户端携带伪造时间时整次 mutation 被拒绝，而不是静默忽略；
11. 进程重启后角色、字段 ID、历史值和只读属性保持；
12. `updatedAt` 不参与并发守卫替换，原并发测试继续通过。

### 13.8 发布、可观测性与回滚

推荐一个版本内按以下提交顺序合并，每步都应可单独评审：

1. 契约 reader 和领域类型；
2. 旧 Schema 扫描/诊断与验证器；
3. 编译器和 Schema API；
4. 写入/查询行为测试；
5. 前端预设和文案；
6. fixture、RPC catalog 与产品 E2E；
7. 发布开关从只读诊断切到允许新表/空表创建。

发布指标至少记录：

- `autodate_role_required/duplicate/immutable/backfill_required` 计数；
- 旧字段四种底层组合的扫描数量；
- Schema 创建回滚次数；
- mutation 中客户端试图写系统时间的拒绝次数；
- 自动日期读取/解析失败数量。

回滚审查必须注意：公共契约使用严格解码，旧二进制看见新的 `autoDate` 属性可能直接拒绝整个 definition。因此一旦生产端已经写出新属性，不能简单降级到不认识该属性的旧版本。可接受的回滚策略是保留 reader 支持、关闭 producer 功能；只有另行执行经过验证的兼容迁移移除新元数据后，才允许回到更旧 reader。

### 13.9 整体字段能力路线图

创建/更新时间完成后，后续字段设置继续按“契约 → 权威写入 → 迁移 → UI → E2E”的顺序推进：

| 阶段 | 目标 | 具体交付 | 进入下一阶段的门槛 |
|---|---|---|---|
| 1. 语义正确 | 不静默丢值或改值 | 显示名/物理名分离；字段元数据；创建默认值；固定存在性列；统一写入边界；数字原始输入校验；唯一索引管理；互操作导出与无损备份分离 | 在线编辑、导入、粘贴、插件、撤销和迁移使用同一契约；失败可回滚 |
| 2. 常用体验 | 丰富显示而不改变存储 | 稳定选项 ID；日期/日期时间及时区；附件限制与缩略图；关系显示字段；货币/百分比/评分预设；通用字段迁移执行器 | 格式变化不改原值；存储变化均有预检、进度、校验和回滚 |
| 3. 高级能力 | 在明确边界内开放高级配置 | JSON Schema；三态布尔；高级唯一索引；开发者 Schema JSON；系统/密码字段管理 | 权限、审计、错误恢复和兼容策略齐备 |
| 4. 独立立项 | 避免用显示能力冒充存储保证 | 精确十进制、逻辑行更新时间、公式引用系统时间 | 有明确业务需求、独立领域模型和端到端迁移方案 |

### 13.10 后续字段能力的可执行工作包

时间字段纵向切片完成后，剩余计划按以下工作包实施。每个工作包都要同时交付契约、后端、前端、迁移和测试，禁止只做设置界面而没有权威写入语义。

#### 工作包 G：统一字段元数据与物理命名

| 步骤 | 位置 | 实施内容 | 验收 |
|---:|---|---|---|
| G1 | `contracts/v1/contracts.schema.json`、各语言契约类型 | 固化 label、physicalName、display、default、presence、unique 和类型专用配置的归属层级 | 未知属性失败；所有支持属性可跨语言无损往返 |
| G2 | `backend/application/identifier_mapping_service.py`、Schema API | 只在创建时生成稳定物理名；重命名只改显示名 | Unicode、重名、保留字和 `_via_` 用例通过；已有物理名不漂移 |
| G3 | `SchemaFieldEditor.vue`、字段详情 | 常规/高级/危险区按第 9 节分组；不显示底层不适用开关 | 每种字段的控件矩阵有组件测试 |
| G4 | catalog 读取与诊断 | 检测显示名/物理名混用和历史不合法名称，生成只读修复报告 | 诊断幂等；未确认前不改 Schema |

审查门槛：任何新增配置都必须回答“属于存储、约束、显示还是迁移策略”，不得再以开放的 `editor.config` 承担存储行为。

#### 工作包 H：默认值、空白存在性与统一写入边界

| 步骤 | 位置 | 实施内容 | 验收 |
|---:|---|---|---|
| H1 | Schema compiler/catalog | 为需要保留空白的字段创建稳定伴生存在性列，并在 definition 中记录 fieldId/physicalName | 值列与存在性列同生同灭，任一步失败整体回滚 |
| H2 | `sidecar/internal/mutation/apply.go` | 同一事务写入业务值和存在性列；创建时由服务端注入应用默认值 | 空白、零值、显式 false、`(0,0)`、空容器均可区分 |
| H3 | 在线编辑、导入、粘贴、插件、撤销 | 统一调用同一解析/校验内核，不在适配器提前损失原始输入 | 同一非法输入在所有入口得到同一错误类和字段路径 |
| H4 | mutation receipt/前端状态 | 始终使用服务端规范值和存在性状态回写 UI | 前端不依据本地猜测提交结果 |
| H5 | 默认值引擎 | 默认值只在记录创建且字段未提供时求值；保存求值来源和版本 | 显式空白不会被默认值覆盖；重试/幂等行为确定 |

审查门槛：完成崩溃恢复、事务回滚和并发创建测试后，才能让用户创建带存在性列的新字段。

#### 工作包 I：索引、唯一性和字段迁移执行器

| 步骤 | 位置 | 实施内容 | 验收 |
|---:|---|---|---|
| I1 | Schema index manager | 用稳定索引 ID 管理普通/部分唯一索引；空白策略进入索引谓词 | 重命名字段不丢索引；重复值报告冲突记录 |
| I2 | 迁移预检 | 在类型、nullable、onlyInt、select 选项或关系目标变化前扫描全列 | 返回总数、冲突数和有代表性的记录 ID，不写入数据 |
| I3 | 迁移执行 | 影子列/影子索引、分批回填、逐批校验、原子切换、延迟清理 | 中断可恢复；失败保持旧读写路径 |
| I4 | 迁移审计 | 记录发起者、旧新契约、来源、批次进度、校验摘要和回滚状态 | 可以解释每次物理 Schema 变化 |
| I5 | UI | 显示预检影响、不可逆风险、进度、冲突下载和取消/重试状态 | 不以模糊 loading 掩盖长迁移 |

审查门槛：迁移执行器未完成前，所有会改变既有值解释的设置必须禁用，而不是直接调用 PocketBase Schema 更新。

#### 工作包 J：按风险批次完善字段族

| 批次 | 字段 | 主要实现 | 必测边界 |
|---|---|---|---|
| J1 | text、editor、email、url | 长度/字节限制、正则、域名规则、URL scheme、规范化 | Unicode 长度、超限、Go/浏览器正则差异、大小写与空白 |
| J2 | number、bool、geoPoint | 原始文本权威解析、安全整数、有限数、显示格式、三态和坐标范围 | 长小数不提前改写、上下溢、`-0`、空白与零值分离、经纬度边界 |
| J3 | date、select | floating-date/UTC 语义、显示精度、稳定选项 ID、多选上限 | DST、跨时区、删除在用选项、标签重命名不改存储值 |
| J4 | relation、file | 关系目标/显示字段、junction 语义、文件数量/大小/MIME/缩略图/protected | 级联方向、循环关系、附件事务补偿、权限与下载路径 |
| J5 | json | 大小限制、根类型、格式化、可选 JSON Schema | 序列化后字节、`null`、深层错误路径、Schema 版本迁移 |
| J6 | password、开发者系统字段 | 从普通菜单隐藏，独立权限、二次确认和审计 | 哈希不回显、成本范围、多字节密码、禁止批量导出 |
| J7 | formula、lookup、rollup | 独立领域模型、依赖图、计算/物化策略和重算队列 | 循环依赖、删除来源、批量重算、失败隔离与一致性 |

每个批次的上线条件：

1. 规范化模型和存储映射已定稿；
2. 解析器以原始输入为起点，服务端返回规范值；
3. 空白、默认值、唯一性和迁移语义已有决策；
4. 在线、导入、粘贴、插件和导出测试齐全；
5. PocketBase 升级/降级兼容性已验证；
6. UI 不暴露 PocketBase 不支持或产品尚未兑现的能力。

#### 工作包 K：导出、备份、恢复与可观测性

| 步骤 | 实施内容 | 验收 |
|---:|---|---|
| K1 | CSV/XLSX 只输出互操作值和空单元格规则，并明确哪些元数据会丢失 | 跨 Excel/LibreOffice 回归样例稳定 |
| K2 | 无损备份包含规范化 Schema、PB collection、存在性列、索引、关系、附件清单和版本 | 新环境恢复后字段 ID、空白语义和时间字段完全一致 |
| K3 | 恢复前检查目标版本、插件依赖、空间和冲突；恢复后做记录数与摘要校验 | 不兼容时在写入前失败；失败可回到原工作区 |
| K4 | 对解析拒绝、迁移冲突、索引失败、存在性不一致和自动日期异常建立指标 | 指标不包含密码、secret 或原始敏感单元格 |

#### 工作包 L：发布波次和总体验收

| 波次 | 开放范围 | 退出条件 |
|---|---|---|
| L0 内部诊断 | 只读 Schema 扫描、契约 reader、指标 | 无未知字段形状，旧数据分类报告完成 |
| L1 新表 | 新表启用时间字段和已完成的低风险字段设置 | 创建/编辑/重启 E2E 全绿，无回滚残留 |
| L2 空表改造 | 允许空表现有 Schema 增加字段 | 空表判定与提交无竞态 |
| L3 非空表迁移 | 只开放迁移执行器已支持的变化 | 中断恢复、冲突报告、原子切换和回滚演练通过 |
| L4 高级能力 | JSON Schema、系统字段、公式等按权限逐项开关 | 安全、性能、审计和降级评审分别通过 |

总体验收必须在真实打包桌面应用和所支持的 sidecar 组合上执行，不能只依赖开发服务器。发布前保存一份生成产物无差异证明、迁移演练记录、回滚演练记录和已知语义边界清单。

### 13.11 审查结论与发布阻断项

| 级别 | 审查发现 | 结论/阻断条件 |
|---|---|---|
| P0 | compiler 将所有 autoDate 硬编码为 `true/true` | 修复并通过真值表测试前不得宣称支持创建时间 |
| P0 | 非空表新增不会自动生成可信历史时间 | 首版硬阻止；不得提供“以当前时间回填”的默认捷径 |
| P0 | 旧 reader 会因严格解码拒绝新 `autoDate` 属性 | reader-first 完成且降级方案评审通过前，producer 不得写新属性 |
| P1 | “空表检查”与 Schema 提交之间可能并发插入 | 必须在同一事务或表级锁内完成，否则不开放空表改造 |
| P1 | 把 PB `System` 设为 true 会破坏现有 Schema 重放 | 编译测试必须固定为 false；代码评审检查不得省略 |
| P1 | junction-only 关系变化不保存源记录 | 首版明确文案并锁定回归测试；不得把字段称作“逻辑行版本” |
| P1 | 幂等重放若再次 Save 会错误刷新时间 | 回执缓存命中必须在 Save 前短路，集成测试比较完整 receipt |
| P1 | 用户伪造系统时间若被静默忽略会掩盖错误 | 所有入口统一拒绝整次 mutation，返回字段级结构化错误 |
| P2 | 公式回填/扇出会因保存基础记录而刷新 `updatedAt` | 作为当前语义记录并测试；未来如需区分用户编辑时间，另建字段 |
| P2 | `updatedAt` 的时钟精度不适合作为并发令牌 | 保留 row revision/digest；性能测试不得以时间字段代替冲突守卫 |

本轮审查没有发现需要修改 PocketBase 源码的必要性。正确路径是复用原生 `AutodateField`，在 VibeTable 的规范化模型、编译器、写入边界和迁移层补齐角色与产品语义。

每个后续能力都应复用本次纵向切片的审查清单：是否改变物理存储、是否折叠空白、所有写入入口是否一致、旧数据如何识别、失败如何回滚、降级 reader 是否仍能读取，以及 UI 文案是否夸大真实语义。

---

## 14. 来源

### PocketBase v0.39.9 源码

- [公共 Field 接口与名称校验](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field.go)
- [TextField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_text.go)
- [BoolField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_bool.go)
- [NumberField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_number.go)
- [DateField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_date.go)
- [AutodateField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_autodate.go)
- [Base collection 默认字段构造](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/collection_model.go)
- [PocketBase 内部 created/updated 字段迁移范式](https://github.com/pocketbase/pocketbase/blob/v0.39.9/migrations/1640988000_init.go)
- [EditorField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_editor.go)
- [EmailField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_email.go)
- [URLField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_url.go)
- [SelectField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_select.go)
- [RelationField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_relation.go)
- [FileField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_file.go)
- [GeoPointField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_geo_point.go)
- [JSONField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_json.go)
- [PasswordField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_password.go)
- [DateTime 序列化格式](https://github.com/pocketbase/pocketbase/blob/v0.39.9/tools/types/datetime.go)

### PocketBase 管理后台 v0.39.9 源码

- [公共字段设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/base/fieldSettings.js)
- [新字段初始公共属性](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/collections/addCollectionFieldButton.js)
- [Select 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/select/settings.js)
- [Relation 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/relation/settings.js)
- [File 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/file/settings.js)
- [Autodate 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/autodate/settings.js)

### 官方文档

- [Collections](https://pocketbase.io/docs/collections/)
- [Go collection operations / indexes](https://pocketbase.io/docs/go-collections/)

### 同类产品对标

- [飞书多维表格字段编辑指南](https://open.feishu.cn/document/server-docs/docs/bitable-v1/app-table-field/guide)
- [WPS 多维表格基础字段详解](https://bbs.wps.cn/topic/18232)
- [WPS 多维表格创建字段 API](https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/fields/create-field)
- [NocoDB Number](https://nocodb.com/docs/product-docs/fields/field-types/numerical/number)
- [NocoDB Decimal](https://nocodb.com/docs/product-docs/fields/field-types/numerical/decimal)
- [NocoDB Currency](https://nocodb.com/docs/product-docs/fields/field-types/numerical/currency)
- [NocoDB Scripts Field API](https://nocodb.com/docs/scripts/api-reference/field)
- [本项目竞品调研记录](./research/2026-07-27-field-number-precision-competitor-comparison.md)

---

## 15. 已确认的产品决策

1. VibeTable 仪表盘是产品的数据展示功能；PocketBase `/_/` 是开发者危险入口，不作为常规数据或 Schema 写入入口。
2. 产品空白值是一等状态，必须与 `0`、`false`、`(0,0)`、空文本和空容器按字段契约区分。
3. 对 PocketBase 无法原生保留空白的可空字段，固定使用字段级存在性列；不使用 `__vt_nulls` JSON 或混合升级方案。
4. CSV/XLSX 是互操作导出：`null` 统一为空单元格；完整恢复使用独立无损备份。
5. 首版只开放 IEEE 754 binary64 通用数字；整数、显示小数位、千分位、百分比、货币和单位属于约束或格式预设。
6. 货币不承诺精确十进制或会计级计算；精确小数首版不开放。
7. 显示格式变化永不修改原始值；任何存储契约变化都必须由 VibeTable 自动迁移、校验、原子切换并支持失败回滚。
8. 所有入口统一经过产品写入边界；不允许溢出、越界、空值折叠、最佳努力降级或其他静默错误。
9. 创建时间和最后更新时间是同一 `autoDate` 数据类型的两个不可变角色，分别映射为 PocketBase `true/false` 与 `true/true`；不向普通用户开放 update-only。
10. 最后更新时间采用“基础记录最后成功保存时间”语义；junction-only 关系变化不刷新源记录，且该字段不能替代 row revision/digest 并发守卫。
11. 两个时间字段在 VibeTable 中是 system/readOnly，但底层 PocketBase `System` 保持 `false`，以兼容 Schema 重放和显式删除流程。
12. 首版只允许在新表或空表创建时间角色；非空表没有可信来源时阻止新增，绝不以迁移时刻伪造历史。
13. 旧自动日期按 PocketBase 实际 `OnCreate/OnUpdate` 开关识别，不按字段名或显示名猜测；角色切换必须新建字段并显式迁移。
