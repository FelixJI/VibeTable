# Directus 计算字段一手来源核验

日期：2026-07-23  
核验对象：VibeTable 当前使用的 Directus 12.1.1，以及 Directus 当前官方文档、官方源码和 Directus Labs 扩展

## 结论摘要

用户提供的声明并不是 Directus 12.1.1 core 的官方计算字段协议。它混合了三件不同的事：

1. Directus core 的 Fields API 确实允许通过 `POST /fields/{collection}` 和
   `PATCH /fields/{collection}/{field}` 创建、修改字段及其 Studio 元数据。
2. Directus Labs 确实发布了一个名为 **Calculated Fields Bundle** 的公式界面扩展，
   其界面 ID 是 `calculated`，公式保存在 `meta.options.formula`。
3. 但这个扩展只在 Directus Studio 浏览器界面中计算并展示结果。官方明确说明结果
   **不会出现在 API 响应中**。它也不是 Directus core 内建字段类型。

逐项判断：

| 声明 | 结论 |
|---|---|
| `POST /fields/{collection}`、`PATCH /fields/{collection}/{field}` 可以管理字段 | **正确** |
| Fields API 可以保存 `meta.options` 一类界面配置 | **正确** |
| Directus core 原生识别 `meta.special: ["calculated"]` | **错误** |
| Directus core 原生识别 `schema.is_computed: true` | **错误** |
| 官方 Labs 公式扩展使用 `meta.options.formula` | **正确，但必须安装扩展，且是 Studio-only** |
| 用户给出的 payload 会在 Directus 12.1.1 创建自动计算字段 | **错误**；它只会创建普通 `string` 数据库列并保存一部分惰性元数据 |
| 公式结果会通过普通 Items API 返回 | **错误**；Labs 扩展官方说明明确排除 API 响应 |
| VibeTable 当前安装了该公式扩展 | **错误** |
| Labs 扩展声明兼容 Directus 12.1.1 | **错误**；当前包清单只声明 `^10.0.0 || ^11.0.0` |

因此，对 VibeTable 而言：

- 如果只需要 Directus Studio 表单中的即时预览，可以评估 Labs 界面扩展，但它目前
  没有声明兼容 Directus 12，且不能满足 VibeTable 网格、普通 Items API、导出、排序和筛选。
- 如果公式值必须通过 Directus Items API 返回，当前可靠方案仍是数据库 generated
  column，或者另行实现会持久化结果的服务端 hook/flow/extension。
- Directus 12.1.1 FieldsService 能反射已有 generated column，却不能通过标准 Fields API
  创建生成表达式；本地 SQLite 需要由受控服务端执行 DDL，而不是相信
  `schema.is_computed` 会生效。

## 1. 官方 Fields API 实际支持什么

