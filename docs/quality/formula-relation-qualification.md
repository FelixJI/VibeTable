# Formula / Relation / Lookup 资格规范

> 状态：Baseline。这里定义通过条件，不声明当前实现已经通过。
>
> 架构边界见 [ADR 0013](../adr/0013-formula-relation-product-authority.md)，实施顺序见
> [成熟度收敛计划](../plans/2026-09-04-formula-relation-maturity-convergence.md)。

## 1. 证据规则

按 [2026-09-07 开发阶段范围调整](../plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md)，
0.5.0/N-1 兼容不作为当前开发资格。此范围变化不改变下列新版产品、性能、恢复或 CI 证据要求。

能力只有同时具备 producer、Host/allowlist、Web consumer、capability 和真实打包产品 E2E 证据时才可在
[能力闭环矩阵](capability-matrix.md) 标记 Closed。unit、integration、组件测试、生成索引、旧的 main run
或未绑定 source SHA 的报告都不能单独形成产品放行结论。

每项性能结果必须记录 fixture、profile、查询/扫描计数、机器或 runner 类别、source SHA、命令和报告位置。
初始目标可以在基线实测后调整，但变更必须说明测量差异与产品影响；不得为使 CI 变绿而放宽。

当前 renderer-public policy 基线只有以下两项 capability 和方法闭集；E2E selector 只是测试路由，不作为
capability：

- `schema.formula`：`formula.draft.validate`、`formula.preview`、`formula.validate`；
- `relation.lookup`：`lookup.draft.preview`、`lookup.list`、`lookup.query`、`lookup.valuePage`、`relation.applyDelta`、
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
| Lookup | 1/8/9 跳、来源分页、取消、批量 frontier、查询数门禁；[条件模式](conditional-lookup.md) 的类型化谓词、完整值与依赖重算 |
| Calculation state | `ready/updating/failed/cancelled/invalid/too_expensive` 六态闭集在 query page、Formula/Lookup 单元格、字段设置、任务中心和 Realtime 使用同一 freshness 与错误语义 |
| Jobs | backfill/fan-out、取消/恢复、进程中断、幂等重放和六态投影 |
| Web | Formula Workbench、Relation Picker、键盘、迟到响应、冲突重载 |
| Host bridge | allowlist、单一 RPC owner、取消、超时、session epoch |
| Product E2E | 创建链、改名、来源修改、重启、snapshot、来源删除和 stale 查询拒绝 |
| Format admission | 当前格式正常读取与恢复；不支持的旧/新格式零写入拒绝。开发阶段不承诺 N-1 迁移 |

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
| Formula 作者与计算链 | `sidecar/internal/formula/*`、S05/S37 与下述 S38 | 常用/日期/集合作者路径已有证据；本轮三表组合通过，不再以“作者协议和产品链未闭合”笼统标记 |
| Relation 原子性 | relation service、Mutation Kernel 与 reciprocal tests | 有 producer 基础；pair update/integrity/picker 未闭合 |
| Lookup | calculator、1/8/9 跳回归、S26/S38 与 `TestCalculationChainQualification` | 条件汇总、完整来源分页及固定 100 行重复条件的查询次数已有证据；不外推其他任意谓词的复杂度 |
| fan-out/backfill | 持久 jobs、取消/恢复、10k integration，三表变更与进行中取消组合回归 | 本轮受影响链传播及旧值拒绝已有证据；六态所有 consumer 与真实进程中断恢复仍按原资格单独判断 |
| `05-formula-lifecycle` | 常用/日期公式编辑、筛选排序、导出与重开；新增链见 S38 | 保留原失败回滚覆盖；不再把当前 S05 限定为早期空表转换样本 |
| `06-relation-fanout` | 双端配置更新、冻结摘要、重开及冲突零写入；见 [pair 更新资格](relation-pair-update.md) | 本地定向通过；不证明标签消费、完整记录选择、Lookup 或跨表重算 |

### 本轮 Formula/条件 Lookup 组合资格（#396）

