# VibeTable 筛选、分组、公式与 Relation/Lookup 完整实施计划

> 状态：Implementation in progress
>
> 日期：2026-08-05
>
> 架构决策：[ADR 0009](../adr/0009-authoritative-view-query-and-related-computation.md)
>
> 竞品基线：[研究记录](../research/2026-08-05-filter-group-formula-relation-benchmark.md)

## 1. 完成定义

本计划完成时，普通用户无需输入 rowId、fieldId 或 `physicalName`，即可：

1. 用三级条件树和类型化操作符筛选完整数据集；表头快捷筛选与高级面板编辑同一状态。
2. 在普通表格创建最多两级分组，折叠、排序、显示空值组和全量数值汇总；分组不写回字段。
3. 按视图搜索、隐藏和恢复字段，隐藏不影响依赖与查询。
4. 可视化创建双向 Relation，在单元格搜索、分页选择、轻量新建并打开关联记录。
5. 沿最多 8 跳 Relation 可视化创建单值或多值 Lookup，并导航到来源记录。
6. 用字段 token 和函数提示编写公式，自动推断类型、实时预览，并直接聚合关联记录后继续计算。
7. 在万级以上关联数据上依靠分页和数据库聚合工作；长任务有计算中、失败、取消和重启恢复状态。

## 2. 非目标

- 无 Relation 的任意跨表 `SUMIF`；
- 公式列表输出、通用 map/filter、任意跨表整列编程；
- 普通表格拖记录跨组写回；
- 多值字段分组；
- M2A、用户可配置 junction 或跨 workspace Relation；
- 级联删除关联业务记录；
- 多人权限、云端协作或共享视图。

## 3. 冻结产品规则

### 3.1 筛选

- `FilterNode = Condition | Group`，Group 拥有 `and/or` 与 children。
- UI 与 sidecar 同时限制：最多三级、50 个 condition。
- 操作符来自字段 capability；空白与零值遵循产品 presence 语义。
- 表头条件是同一 AST 的快捷编辑；复杂条件存在时表头显示高级条件状态。

### 3.2 分组与汇总

- 最多两级；支持字段、方向、空值组、日期粒度、数值 bucket。
- 仅单值字段可分组：标量、单选、单 Relation、单值 Lookup、标量 Formula。
- 不支持拖动写回。
- 每组记录数始终存在；每个视图最多三个 `SUM/AVG/MIN/MAX` 汇总。

### 3.3 隐藏

- `visibleFieldIds` 是逐视图 presentation。
- 隐藏字段仍可筛选、分组、排序、Lookup 和 Formula。
- UI 提供搜索、逐项显示、批量显示与显示全部。

### 3.4 Relation

- `relationPairId`、`reciprocalFieldId` 和双方基数由 sidecar 分配/规范化。
- 创建时配置目标表、当前字段名、反向字段名、单/多和展示字段。
- 目标记录选择器传输 record ID、展示主显示字段和可选次要字段；支持分页。
- 轻量新建只在其余 required 可由默认值满足时直接提交，否则打开完整编辑。
- 完整编辑支持 scalar、v1 类型化 select/multiSelect 与可视化 Relation；Relation 按展示标签远程搜索，不受首屏 50 条候选限制。
- required Attachment 在现有上传协议不能与记录插入原子提交，能力层明确不支持该配置；不得以先建空记录再补附件伪装原子成功。
- 删除关系字段成对进行；字段依赖存在时阻止。删除记录只解除链接或 `restrict`。

### 3.5 Lookup

- 配置只包含 Relation path、target field 和显示设置；删除 aggregate/resultType 用户输入。
- 路径最长 8，由 sidecar capability 下发。
- 多值保留元素类型、来源 table/record/field 身份；单元格摘要、详情分页。

### 3.6 Formula

- source 仍以稳定 `physicalName` 执行；编辑器展示 displayName token。
- 打开时将已知 `physicalName` 解析为 token；保存时再序列化，未知引用显示 `#REF!`。
- 结果类型自动推断；格式与类型分离。
- 关联聚合只沿显式 Relation path，输出标量。
- 失败状态不沿用旧值；循环、引用缺失、类型、资源超限分别诊断。

## 4. Work packages 与 TDD 纵向切片

### WP0：正式契约与旧行为红灯

**Seam：** strict contract decoders、ViewQuery、FieldChange、Formula validate/preview。

