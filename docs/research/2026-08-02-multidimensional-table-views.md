# 多维表格视图竞品研究（截至 2026-08-02）

范围：仅核对官方一手帮助中心/产品文档；“支持”指该产品在该日期的正式文档已列出的能力，不推断套餐、移动端或企业权限差异。字段前提是创建或进行关键交互所需的最小数据条件。

## 共同模式

四个产品都将视图定义为同一数据集的不同呈现：数据编辑会回写到其他视图，而筛选、排序、分组、字段显示等通常是视图配置。飞书对此区分最明确：数据在全部视图同步，视图内配置各自生效。[飞书官方文档](https://www.feishu.cn/hc/zh-CN/articles/472603853615-%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E7%9A%84%E6%95%B0%E6%8D%AE%E8%A1%A8%E5%92%8C%E8%A7%86%E5%9B%BE)；Airtable 亦将 view 定义为组织同一张表数据的方式。[Airtable 官方文档](https://support.airtable.com/docs/view-basics)

## 产品对比

| 产品 | 正式视图类型 | 字段前提 | 核心交互 |
| --- | --- | --- | --- |
| Airtable | Grid（默认）、Form、Calendar、Gallery、Kanban、Timeline、List，另有 Gantt。 [官方目录](https://support.airtable.com/docs/view-types) | Form 直接由已有字段生成；Calendar 依赖真实 Date；Kanban 的堆栈字段为单选、用户或“不可多关联”的关联记录；Timeline 要开始日期，结束日期可选；Gantt 用开始/结束日期，依赖关系可由关联记录提供；Gallery 有附件字段时自动选作封面；List 用关联记录形成层级。 [日期](https://support.airtable.com/docs/date-and-time-field)、[看板](https://support.airtable.com/docs/getting-started-with-airtable-kanban-views)、[时间线](https://support.airtable.com/timeline-view-overview)、[Gantt](https://support.airtable.com/how-to-add-and-configure-the-gantt-view)、[画册](https://support.airtable.com/docs/getting-started-with-airtable-gallery-views)、[表单](https://support.airtable.com/docs/getting-started-with-airtable-form-views)、[列表](https://support.airtable.com/v1/docs/list-view-overview) | Grid 可多级分组（看板只单字段）；Form 提交创建记录且可条件显隐字段；看板拖卡、配置卡片字段、筛选/排序/着色；时间线横向缩放、按任意字段泳道分组、拖动改日期/时长与跨泳道改分组值，计算日期不可拖改；Gantt 显示时长、重叠和依赖箭头；List 跨关联表编辑层级记录。 [表单](https://support.airtable.com/docs/getting-started-with-airtable-form-views)、[看板](https://support.airtable.com/docs/getting-started-with-airtable-kanban-views)、[时间线操作](https://support.airtable.com/docs/working-with-records-in-the-timeline-view)、[Gantt](https://support.airtable.com/how-to-add-and-configure-the-gantt-view) |
| 飞书多维表格 | Table（默认）、Kanban、Calendar、Gallery、Gantt、Form。 [官方概览](https://www.feishu.cn/hc/zh-CN/articles/360049067931-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E8%A7%86%E5%9B%BE) | 看板分组字段可为人员、单/多选、复选框、流程、评分、单/双向关联；Calendar 以日期/日期公式/创建时间作开始或结束，不能以最后更新时间创建；Gantt 以日期字段的开始/结束时间呈现；Gallery 以附件为主体。 [视图说明](https://www.feishu.cn/hc/zh-CN/articles/360049067931-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E8%A7%86%E5%9B%BE)、[日历详解](https://www.feishu.cn/hc/zh-CN/articles/980591272592-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E7%9A%84%E6%97%A5%E5%8E%86%E8%A7%86%E5%9B%BE) | Table 支持字段、排序、分组、筛选；Kanban 拖卡写回分组值并可用附件封面；Calendar 日/周/月、新建、拖动整体或边界改日期；Gantt 支持周/月/季/年、分组/筛选、拖时间条、里程碑；Form 是受控录入入口。 [视图说明](https://www.feishu.cn/hc/zh-CN/articles/360049067931-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E8%A7%86%E5%9B%BE) |
| Notion Database | Table、Board、Timeline、Calendar、List、Gallery、Chart；官方分类还列 Dashboard、Feed、Map、Forms 等数据库视图/呈现。 [类型概览](https://www.notion.com/help/views-filters-and-sorts)、[官方分类](https://www.notion.com/help/category/database-views/all) | Calendar 使用 Date 属性；Timeline 至少需一个含日期范围的 Date 属性（也可配置独立开始/结束属性）；Board 按任意属性分组；Gallery 可用 Files & media 作图片，否则用页面内容。 [类型概览](https://www.notion.com/help/views-filters-and-sorts)、[Timeline](https://www.notion.com/help/timelines) | Board 拖卡改变分组（常为状态）；Timeline 可按小时至年缩放、显示/排序属性、拖动调整项目，并切换日期属性；Chart 为条/线/环图。 [Board/Timeline 使用指南](https://www.notion.com/help/guides/when-to-use-each-type-of-database-view)、[Timeline](https://www.notion.com/help/timelines) |
| SeaTable | Table（默认）、Kanban、Calendar、Gallery、Timeline、Tree、Big Data；地图/组织图等以插件提供。 [官方视图说明](https://seatable.com/help/what-is-a-view/) | Kanban 至少需单选或协作者列；Calendar 需日期列；Gallery 需图片列；Timeline 需两列日期；Tree 需至少两张关联表；Big Data 需启用相应存储。 [官方视图说明](https://seatable.com/help/what-is-a-view/)、[看板详解](https://seatable.com/help/the-kanban-view/) | Table 负责录入、字段和筛选/排序/分组/隐藏列；Kanban 选择分组列与卡片标题、拖卡、可加记录并编辑详情；视图可锁定、私有、置顶、共享。 [视图与插件](https://seatable.com/help/views/)、[看板详解](https://seatable.com/help/the-kanban-view/)、[视图管理](https://seatable.com/help/base-editor/views/) |
| Smartsheet | Grid、Table、Card、Board、Calendar、Gantt、Timeline。 [官方选型页](https://help.smartsheet.com/articles/765715-grid-gantt-calendar-and-card-views) | Calendar 至少一列 Date；Timeline 与 Gantt 各需两列非公式 Date（Gantt 还须项目设置）；Card 至少一个单选下拉或联系人列；Board 使用单选下拉、单选联系人或符号列。 [官方选型页](https://help.smartsheet.com/articles/765715-grid-gantt-calendar-and-card-views)、[Board](https://help.smartsheet.com/learning-track/level-2-workflows/board-view) | Calendar 拖动改日期、拖边界改区间、双击编辑整行；Card/Board 将行变卡和分组泳道，拖卡跨泳道；Gantt 处理依赖与关键路径；Timeline 按时间和分组管理日期工作。 [Calendar](https://help.smartsheet.com/learning-track/level-2-workflows/calendar-view)、[Card](https://help.smartsheet.com/articles/2302238-using-card-view-to-visualize-your-project)、[选型页](https://help.smartsheet.com/articles/765715-grid-gantt-calendar-and-card-views) |

## 对 VibeTable 的代码基线

截至本次检查，`PresetView.kind` 已支持 `table | calendar | timeline`，并持久化 `dateField`、`endDateField`、`titleField`（`desktop/web-grid/src/contracts/index.ts`）。工作区已渲染月历和时间线：前者展示日期记录但不能编辑，后者是按开始时间排序的纵向列表；创建入口亦仅列出这三类（`desktop/web-grid/src/views/WorkspaceView.vue`，`desktop/web-grid/src/components/grid/RecordCalendarView.vue`，`RecordTimelineView.vue`）。因此下列建议不是从零增加“日历/时间线”，而是补足其可用性。

## VibeTable 建议矩阵（离线、单人桌面）

| 优先级 | 建议 | 理由与边界 |
| --- | --- | --- |
| 近期优先 | **日历视图可编辑**：可选开始/结束日期；点空白日期新建、拖动整体与边界修改可编辑日期；日/周/月切换，保留现有月视图。 | 与现有字段模型、单人本地写入和 Calendar 组件直接契合；是 Airtable、飞书、Notion、Smartsheet 的共同高频交互。不可编辑的公式/系统日期应明确禁用拖动，避免错误承诺。 |
| 近期优先 | **时间线升级为横向区间布局**：开始必填、结束可选；按任意字段分组为泳道；缩放（日/周/月）；拖动移动/改时长；缺日期记录可在侧栏发现。 | 现有 `dateField/endDateField/titleField` 已有持久化位置。先做到无依赖的项目排期，覆盖离线个人计划，避免直接把现有纵向列表叫 Gantt。 |
| 近期优先 | **Kanban（单字段分组）**：仅允许单选、布尔、单值关联等可安全写回的字段；拖卡更新字段，支持筛选/排序、卡片字段和附件缩略图。 | 四家产品均将其作为任务/流程主视图；单字段是小而稳的本地实现边界。多选/多关联的列语义要先定义，不能暗中选择某一值。 |
| 后续再做 | **Gallery**：附件/图片列为封面，卡片打开记录详情。 | 复用现有 file/attachment 能力，需求清楚，但不如日历、时间线、看板普适。 |
| 后续再做 | **真正的 Gantt**：依赖、关键路径、任务层级、自动排程。 | Smartsheet/Airtable 将其与一般 Timeline 区分；需要日期冲突、依赖循环、撤销和排程语义，超出本轮离线单人 MVP。 |
| 后续再做 | **Form 录入视图**：字段显示/必填/默认值/提交后行为。 | 对个人快速录入有价值，但已有数据网格和字段设置；离线分享、外部收集不是本产品当前优势。 |
| 不适合 | **实时协作、个人/公共视图、视图权限、视图锁、评论与分享链接。** | 这些主要服务多人同步与治理；会把本地单人产品拖入账号、ACL、冲突处理与托管分发。保留“本地默认视图”即可。 |
| 不适合 | **Map、Org chart、Tree、Big Data、插件化可视化。** | 依赖地理、跨表层级、海量分层存储或第三方运行时；当前收益低且显著扩大产品与安全边界。需要时应先以受控导出或单独插件决策处理。 |
| 不适合 | **内置高级 BI/图表作为“表格视图”。** | 项目已有 Dashboard 方向；把 Chart 复制进数据表视图会造成两套过滤、持久化和权限模型。应让 Dashboard 消费同一查询/筛选契约，而非复制实现。 |

## 落地顺序与验收口径

本次 PR 的边界是先补齐看板/画册类型、横向区间时间线及各预设独立的筛选/排序投影；
拖卡写回、日历拖动、时间线泳道/缩放/拖动改期仍按下列顺序继续演进，不在本次完成范围内。

1. 先完善日历和时间线的字段校验、空值提示、只读日期限制、保存/重开视图一致性；对每种拖动生成普通字段更新，走既有审计和撤销路径。
2. 再实现单字段 Kanban，并用字段能力表控制“是否可作为泳道”与“是否可拖写”。
3. 以同一 `PresetView` 的筛选、排序、列显示、默认视图语义贯穿三种布局；布局特定配置只增加显式、可版本化的字段，避免以 UI 默认值推断行为。
4. 在离线产品中，性能目标应优先是大表的虚拟化、日期/区间索引和无网络依赖；不要把竞品的协作特性当作完成视图的前置条件。
