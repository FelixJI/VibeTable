# 筛选、分组、公式与关联能力竞品研究（截至 2026-08-05）

## 范围与方法

本研究只使用 Airtable、飞书多维表格和 Notion 的官方帮助中心或开发者文档，目标不是逐项复制云端协作产品，而是提炼离线桌面多维表格必须具备的共同语义。

## 共同基线

### 视图查询

- 筛选、排序、分组和字段显示属于视图配置，不改变底层记录。
- 筛选器是类型化条件树，而不是字符串表达式。Airtable 支持条件组以及 AND/OR 组合；Notion 同样提供高级条件组；飞书把筛选、分组和排序作为表格视图的共同能力。
- 分组必须对完整筛选结果计算，支持折叠、空值组、方向和汇总，而不是只处理当前分页。

来源：[Airtable 筛选](https://support.airtable.com/v1/docs/filtering-records-using-conditions)、[Airtable 分组](https://support.airtable.com/grouping-records-in-airtable)、[飞书分组和筛选](https://www.feishu.cn/hc/zh-CN/articles/360049067904-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E5%88%86%E7%BB%84%E5%92%8C%E7%AD%9B%E9%80%89)、[Notion 视图、筛选与排序](https://www.notion.com/help/views-filters-and-sorts)。

### 公式

- 公式按记录计算，字段引用必须可发现、可重命名并有类型诊断。
- 成熟产品提供字段/函数浏览、参数说明和错误反馈，不要求用户了解物理存储列。
- 跨表计算必须有确定的关系或记录集合。Airtable 通过 Relation + Lookup/Rollup 形成依赖；Notion 公式可消费 Relation 列表；飞书提供跨表引用与计算能力。

来源：[Airtable 公式](https://support.airtable.com/docs/formula-field-overview)、[飞书公式字段](https://www.feishu.cn/hc/zh-CN/articles/360049067853-%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E5%85%AC%E5%BC%8F%E5%AD%97%E6%AE%B5%E6%A6%82%E8%BF%B0)、[Notion 公式语法](https://www.notion.com/en-gb/help/formula-syntax)。

### Relation、Lookup 与聚合

- Relation 先建立记录身份之间的关系；Lookup 再沿关系读取目标字段。两者不能在 UI 中退化为手填 rowId 或字段 ID。
- 多记录 Lookup 需要保留值类型与来源，不能只拼接为文本。
- 聚合是对关联集合的计算。VibeTable 选择把聚合收敛到 Formula，以便聚合结果继续参与表达式，而不要求创建并隐藏中间 Rollup 字段。

来源：[Airtable 关联记录](https://support.airtable.com/docs/linking-records-in-airtable)、[Airtable Lookup](https://support.airtable.com/docs/lookup-field-overview)、[Airtable Rollup](https://support.airtable.com/docs/rollup-field-overview)、[飞书查找引用](https://www.feishu.cn/hc/zh-CN/articles/398396737655-%E7%94%A8%E6%9F%A5%E6%89%BE%E5%BC%95%E7%94%A8%E6%95%B0%E6%8D%AE)、[Notion Relation 与 Rollup](https://www.notion.com/en-gb/help/relations-and-rollups)。

## VibeTable 现状

| 能力 | 当前实现 | 成熟度 | 主要缺口 |
|---|---|---|---|
| 筛选 | sidecar 已有类型化操作符、嵌套组和稳定排序；Web 表头只生成 `eq`/AND；替代视图另做当前页本地过滤 | Basic | 可视化条件树、统一 ViewQuery、类型化操作符、持久化 |
| 分组 | sidecar 聚合查询支持 GroupBy；Web 仅监听 `dataGrouped`，普通表格无创建入口且视图不保存 groups | Missing–Basic | 普通表格入口、两级组树、全量计数/汇总、折叠状态 |
| 公式 | cel-go 编译、资源限制、依赖、回填和物化较完整；编辑器只是原始 textarea，preview coordinator 未接线 | Basic–Usable | displayName token、类型推断、实时预览、跨 Relation 聚合 |
| Relation | 执行层支持 direct/junction/M2A 与 fan-out；字段设置仍要求 ID，缺成对关系产品语义 | Basic–Usable | 双向字段、表/字段/记录选择器、主显示字段、删除预检 |
| Lookup | 执行层支持多跳与聚合；Schema V2/UI 只暴露 raw IDs 且能力漂移 | Basic | 可视化路径、类型化多值、来源导航、移除 Rollup 式 aggregate |
| 隐藏 | 视图有 `visibleFields` 但缺完整管理入口 | Basic | 搜索、批量显示、所有视图一致持久化 |

## 适合离线产品的取舍

1. PocketBase 继续是唯一权威，所有视图查询由 sidecar 对完整数据集执行。
2. UI 最多三级筛选、50 条条件；普通表格首版两级分组，不支持拖记录跨组写回。
3. Relation 只开放同 workspace 的直接双表关系、自关联和单/多基数；自动创建反向字段。
4. Lookup 路径由 sidecar 统一限制为最多 8 跳；不把 M2A/junction 暴露给普通用户。
5. Formula 只输出标量，可沿 Relation 聚合；不开放任意跨表扫描、通用 map/filter 或整列编程。
6. 不保留固定 1000 条关联记录的业务上限。Relation 和 Lookup 分页，Formula 聚合下推数据库，资源由时间、内存、扫描成本和取消控制。

## 不直接复制的能力

- 不建设多人实时协作、视图权限和分享链接。
- 不采用 Notion 的 page-first Relation 模型。
- 不开放没有 Relation 的任意跨表 `SUMIF`，避免隐式全表依赖。
- 不让多值字段参与普通表格分组，避免一条记录同时属于多个组。