- 为 `ViewDefinitionV2`、Relation pair、无 aggregate Lookup、Formula inferred type、recalculation state 增加正反 fixture。
- 生成并核对 Go/Python/TypeScript/C# RPC catalog。
- 红灯证明：groups 无法保存、替代视图语义分叉、raw ID 配置可提交、公式 placeholder 无效、1000 fan-out 被拒绝。

**完成门槛：** 失败来自缺失行为，而不是 fixture 或测试环境。

### WP1：ViewQuery 深模块

**Seam：** `ValidateViewDefinition`、`ExecuteViewQuery`、`SaveViewDefinition`。

- 引入 closed union Filter AST、Group/Summary/Presentation contract 与稳定错误码。
- 将 Query compiler 的 filters/sorts/search/group/metrics 收进一个执行计划。
- 组树分页必须保持稳定主键排序；汇总基于完整筛选结果。
- `PresetView` 升级为版本化 view definition；现有视图一次性转换，不保留开放 JSON 双轨。

**测试：** nested AND/OR、深度/数量拒绝、日期/数字 bucket、空值、稳定分页、全量汇总、非法多值分组。

### WP2：Web 查询 adapter 与控制面板

**Seam：** 用户通过公开视图栏修改配置，Workspace 只发送规范化 ViewQuery。

- `ViewQueryStore` 成为单一前端状态源；Tabulator header filter 是 adapter。
- 删除 `projectPresetRows` 本地筛选/排序实现，所有布局消费 sidecar 页。
- 增加筛选、分组、隐藏面板；操作符和字段资格由 capability 决定。
- 分组渲染只读；折叠状态按视图保存，不产生 mutation。

**测试：** 表头/高级筛选同步、两级分组顺序、隐藏字段仍可查询、替代视图请求等价、刷新/重开一致。

### WP3：Relation pair 与主显示字段

**Seam：** `field.change.plan/apply`、Relation record picker RPC。

- Schema V2 RelationSpec 增加 pair/reciprocal identity；表定义增加主显示 fieldId。
- Planner 原子规划双方字段，revision/operationId 覆盖两张表；自关联保持两个不同 fieldId。
- 删除计划列出双方和全部字段/视图依赖；依赖阻止，业务记录不级联。
- UI 用表/字段 catalog 选择器替代 raw ID；记录选择器支持搜索、分页、单/多和轻量新建。

**测试：** 一对一/多、自关联、反向一致性、失败原子性、重放幂等、删除依赖、setNull/restrict、万级选择分页。

### WP4：Lookup 读取模块

**Seam：** `field.change` Lookup 配置、`related.values.page`、query projection。

- Schema V2 移除 Lookup aggregate/resultType 输入，结果类型由路径推导。
- sidecar capability 声明最大深度 8；校验与 UI 共用。
- 多值 Lookup 使用结构化元素与 provenance；摘要和详情分页。多跳页在取得下一项证据后提前停止，
  以 `provenanceTotalKnown` 区分精确总数和下界；标量 reduce 流式执行，只有列表/去重集合受字节预算保护，不设置业务记录数上限。
- 筛选支持 containsAny/containsAll/isEmpty/isNotEmpty；多值排序/分组拒绝。

**测试：** 1/8/9 跳、路径缺失、单/多结果类型、来源导航、分页、取消、敏感字段拒绝。

### WP5：Formula Workbench 与关联聚合

**Seam：** `formula.validate/preview` 与 `field.change` 保存。

- 接入现有 FormulaPreviewCoordinator。
- 字段/函数 catalog、token tokenizer/serializer、自动补全、参数说明和诊断位置。
- 编译器输出推断 resultType；FieldDraft 不再接受用户 resultType。
- RelatedValues 以受控 CEL 值/函数进入 Formula；聚合尽量编译为 sidecar 查询计划。
- 默认 UI 不出现 CEL 或 `f_...`，高级诊断可查看。

**测试：** 字段重命名、同名字段 token、未知 physicalName、取消/stale preview、空集合/null、Relation SUM/COUNT 后继续计算、循环与资源超限。

### WP6：大基数与重算状态

**Seam：** mutation 后的公开 query value/status 与 recalculation status/cancel/resume。