当前官方 [Fields API 文档](https://directus.com/docs/api/fields) 列出了：

- `POST /fields/{collection}`：创建字段；
- `PATCH /fields/{collection}/{field}`：修改字段；
- 请求体包含 `type`、`field`、`schema` 和 `meta`；
- `meta.special` 是通用的 transformation flags；
- `meta.options` 是所选 interface 的配置，其结构取决于 interface。

这只能证明 Fields API 可以管理字段元数据，不能证明任意写入
`special/options` 的字符串会获得 core 执行语义。

Directus 12.1.1 的官方 controller 源码也显示：

- `schema` 的明确字段只有 `default_value`、`max_length` 和 `is_nullable`；
- `.unknown()` 会让额外的 schema 属性通过 Joi 语法校验；
- `meta` 接受任意对象。

来源：

- [Directus v12.1.1 `api/src/controllers/fields.ts`](https://github.com/directus/directus/blob/v12.1.1/api/src/controllers/fields.ts)
- 本项目安装包：
  [`controllers/fields.js`](../../scripts/local_directus/node_modules/@directus/api/dist/controllers/fields.js)

这意味着 `schema.is_computed` 很可能不会在入口处报错，但“没有报错”不等于“实现了
计算字段”。是否生效取决于后续 FieldsService，而后续服务没有处理这个属性。

## 2. Directus 12.1.1 FieldsService 没有计算字段建列逻辑

Directus v12.1.1 的 `FieldsService.createField()` 对非 alias 字段调用
`addColumnToTable()`。后者处理：

- 普通数据库类型；
- 长度、数值 precision/scale；
- nullable、default；
- primary key、unique、index；
- auto increment。

它没有读取：

- `schema.is_computed`；
- `schema.is_generated`；
- `schema.generation_expression`；
- `meta.special` 中的 `calculated`；
- `meta.options.formula`。

来源：

- [Directus v12.1.1 `FieldsService`](https://github.com/directus/directus/blob/v12.1.1/api/src/services/fields.ts#L898-L1040)
- [Directus 当前 `main` 的 `FieldsService`](https://github.com/directus/directus/blob/main/api/src/services/fields.ts#L906-L1048)
- 本项目安装包：
  [`services/fields.js`](../../scripts/local_directus/node_modules/@directus/api/dist/services/fields.js)

截至本次核验，`main` 与 v12.1.1 的这个服务文件内容 SHA 相同；当前开发分支也没有
新增 `is_computed` 或 `generation_expression` 建列逻辑。

因此用户给出的请求：

```json
{
  "field": "full_title",
  "type": "string",
  "meta": {
    "special": ["calculated"],
    "options": {
      "formula": "concat(title, ' - ', author.name)"
    }
  },
  "schema": {
    "is_computed": true
  }
}
```

在 core 12.1.1 中的实际效果是：

1. `type: "string"` 触发创建普通字符串数据库列；
2. `schema.is_computed` 通过宽松入口校验，但建列逻辑忽略它；
3. `meta.special` 和 `meta.options` 可以被保存到 `directus_fields`；
4. 没有 core 代码读取这些值来执行公式；
5. `meta.interface` 也没有设为公式扩展 ID，因此即便安装 Labs 扩展，该 payload
   也不符合扩展的接口注册方式。

## 3. `schema.is_computed` 不是 Directus 字段 schema

Directus v12.1.1 的官方 `Column` 类型有：

```ts
is_generated: boolean;
generation_expression?: string | null;
```

没有 `is_computed`。

来源：

- [Directus v12.1.1 `packages/schema/src/types/column.ts`](https://github.com/directus/directus/blob/v12.1.1/packages/schema/src/types/column.ts)
- [Directus v12.1.1 字段类型定义](https://github.com/directus/directus/blob/v12.1.1/packages/types/src/fields.ts)

截至本次核验，Directus `main` 上这两个类型文件与 v12.1.1 内容 SHA 相同，
仍然只有 `is_generated/generation_expression`，没有 `is_computed`：

- [当前 `Column` 类型](https://github.com/directus/directus/blob/main/packages/schema/src/types/column.ts)
- [当前 `FieldMeta` 类型](https://github.com/directus/directus/blob/main/packages/types/src/fields.ts)

这里的 `is_generated/generation_expression` 是 schema inspector 对数据库已有生成列的
反射信息。它不代表 FieldsService 有对应的 generated-column DDL 创建实现。

## 4. `meta.special: ["calculated"]` 不是 core special

Directus 12.1.1 core 的 alias 类型集合为：

```text
alias, o2m, m2m, m2a, o2a, files, translations
```

core 的自动生成 special 集合为：

```text
uuid, date-created, date-updated,
role-created, role-updated,
user-created, user-updated
```

其中都没有 `calculated`。本项目安装的全部 `@directus/*` core 包中也没有
`calculated` 或 `is_computed` 的实现匹配。

来源：

- 本项目安装包：
  [`@directus/api/dist/constants.js`](../../scripts/local_directus/node_modules/@directus/api/dist/constants.js)
- 本项目安装包：
  [`@directus/constants/dist/index.js`](../../scripts/local_directus/node_modules/@directus/constants/dist/index.js)

`meta.special` 在数据库层可以保存任意字符串，不代表所有字符串都是 core 支持的
special。`calculated` 被保存后，在未安装扩展的 12.1.1 中是惰性元数据。

## 5. 官方公式能力来自 Directus Labs 扩展，不是 core

Directus 官方在 2024 年的 Changelog 中把 Calculated Field 明确放在 Directus Labs：

- [2024 年 10 月 Changelog](https://directus.com/tv/the-changelog/3-october-2024)
  说明 Calculated Field Interface 使用 Formula.js 和受限 JavaScript 运算符，并明确
  表示结果只在 interface 中可见，不在 API 响应中。
- [2024 年 11 月 Changelog](https://directus.com/tv/the-changelog/4-november-2024)
  把 Directus Labs 称为团队的实验性 GitHub 组织，并把 calculated fields bundle
  列为 Labs extension。
- [Directus Labs extensions 仓库](https://github.com/directus-labs/extensions)
  明确说明它与 Directus core 仓库不同，Labs 扩展发布后未必持续开发。

Calculated Fields Bundle 自己的 README 再次明确：

> Currently only an interface is provided in this bundle. Values are only
> visible in the interface and not in API responses.

来源：

- [Calculated Fields Bundle README](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/readme.md)

扩展源码注册的是：

```ts
defineInterface({
  id: 'calculated',
  types: ['alias'],
  localTypes: ['presentation'],
  // ...
});
```

公式配置字段的名字确实是 `formula`，并从当前字段的 `meta.options.formula` 读取。
但接口组件只是把结果绑定到只读 `<v-input :model-value="result">`，没有把结果写入
数据库。扩展包清单也只有 app `interface` entry，没有 hook、endpoint 或其他 API
entry，因此没有服务端参与计算。

来源：

- [扩展 interface 注册](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/src/interface/index.ts)
- [扩展 Vue 组件](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/src/interface/interface.vue)

所以，对这个 Labs 扩展而言，真正关键的元数据是
`meta.interface: "calculated"`，而不是 `meta.special: ["calculated"]`。
同时它只接受 alias/presentation 类型；用户示例的 `type: "string"` 与其注册约束不符。
官方扩展示例的字段引用语法是 `{{field}}`，函数示例使用 Formula.js 的
`CONCATENATE(...)` 等函数；用户给出的裸 `concat(title, ..., author.name)` 也不是该
扩展 README 展示的公式语法。

## 6. 版本兼容性：Labs 扩展没有声明支持 Directus 12

Calculated Fields Bundle 当前包版本为 1.0.3，其扩展清单声明：

```json
"host": "^10.0.0 || ^11.0.0"
```

来源：

- [Calculated Fields Bundle `package.json`](https://github.com/directus-labs/extensions/blob/main/packages/calculated-fields-bundle/package.json)

VibeTable 的 lockfile 则实际安装：

- `directus` 12.1.1；
- `@directus/api` 37.0.1；
- `@directus/schema` 14.0.0。

来源：

- [`scripts/local_directus/package-lock.json`](../../scripts/local_directus/package-lock.json)

因此即便只接受 Studio-only 语义，也不能把这个 Labs 扩展未经验证地当作
Directus 12.1.1 的受支持依赖。

## 7. VibeTable 当前没有安装 calculated 扩展

VibeTable 版本控制的 Directus extension manifest 只列出：

- `vibetable-bulk-mutation`；
- `vibetable-workspace-index`；
- `vibetable-plugin-bridge`；
- `vibetable-lookup-query`。

没有 `@directus-labs/calculated-fields-bundle` 或其他 calculated/computed interface。

来源：

- [`directus/extensions/manifest.json`](../../directus/extensions/manifest.json)
- [`scripts/local_directus/package.json`](../../scripts/local_directus/package.json)

项目安装的 Directus core 包中出现的 `@hapi/formula` 是 Joi 校验器的传递依赖，
与 Directus Calculated Fields Bundle 无关。

## 8. 为什么 VibeTable 的 SQLite 集成测试自行建 generated column

项目现有 Directus 12 集成测试已经写明：Directus 的 FieldsService 不创建
`generation_expression` 列。因此测试流程是：

1. 用 Directus API 创建普通 collection；
2. 直接执行 SQLite
   `GENERATED ALWAYS AS (...) VIRTUAL` DDL；
3. 刷新 Directus schema cache；
4. 通过 Fields API 配置字段的 readonly/display 元数据；
5. 验证 Directus 返回 `schema.is_generated == true`；
6. 验证 VibeTable 可读取和导出结果。

来源：

- [`tests/backend/integration/test_plugin_directus_12.py`](../../tests/backend/integration/test_plugin_directus_12.py)

这个测试验证的是数据库 generated column，与 Labs 的 Studio-only calculated interface
不是同一种能力。

## 9. 对产品实现的直接影响

### 目标只是 Studio 表单预览

可以考虑安装或移植 Labs Calculated Fields Bundle，并通过 Fields API 管理其
`meta.interface/options`。但必须接受：

- 只在 Studio 表单里可见；
- 普通 Items API 不返回结果；
- VibeTable 网格、导出、筛选、排序无法依赖该值；
- 当前包未声明兼容 Directus 12，需要自行验证或维护 fork。

### 目标是 API、网格、导出、筛选、排序都可用

不能使用 Labs interface 代替真实字段。可选方案：

1. **数据库 generated column**  
   值由数据库即时维护，通过普通 Items API 返回。SQLite 需要受控后端或 Directus
   endpoint 执行安全 DDL；标准 FieldsService 不会创建表达式。
2. **服务端 hook/flow 持久化普通字段**  
   创建/更新时计算并写回，API 可见，但需要处理回填、依赖、批量更新、循环依赖和
   一致性，不是真正的 generated column。
3. **自定义 API extension 查询时计算**  
   可以避免数据库方言 DDL，但需要自定义读取协议，未必能自然接入 Directus 普通
   Items API 的筛选和排序。

对 VibeTable 当前的本地 SQLite 产品形态，数据库 generated column 与既有读取、
readonly、导出链路最一致。

## 最终判定

用户给出的文字很可能是把 Directus Labs Calculated Fields Bundle 的介绍和通用
Fields API 拼成了一个并不存在的 core API 协议。最容易误判的点是：

- `POST/PATCH /fields` 路由存在，所以 payload 往往不会整体报错；
- `meta.options.formula` 也确实能保存；
- 但真正执行公式的是额外安装的 Studio interface；
- `schema.is_computed` 和 `special=calculated` 没有 Directus 12.1.1 core 语义；
- Labs interface 的结果官方明确不会进入 API。

因此不能据此撤销“API 可见公式值需要服务端/数据库实现”的结论，但应该把原先
“Directus 不支持公式”的表述收窄为：

> Directus 生态提供 Studio-only 公式界面扩展，Directus core 也能反射数据库生成列；
> 但 Directus 12.1.1 标准 Fields API 不能按用户给出的协议创建 API 可见的计算字段。
