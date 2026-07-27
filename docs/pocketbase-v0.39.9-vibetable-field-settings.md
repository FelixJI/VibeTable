# PocketBase v0.39.9 字段设置、默认值与桌面多维表格暴露方案

> 适用对象：单人桌面多维表格软件（下文暂称 **VibeTable**）  
> PocketBase 版本：**v0.39.9**  
> 调研日期：**2026-07-27**  
> 依据：PocketBase v0.39.9 标签对应的 Go 源码、Dashboard 源码与官方 Collections 文档。

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

### 1.3 `number` 没有原生精度、标度、步长和舍入模式

PocketBase 的 `number` 值在 Go 层按 `float64` 处理，原生只有：

- `min`
- `max`
- `onlyInt`
- `required`

“显示几位小数”“写入时保留几位”“千分位”“货币”“百分比”“舍入方式”等都必须由 VibeTable 自己定义。

### 1.4 空白与零值默认无法区分

这是多维表格中最需要提前决定的问题：

| 用户语义 | PocketBase 原生落值 | 是否丢失语义 |
|---|---:|---:|
| 数字为空 | `0` | 是：无法区分空白与真实的 0 |
| 复选框为空 | `false` | 是：无法区分未填写与明确否定 |
| 地理位置为空 | `{lon:0,lat:0}` | 是：无法区分空白与真实坐标 `(0,0)` |
| 日期为空 | `""` | 通常可接受 |
| 文本为空 | `""` | 通常可接受 |
| 多选为空 | `[]` | 通常可接受 |
| JSON 为空 | `null` | 不丢失，JSON 是例外 |

如果 VibeTable 的筛选、公式、统计要区分“空白”和“0/false”，必须增加存在性元数据，详见第 7 节。

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
| `number` | `0` | min、max、onlyInt、required | 普通小数；显示最多 2 位，不写入舍入 |
| `bool` | `false` | required | 复选框；默认不勾选 |
| `date` | `""` | min、max、required | 仅日期；默认空白 |
| `autodate` | `""`，随后自动赋值 | onCreate、onUpdate | 默认“创建时间”预设 |
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
| `presentable` | `false` | `false` | 高级 | 仅提示 PocketBase Dashboard 如何显示关系预览；VibeTable 应有自己的“关系显示字段” |
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

VibeTable 若实现了独立的空白存在性，就应将普通“必填”交给 VibeTable 校验，而不是盲目映射到数字、布尔和 GeoPoint 的 PocketBase `required`。

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
- 写入小数位数
- 舍入模式
- 输入步长
- 千分位
- 单位、前缀、后缀
- 货币格式
- 百分比格式
- 空白与 0 的区分

### 推荐的常规选项

| 选项 | 推荐默认 | 说明 |
|---|---:|---|
| 数字样式 | 普通数字 | 普通数字 / 整数 / 货币 / 百分比 / 自定义单位 |
| 显示小数位 | 最多 2 位 | 显示 `1.2` 而不是强制 `1.20`；可切换固定小数位 |
| 隐藏末尾 0 | 开启 | 仅在“最多 N 位”模式有效 |
| 千分位 | 开启 | 仅影响显示 |
| 默认值 | 关闭 | 开启后默认可为 0 |
| 最小值/最大值 | 不限制 | 建议放常规区的“更多限制”折叠项 |

### 推荐的高级选项

| 选项 | 推荐默认 | 说明 |
|---|---:|---|
| 写入时舍入 | 关闭 | 防止默认 2 位小数静默破坏高精度输入 |
| 写入小数位 | 未设置 | 仅在开启写入舍入时生效 |
| 舍入模式 | `halfUp` | 推荐提供 halfUp、halfEven、floor、ceil、truncate |
| 输入步长 | `any`；键盘增减 1 | 不作为存储约束 |
| PB `onlyInt` | 由逻辑类型派生 | 整数、缩放货币应开启 |
| PB `required` | 关闭 | 因其真实含义是“不能为 0” |
| 空白策略 | `preserve` | 由第 7 节的存在性方案实现 |