- 删除 `maxRelatedRecords=1000` 业务限制；遍历改为 cursor/page 与 cost budget。
- count/sum/avg/min/max 对可识别 path 下推 SQL；无法下推时使用有界流式扫描。
- 新增持久 recalculation job、checkpoint、schema/data revision 和启动恢复。
- stale value 不参与权威查询；query 返回 updating/error diagnostics。

**测试：** >10k direct relation、8 跳低 fan-out、fan-out 爆炸成本拒绝、取消、崩溃恢复、旧值不参与筛选/分组。

### WP7：E2E 与收口

- 产品 E2E：创建双向 Relation → 选择/轻量新建记录 → Lookup → Relation 聚合 Formula → 筛选 → 两级分组/汇总 → 隐藏/重开。
- 大数据集 E2E/性能 fixture 不进入日常 UI 测试；用 sidecar 集成测试证明 >10k。
- 删除旧 raw-ID 输入、Lookup aggregate UI、错误 placeholder 和本地投影分叉。
- 更新 `CONTEXT.md` 领域语言、用户文档、i18n 和截图。

## 5. 稳定错误与状态

至少提供：

- `view.filter.depth_limit`、`view.filter.condition_limit`
- `view.group.unsupported_field`、`view.group.limit`
- `relation.pair.conflict`、`relation.delete.dependency_blocked`
- `lookup.path.depth_limit`、`lookup.path.invalid`、`lookup.value.source_missing`
- `formula.syntax`、`formula.type`、`formula.dependency`、`formula.cycle`、`formula.resource_limit`
- `calculation.pending`、`calculation.failed`、`calculation.cancelled`

错误必须有稳定 code/path/details；消息可本地化。日志不得包含敏感字段值。

## 6. 验证矩阵

| 层 | 必须验证 |
|---|---|
| Contracts | JSON Schema 正反 fixture、生成 catalog 无漂移、四语言 strict decode |
| Go | Query/Relation/Lookup/Formula/Recalculation interface、真实 PocketBase 集成、gofmt/vet/test |
| Python | closed RPC adapter、错误映射、paste/import/query 无 authority 分叉 |
| Web | store/component tests、typecheck、build、用户不输入内部 ID |
| .NET | request allowlist、host bridge、cancel/timeout/session lifecycle |
| Product E2E | 端到端闭环、重启恢复、截图 |
| GitHub | required、全部矩阵、CodeQL、release build/smoke |

每个测试必须在旧实现上因行为缺失而失败，再用最小纵向实现变绿；不按语言一次性铺完测试。

## 7. 提交与 PR 结构

同一 Draft PR 内按可独立审查意图提交：

1. `docs(query): 冻结视图查询与关系计算方案`
2. `feat(query): 统一视图查询与分组契约`
3. `feat(view): 增加筛选分组与隐藏入口`
4. `feat(relation): 建立双向关系与可视化选择`
5. `feat(lookup): 增加类型化关系路径引用`
6. `feat(formula): 增加字段令牌与关联聚合`
7. `perf(calculation): 支持大基数分页与可恢复重算`
8. `test(e2e): 覆盖多维表格计算闭环`

PR 保持 Draft，直到本地定向门禁、GUI 截图和 GitHub required 全绿；用户未要求 merge，因此不自动合并。

## 8. 完成审计

- [x] 所有视图使用同一 ViewQuery，没有当前页本地筛选/排序分叉。
- [x] 筛选三级/50 条、分组两级、汇总三字段在 UI 和 sidecar 同时约束。
- [x] 隐藏逐视图保存，隐藏字段仍可参与查询与计算。
- [x] Relation 成对创建/删除，记录选择和轻量新建无 raw ID。
- [x] 删除关系依赖时阻止；删除业务记录不级联。
- [x] Lookup 最多 8 跳、单/多值类型化、有来源与分页，无 Rollup aggregate。
- [x] Formula token/推断/预览/关联聚合可用，用户默认不见 CEL/physicalName。
- [x] 公式只输出标量，不支持无 Relation 跨表扫描。
- [x] >10k 关联不因固定数量上限失败，聚合不全量物化。
- [x] pending/error 不使用旧值，后台重算可取消并在重启后恢复。
- [x] 完整创建保留 v1 number/boolean 枚举值，嵌套 Relation 迟到搜索响应不会跨编辑器污染。
- [ ] 契约、Go/Python/Web/.NET/E2E、截图和 GitHub 全量门禁均有证据。