本节仅收口 [#390](https://github.com/FelixJI/VibeTable/issues/390) 声明的个人离线计算范围。
[#396](https://github.com/FelixJI/VibeTable/issues/396) 本地最终候选的真实同包 S38 已通过；
完整适用 CI、fresh independent review、正常 squash 及 main/CD 集成尚待完成，不能据此将整份规范标为 Closed。
既有历史 main 样本及其 manifest gap 保持原记录。

| 已覆盖范围 | 可执行证据入口 | 对应验收 |
|---|---|---|
| 可见 UI 创建条件 SUM Lookup、标量 Formula 与跨表集合 Formula；修改来源值和已存公式后，两层结果逐值对齐独立 oracle | [S38 场景](../../tests/e2e/calculation_chain_journey.mjs)及[独立 fixture/oracle](../../tests/e2e/calculation_chain_journey.test.mjs)；常用/日期与集合语法分别沿用 S05/S37 | GAC1–3；AC1/AC7 的代表链部分 |
| 第三表按计算值筛选排序，完整结果/计数与网格一致；0/1/199 匹配，真实来源详情从 100 条翻页并核对全部 199 条；CSV/XLSX 同快照逐值及顺序一致、公式样文本不执行、计算列只读；关闭重开保留工作区身份与视图 | 同一 S38；[独立导出读取器](../../tests/e2e/data_io_workbook.py) | GAC4；AC2/AC7 |
| 来源新增/修改/删除、匹配进入/退出、无关字段不排全量任务、schema/表达式及受控时钟变化后刷新；旧值在 fan-out 完成前拒绝 | [三表变更回归](../../sidecar/tests/integration/calculation_chain_changes_test.go)的 `TestCalculationChainChangesPropagateAndRefresh`；字段删除/循环、导入失败/取消等沿用各自既有契约，不归入 S38 的 UI 覆盖 | GAC3/GAC5；AC1/AC4 的组合部分 |
| 同身份和修订、不同数据的工作区 A→B→A 无串值；正在执行的查询取消；同包离线运行与正常退出清理 | 同文件 `TestCalculationChainWorkspaceIsolationAndInFlightCancellation`；S38 的无外网、bridge 与 lifecycle 门禁 | GAC5；AC6 |
| 固定 10k/50k 来源、1k 主表、100 行视窗；完整结果 oracle、冷热分位数、查询/编译计数与进程内存满足预先冻结预算；重复 Lookup 条件只执行一次匹配查询 | [规模 fixture](../../sidecar/tests/integration/calculation_chain_qualification_test.go)的 `TestCalculationChainQualification`；[批量摘要回归](../../sidecar/tests/integration/query_digest_batch_test.go)；[测量口径及前后计数](../e2e-performance.md#2026-09-29-三表计算链资格) | GAC6；AC3–5 |

规模测量是显式独立进程入口；默认测试中的未选择规模运行不算性能通过。application-cold 不等于磁盘冷启动，
DBX 查询回调数不等于 SQLite 扫描行数；非可下推集合谓词仍受[集合执行边界](formula-collections.md#执行边界)限制。
原始来源采用 validated PocketBase Save 准备，这组规模结果不证明 50k 导入吞吐。

本轮不解除本规范更广的 Relation 四种基数/pair 全生命周期、六态在全部 consumer 的一致投影、真实 fan-out
进程中断与 snapshot 恢复组合等要求，也不承诺全量飞书函数/语法兼容。对应范围须按各自证据判断；
#390 GAC7 / #396 AC8 的最终门禁状态由实际 PR 与合并后证据回填。

## 7. Closed 完成定义

- 用户无需输入 tableId、fieldId、recordId 或 physical name。
- Formula preview 与提交结果一致，改名保持有效，删除引用产生 `#REF!`。
- Formula、Lookup 和 Formula→Formula 顺序正确；来源修改精确触发跨表重算。
- reciprocal relation 始终事务一致；四种基数、自关联和 pair 全生命周期有证据。
- Lookup 支持来源导航、分页与批量执行，没有 rows×fields N+1。
- stale 结果不参与任何查询或下游计算；fan-out 可取消、恢复和重启续跑。
- `ready/updating/failed/cancelled/invalid/too_expensive` 六态在 query page、Formula/Lookup 单元格、
  字段设置、任务中心和 Realtime 均有产品证据，不接受未知 fallback 状态。
- 当前格式 snapshot 恢复与不支持格式零写入拒绝成立；10k/100k fixture 无无界内存。
- 三条产品场景、能力矩阵、E2E 索引、用户文档和截图与同一 fresh main 证据一致。

任一条缺少证据时维持 Open/Partial，不用相邻测试或实现存在性推断 Closed。

日期/日历与易变时钟函数的约定及固定时钟验证入口见 [日期公式与时钟](formula-date-clock.md)。