### 重要的 PocketBase 0.39.9 校验特性

PocketBase 在验证数字时，遇到 `0` 会先处理 `required`，随后直接结束验证。因此：

```text
required = false
min = 1
value = 0
```

PocketBase 仍会接受 `0`。VibeTable 若把 `min` 作为用户可见约束，必须在自己的写入层再次校验，不能只依赖 PocketBase。

### 货币建议：不要直接把金额作为二进制小数保存

建议增加 VibeTable 逻辑类型“货币”，底层仍使用 PocketBase `number`，但保存最小货币单位的整数：

```text
用户输入：123.45 元
底层保存：12345 分
PB onlyInt：true
VibeTable scale：2
```

这样可避免常见二进制浮点误差。不同货币的默认标度应可配置，而不是永远假定两位。

### 百分比建议

推荐统一保存比例值：

```text
用户输入/显示：12.50%
底层保存：0.125
```

默认显示 2 位小数；高级设置允许选择“保存比例 0–1”或“保存百分数 0–100”，但一个字段创建后不应静默切换存储口径。

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
    "roundOnWrite": false,
    "storageScale": null,
    "roundingMode": "halfUp",
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
| 默认值 | `false` | 常规 |
| 允许未填写/三态 | `false` | 高级 |
| `true` 标签 | 是 | 高级 |
| `false` 标签 | 否 | 高级 |

若启用三态，必须使用存在性元数据，否则 `null` 会被 PocketBase 转为 `false`。

### 推荐默认

