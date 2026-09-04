# Formula / Relation / Lookup 资格规范

> 状态：Baseline。这里定义通过条件，不声明当前实现已经通过。
>
> 架构边界见 [ADR 0013](../adr/0013-formula-relation-product-authority.md)，实施顺序见
> [成熟度收敛计划](../plans/2026-09-04-formula-relation-maturity-convergence.md)。

## 1. 证据规则

能力只有同时具备 producer、Host/allowlist、Web consumer、capability 和真实打包产品 E2E 证据时才可在
[能力闭环矩阵](capability-matrix.md) 标记 Closed。unit、integration、组件测试、生成索引、旧的 main run
或未绑定 source SHA 的报告都不能单独形成产品放行结论。

每项性能结果必须记录 fixture、profile、查询/扫描计数、机器或 runner 类别、source SHA、命令和报告位置。
初始目标可以在基线实测后调整，但变更必须说明测量差异与产品影响；不得为使 CI 变绿而放宽。

当前 renderer-public policy 基线只有以下两项 capability 和方法闭集；E2E selector 只是测试路由，不作为
capability：

- `schema.formula`：`formula.draft.validate`、`formula.preview`、`formula.validate`；
- `relation.lookup`：`lookup.list`、`lookup.query`、`lookup.valuePage`、`relation.applyDelta`、
  `relation.createTarget`、`relation.previewDelta`、`relation.searchTargets`、`relation.updateSingle`。

后续新增或迁移方法必须同时更新权威 `contracts/v2/product-rpc-capability-policy.json`、生成物、inventory、
Host allowlist 与资格证据，不能只增加 selector 或 UI 路由。

## 2. 统一业务 fixture

```text
产品
├─ 名称
└─ 单价

订单明细
├─ 产品 Relation(one)
├─ 数量
├─ 单价 Lookup
└─ 金额 Formula = 数量 × 单价

订单
├─ 明细 Relation(many)
├─ 小计 Formula = SUM(明细.金额)
├─ 税额 Formula
└─ 合计 Formula
```

fixture 必须由公开产品路径或同一权威 producer 建立，不通过直接 SQL 预造应由产品能力产生的状态。规模
profile 使用确定性数据生成器；测试只断言可观察契约，不读取内部 metadata 表证明自身通过。

## 3. 功能资格矩阵

| 层级 | 必须证明 |
|---|---|
| Contract | closed schema 正反 fixture、额外字段拒绝、UTF-16 range、四语言 strict decode |
| Formula | `SUM/AVERAGE/MIN/MAX/COUNT/COUNTA` 闭集、token 往返、同名/改名/删除、null/空串/零/布尔/date、类型/循环/成本/超时、preview=commit |
| Relation | 四种基数、自关联、pair 对称、delta、重复/孤儿、setNull/restrict、revision 冲突 |
| Computation | Formula→Formula、Lookup→Formula、跨表依赖、循环、freshness 与原子依赖提交 |
| Go integration | 真实 PocketBase transaction、reciprocal 双写、audit/outbox、故障回滚 |
| Lookup | 1/8/9 跳、来源分页、取消、批量 frontier、查询数门禁 |
| Calculation state | `ready/updating/failed/cancelled/invalid/too_expensive` 六态闭集在 query page、Formula/Lookup 单元格、字段设置、任务中心和 Realtime 使用同一 freshness 与错误语义 |
| Jobs | backfill/fan-out、取消/恢复、进程中断、幂等重放和六态投影 |
| Web | Formula Workbench、Relation Picker、键盘、迟到响应、冲突重载 |
| Host bridge | allowlist、单一 RPC owner、取消、超时、session epoch |
| Product E2E | 创建链、改名、来源修改、重启、snapshot、来源删除和 stale 查询拒绝 |
| Compatibility | N-1 reader 对可理解数据只读/迁移；无法理解的新结构零写入拒绝 |

Formula differential test 必须在同一 Schema/data revision 上证明 preview 与 Mutation Kernel 提交结果
一致。Relation model test 随机执行 add/remove/replace/delete/retry/stale，每一步都证明
`A links B ⇔ B links A`，并核对双方 revision、audit 与 outbox。

## 4. 初始性能目标

