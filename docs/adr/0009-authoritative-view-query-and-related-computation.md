# ADR 0009：统一 ViewQuery 与关系计算权威

- 状态：Accepted
- 日期：2026-08-05

## 背景

VibeTable 已有 sidecar Query、Relation、Lookup 和 Formula 计算基础，但产品入口分散：Tabulator 表头筛选只表达简单等值条件，替代视图在当前加载行上重复筛选和排序，普通表格没有分组入口；Formula 编辑器暴露原始 CEL；Relation/Lookup 配置要求内部 ID。相同概念在不同调用者中具有不同语义。

VibeTable 是离线优先桌面产品，PocketBase 是唯一数据权威。查询、关联和计算不能因为前端视图或分页方式不同而改变结果。

## 决策

### 1. ViewQuery 是查询深模块的唯一 interface

建立版本化、严格、类型化的 `ViewDefinition`：

```text
ViewDefinition
├─ identity: viewId / tableId / kind / revision
├─ query
│  ├─ filter: FilterGroup
│  ├─ sorts: SortSpec[]
│  ├─ groups: GroupSpec[]
│  └─ search: SearchSpec | null
└─ presentation
   ├─ visibleFieldIds: fieldId[]
   ├─ summaries: SummarySpec[]
   └─ layout: kind-specific closed union
```

- Filter UI 最多三级、50 条条件；sidecar 对同一约束做权威校验。
- Groups 首版最多两级，contract 使用有序列表以便以后扩展。
- Summaries 最多三个数值字段，针对完整筛选结果计算。
- 隐藏字段只改变 presentation，不改变 Schema、依赖或查询资格。
- Tabulator、看板、日历和时间线是 adapter；不得各自实现筛选/排序语义。

### 2. Relation 是成对 Schema 身份

一个用户关系由 `relationPairId` 标识，并在两张表中拥有两个互指字段。创建、修改和删除都通过 `FieldChange` interface 原子计划与应用。

- 普通入口只支持同 workspace 的 direct Relation、自关联和单/多基数。
- 创建时自动建立可命名反向字段。
- 删除任一字段必须展示对端字段和依赖；存在 Formula/Lookup 字段依赖时阻止删除。
- 删除业务记录默认解除链接，可选 `restrict`；不开放级联删除业务记录。
- 每张表声明主显示字段；记录选择器使用稳定 record ID 传输、主显示字段展示。

### 3. Lookup 只负责读取，不负责聚合

Lookup 由 Relation path 和目标 fieldId 定义，不再包含用户可选 aggregate。

- 最多 8 个 Relation step，限制由 sidecar capability 声明并校验。
- 单 Relation 返回目标字段标量；多 Relation 返回保留元素类型与来源 record ID 的值集合。
- 多值单元格只呈现摘要，详情和来源按页读取；不要求一次物化全部 JSON 值到前端。
- Lookup 值可导航到来源记录，并具有 `#REF!`、计算中和诊断状态。

### 4. Formula 负责标量计算与关联聚合

保留现有 `physicalName` CEL 执行契约和 `fieldId` 依赖图：

- 普通编辑器显示 `displayName` token，插入字段后序列化为稳定 `physicalName`。
- CEL 和 `f_...` 仅在高级诊断模式展示。
- 编译器推断结果类型，用户只选择显示格式。
- Relation 集合必须通过 `SUM/COUNT/AVG/MIN/MAX/ANY` 等函数收敛为标量。
- 不支持无 Relation 的跨表扫描、任意列表输出和通用 map/filter。
- 当前开发阶段不保留旧 Lookup aggregate 产品兼容层。

### 5. 大基数使用查询计划，不使用业务条数上限

- Relation 与来源详情分页/虚拟化读取；多跳 Lookup 在填满当前页并探测到下一项后立即停止，
  `provenanceTotalKnown=false` 表示当前总数只是下界，最后一页才返回精确总数。
- 可下推的关联聚合由数据库完成；不能先加载全部关联记录再在 Go 内存聚合。
- 多跳执行限制为 8，并使用可取消的时间、内存和扫描成本预算。
- 当前记录和小 fan-out 同步重算；大 fan-out 进入持久、可恢复的后台任务。
- 计算失败或待计算时不允许旧值冒充当前值参与筛选、分组、排序或后续公式。

## 深模块与 seam

| Module | Interface | Adapter | 隐藏的 implementation |
|---|---|---|---|
| ViewQuery | validate/execute/save | PocketBase、内存测试、Web view | 类型解析、SQL、分页、组树、汇总、revision |
| RelationPair | plan/apply/inspect/delete | FieldChange RPC、测试 catalog | 双字段身份、基数、反向维护、依赖与删除语义 |
| RelatedValues | describe/page/aggregate | PocketBase、内存 relation graph | 多跳遍历、来源、提前停止分页、流式 reduce、字节资源预算；Formula Relation 聚合由 SQL 下推 |
| FormulaWorkbench | validate/preview/commit | Host RPC、测试 adapter | token 映射、CEL、类型、依赖、诊断、取消 |
| Recalculation | invalidate/status/cancel/resume | PocketBase jobs、内存 job store | fan-out、checkpoint、错误、重启恢复 |

测试只通过这些 interface 观察行为；不通过读取内部 metadata 表或 mock 内部调用顺序验证。

## 后果

### 正向

- 相同视图定义在所有布局得到相同结果。
- 复杂性集中在少量深模块，Web 不再理解 PocketBase 字段名和查询细节。
- Formula、Lookup 和 Relation 共享依赖、来源与大基数执行语义。
- 字段改名不会破坏公式，用户不接触内部 ID。

### 成本

- `PresetView`、查询 RPC、Schema V2 和生成契约需要同步升级。
- Lookup aggregate 的删除和 Relation pair 身份属于开发期破坏性调整。
- 大 fan-out 后台任务与 stale/error 状态需要贯穿查询和 UI。

## 被否决方案

- 在各视图 adapter 中分别实现筛选和排序：造成语义漂移。
- 用 displayName 作为公式持久引用：重命名与同名字段不稳定。
- 为每个跨表聚合创建隐藏 Rollup/Lookup：增加 Schema 噪声和中间字段管理。
- 只把 fan-out 常量从 1000 改成 10000：仍会在更大数据上失败，并保留全量内存展开问题。