```jsonc
{
  "type": "bool",
  "required": false,
  "_vibe": {
    "appearance": "checkbox",
    "defaultValue": false,
    "allowIndeterminate": false
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

## 5.6 `autodate`：自动日期

### 原生设置

| 设置 | 类型 | PB 结构体默认 | Schema 合法要求 | 推荐预设 |
|---|---|---:|---|---|
| `onCreate` | boolean | `false` | 与 `onUpdate` 至少一个为 true | 创建时间：`true` |
| `onUpdate` | boolean | `false` | 与 `onCreate` 至少一个为 true | 创建时间：`false` |

`false/false` 是 Go 零值，但不是一个可以保存的合法字段配置。PocketBase Dashboard 新建该字段时会将其初始化为“Create”。

### 推荐不要只暴露两个裸开关

常规区使用预设：

| 预设 | onCreate | onUpdate | 建议用途 |
|---|---:|---:|---|
| 创建时间 | true | false | 记录首次创建时间 |
| 最后修改时间 | true | true | 创建时写入，此后每次更新刷新 |
| 仅更新时间 | false | true | 高级场景 |

### 其他行为

- 普通记录赋值不会修改该字段；它由 PocketBase 管理。
- 无 `required`、无 `help`。
- 显示精度和时区建议复用 `date` 的 VibeTable 格式元数据。

### 推荐默认

```jsonc
{
  "type": "autodate",
  "onCreate": true,
  "onUpdate": false,
  "_vibe": {
    "preset": "createdAt",
    "timezone": "system",
    "displayPrecision": "minute",
    "readOnly": true
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

`thumbs=[]` 表示不配置额外尺寸；PocketBase Dashboard 说明默认 `100x100` 缩略图仍作为基础尺寸存在。

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
| 存储舍入 | 关闭 |
| 输入顺序 | 纬度、经度，界面明确标注；底层仍为 `{lon,lat}` |
| 地图拾取 | 开启 |

六位小数足以满足大多数桌面表格中的精细位置显示；不建议默认裁剪底层值。

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
    "roundOnWrite": false,
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

PocketBase 源码明确说明该类型通常只用于认证集合的内部 `password` 系统字段。PocketBase Dashboard 的普通“新建字段”菜单也跳过该类型。

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
| `number` | Go `float64`，无原生十进制精度 | 显示精度与写入精度分离 |
| 整数 | 仍经 `float64` 数据通道 | 建议限制到 JavaScript 安全整数范围 |
| 货币 | 无原生定点数 | 缩放整数 + currency scale |
| 百分比 | 无原生格式 | 比例存储 + 显示小数位 |
| `date` | 毫秒 | 默认显示到分钟；秒/毫秒高级可选 |
| `geoPoint` | 无原生小数位设置 | 默认显示 6 位，默认不裁剪存储 |

### 数字精度建议模型

```ts
interface NumberFormatOptions {
  style: "decimal" | "integer" | "currency" | "percent" | "unit";

  // 显示层
  displayScale: number;                 // 默认 2
  displayScaleMode: "fixed" | "max";  // 默认 max
  trimTrailingZeros: boolean;           // 默认 true
  useGrouping: boolean;                 // 默认 true

  // 写入层
  roundOnWrite: boolean;                // 默认 false
  storageScale: number | null;           // 默认 null
  roundingMode: "halfUp" | "halfEven" | "floor" | "ceil" | "truncate";

  // 语义层
  currencyCode?: string;
  percentStorage?: "ratio" | "whole";
  unit?: string;
}
```

不要用一个名为“精度”的整数同时控制显示和存储，否则用户无法判断更改该值会不会修改已有数据。

---

## 7. 空白值设计：本项目最需要先决定的架构问题

## 7.1 方案 A：接受 PocketBase 的零值语义

### 做法

- 数字空白就是 0；
- 复选框空白就是 false；
- 地理位置空白就是 `(0,0)`。

### 优点

- 最简单；
- 无额外字段；
- 原生筛选和索引最直接。

### 缺点

- 无法实现可靠的“为空”筛选；
- 平均值、计数、公式会把空白当成真实数据；
- 导入 Excel/CSV 后语义容易改变。

不建议用于以表格体验为核心的产品。

## 7.2 方案 B：单个记录级空值清单，推荐作为单人桌面版起点

在每个数据集合中增加一个 VibeTable 内部 JSON 字段，例如：

```json
{
  "__vt_nulls": ["fld_price", "fld_approved", "fld_location"]
}
```

数组中记录当前为空的 VibeTable 字段 ID。

### 优点

- 每个集合只增加一个内部列；
- 字段重命名不受影响，因为保存稳定 ID；
- 适合单人桌面、所有写入都经过 VibeTable 的场景。

### 缺点

- “为空”筛选和索引比普通列复杂；
- 外部直接写 PocketBase 时必须遵循同一协议；
- 大量字段频繁更新时会反复改写同一 JSON 值。

### 注意

这个内部字段应在 **VibeTable 视图层隐藏**。不要轻易设置 PocketBase `hidden=true`，否则通过 JSON API 的前端可能收不到它。

## 7.3 方案 C：每个需要空白语义的字段增加存在性列

例如：

```text
price               number
__vt_has_price      bool
approved            bool
__vt_has_approved   bool
location            geoPoint
__vt_has_location   bool
```

### 优点

- 原生筛选、索引和唯一约束最清晰；
- 读写逻辑简单；
- 可准确区分 0、false、(0,0) 与空白。

### 缺点

- 字段数量增加；
- Schema 和迁移更复杂。

### 推荐采用范围

至少对以下字段使用存在性列：

- 需要“为空”筛选或索引的数字；
- 允许三态的布尔；
- GeoPoint；
- 允许 0 且又要求“必填”的数字；
- 需要“忽略空白唯一”的数字。

## 7.4 推荐的混合方案

对于单人桌面 VibeTable：

1. 默认使用一个 `__vt_nulls` JSON 字段；
2. 当某字段启用“索引、唯一、频繁为空筛选”等能力时，升级为独立 `__vt_has_<id>`；
3. 所有创建、更新、导入、复制、撤销和 API 写入统一经过一个写入服务；
4. PocketBase 的数字/布尔/GeoPoint `required` 默认保持关闭，由 VibeTable 按存在性校验。

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
- 写入舍入
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
| 整数 | `number` | onlyInt=true、显示 0 位 |
| 小数 | `number` | 显示/写入精度 |
| 货币 | `number` | 缩放整数、币种、scale |
| 百分比 | `number` | 存储口径、显示 scale |
| 评分 | `number` | min/max、图标 |
| 复选框 | `bool` | 三态、标签 |
| 日期 | `date` | floating-date |
| 日期时间 | `date` | 时区、显示精度 |
| 创建时间 | `autodate` | onCreate=true |
| 修改时间 | `autodate` | onCreate=true、onUpdate=true |
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
      "nullPolicy": "preserve"
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
      "roundOnWrite": false,
      "storageScale": null,
      "roundingMode": "halfUp"
    }
  },

  "bool": {
    "_vibe": {
      "appearance": "checkbox",
      "defaultValue": false,
      "allowIndeterminate": false
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
    "onUpdate": false
  },

  "autodateModified": {
    "onCreate": true,
    "onUpdate": true
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
      "roundOnWrite": false,
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

---

## 12. Schema 校验与迁移检查清单

实现字段设置界面时，保存前至少做以下交叉校验：

### 公共

- 显示名可以是 Unicode；物理 `name` 必须符合 PocketBase 规则。
- `help` 不超过 300 字符。
- `hidden=true` 时不要同时依赖 `presentable`。
- 系统字段修改需要单独权限与确认。

### 文本

- `max >= min`。
- 正则必须由 PocketBase/Go 正则验证，不能只用浏览器正则检查。
- 自动生成正则生成出的内容仍需满足 min/max/pattern。

### 数字

- `max >= min`。
- `onlyInt=true` 时 min/max 也必须是整数。
- 自行补充对 0 的 min/max 校验。
- 写入舍入策略变化时，明确是否迁移已有值。

### 日期

- `max >= min`。
- 日期模式切换为日期时间时，不要无提示改变已有时区语义。

### 自动日期

- `onCreate`、`onUpdate` 至少一个为 true。

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

## 13. 建议的实现优先级

### 第一阶段：保证语义正确

1. 显示名与物理字段名分离；
2. 建立 VibeTable 字段元数据；
3. 实现创建时默认值注入；
4. 确定空白与 0/false 的处理方案；
5. 为数字增加显示精度；
6. 为唯一值建立索引管理器。

### 第二阶段：完善常用字段体验

1. 单选/多选稳定选项 ID；
2. 日期/日期时间与时区；
3. 文件大小、MIME 预设、缩略图；
4. 关系显示字段与迁移；
5. 货币、百分比、评分等逻辑类型。

### 第三阶段：高级能力

1. 写入舍入与批量精度迁移；
2. JSON Schema；
3. 三态布尔；
4. 高级唯一索引；
5. 开发者 Schema JSON 编辑器；
6. 系统字段与密码字段管理。

---

## 14. 来源

### PocketBase v0.39.9 源码

- [公共 Field 接口与名称校验](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field.go)
- [TextField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_text.go)
- [BoolField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_bool.go)
- [NumberField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_number.go)
- [DateField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_date.go)
- [AutodateField](https://github.com/pocketbase/pocketbase/blob/v0.39.9/core/field_autodate.go)
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

### PocketBase Dashboard v0.39.9 源码

- [公共字段设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/base/fieldSettings.js)
- [新字段初始公共属性](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/collections/addCollectionFieldButton.js)
- [Select 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/select/settings.js)
- [Relation 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/relation/settings.js)
- [File 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/file/settings.js)
- [Autodate 设置 UI](https://github.com/pocketbase/pocketbase/blob/v0.39.9/ui/src/fields/autodate/settings.js)

### 官方文档

- [Collections](https://pocketbase.io/docs/collections/)
- [Go collection operations / indexes](https://pocketbase.io/docs/go-collections/)

---

## 15. 当前需要确认的产品决策

本方案中最影响后续架构的一项不是颜色、精度或默认上限，而是：

> **VibeTable 是否必须严格区分“空白数字”和 0、“未填写复选框”和 false、“空位置”和 `(0,0)`？**

如果答案是“必须”，建议在开始实现字段设置界面前，先定下第 7 节的存在性数据结构；否则之后公式、筛选、唯一索引、CSV/Excel 导入和统计都需要返工。