| 场景 | 初始目标 |
|---|---|
| Formula warm validate | p95 ≤ 100 ms |
| Formula cold validate（约 200 字段） | p95 ≤ 300 ms |
| 当前行不超过 10 个 Formula 的普通编辑 | 结果 p95 ≤ 300 ms 可见 |
| Relation Picker（100,000 目标记录） | 搜索首屏 p95 ≤ 300 ms |
| 100 行页面、2 个 direct Lookup | warm p95 ≤ 500 ms |
| Lookup 查询复杂度 | 不随 `pageRows × lookupFields` 线性增加数据库请求 |
| 10k fan-out | 内存有界、可取消/恢复、无固定条数拒绝 |
| 进程中断 | 已提交事务不丢，未提交批次无半提交 |
| stale computed value | 进入筛选、排序、分组、汇总或后续 Formula 的次数为 0 |

大型 fan-out 不以“几秒完成”单独放行。它还必须不阻塞普通写入、不丢任务、不重复 audit、进度可观察、
可取消和恢复、内存有界，并且不把旧值当作当前值。

## 5. 产品场景

最终至少新增并通过以下真实打包场景；manifest 集成时按现有数字前缀规则分配 ID：

### `formula-authoring-lifecycle`

用户只用展示名/token 创建 Formula，实时 validate/preview 后保存；改名后公式继续有效，删除引用后显示
`#REF!`；同名字段必须显式选择，普通 UI 不暴露 physical name。

### `relation-lookup-computation-chain`

从 UI 创建双向 Relation、选择或轻量新建目标、建立 Lookup 和订单金额/合计 Formula；修改产品价格后，
明细和订单在 freshness 匹配后精确更新，筛选期间从未消费旧值。

### `computation-recovery`

在 fan-out 运行中精确中断 sidecar，重启并恢复任务；已提交批次保持、未提交批次无半提交，最终结果、
audit 与 outbox 不重复。snapshot 恢复后重建依赖并得到相同结果。

每个场景必须通过 `tests/e2e/product_e2e_runner.py` 启动当前 source 构建的 WPF host、附着真实 WebView2，
并产生和 source SHA 绑定的 required 报告。场景实现前不得把名称加入当前 manifest 或改写历史 main 证据。

## 6. 当前证据与缺口

| 能力 | 当前支撑证据 | 当前结论 |
|---|---|---|
| Formula 内核 | `sidecar/internal/formula/*` 及单元/集成测试 | 有 producer 基础；作者协议和产品链未闭合 |
| Relation 原子性 | relation service、Mutation Kernel 与 reciprocal tests | 有 producer 基础；pair update/integrity/picker 未闭合 |
| Lookup | calculator、来源分页和 1/8/9 跳测试 | 有遍历基础；页面级查询复杂度未资格化 |
| fan-out/backfill | 持久 jobs、取消/恢复、10k integration | 有恢复基础；公开状态、成本与产品 E2E 未闭合 |
| `05-formula-lifecycle` | 空表转换和非空迁移失败回滚 | 不证明 authoring 或计算链 |
| `06-relation-fanout` | cascade 方向和影响预览 | 不证明记录选择、Lookup 或跨表重算 |

## 7. Closed 完成定义

- 用户无需输入 tableId、fieldId、recordId 或 physical name。
- Formula preview 与提交结果一致，改名保持有效，删除引用产生 `#REF!`。
- Formula、Lookup 和 Formula→Formula 顺序正确；来源修改精确触发跨表重算。
- reciprocal relation 始终事务一致；四种基数、自关联和 pair 全生命周期有证据。
- Lookup 支持来源导航、分页与批量执行，没有 rows×fields N+1。
- stale 结果不参与任何查询或下游计算；fan-out 可取消、恢复和重启续跑。
- `ready/updating/failed/cancelled/invalid/too_expensive` 六态在 query page、Formula/Lookup 单元格、
  字段设置、任务中心和 Realtime 均有产品证据，不接受未知 fallback 状态。
- snapshot 恢复与 N-1 资格成立；10k/100k fixture 无无界内存。
- 三条产品场景、能力矩阵、E2E 索引、用户文档和截图与同一 fresh main 证据一致。

任一条缺少证据时维持 Open/Partial，不用相邻测试或实现存在性推断 Closed。
