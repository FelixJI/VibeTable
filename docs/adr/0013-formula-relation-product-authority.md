# ADR 0013：公式与关联采用 Go 产品权威和统一计算计划

- 状态：已接受
- 日期：2026-09-04
- 差距分析基线：`main@57cbad609cced20ae5024ae46dc5b5c8d635b0bc`（需求附件冻结）
- 集成基线：`main@24ead0cf0ae49ad0e9bad211bda247d891c64164`（提交前 fresh main）

## 背景

ADR 0009 已冻结 Relation、Lookup 与 Formula 的产品边界，现有 Go sidecar 也已具备 CEL 编译、
关系对身份、reciprocal 写入、Lookup 遍历和可恢复重算基础。当前缺口不是另一套计算引擎，而是同一能力在
作者协议、RPC owner、依赖顺序、公开状态和真实产品证据上仍不完整。

Formula 编辑器仍会在 Web 中把展示名投影回 physical name；Python 仍注册和解释部分 Formula、Relation、
Lookup 方法；Formula 与 Lookup 各自维护计划信息；产品 E2E 只证明字段迁移和 cascade 影响预览，没有证明
普通用户可以完成 Relation → Lookup → Formula → 跨表重算 → 重开的闭环。

## 决策

### 1. 维持既有产品边界

- Relation 建立记录之间的稳定关系，并拥有 reciprocal 一致性。
- Lookup 读取显式 Relation path 上的值和来源，不承担聚合。
- Formula 产生标量结果，并负责沿显式 Relation 的聚合。
- 本轮不新增 Rollup、任意跨表扫描、列表结果、用户函数、脚本公式或 Decimal 语义。

关联聚合的公开函数闭集为 `SUM`、`AVERAGE`、`MIN`、`MAX`、`COUNT`、`COUNTA`，与现有 Go
`relationSum`、`relationAverage`、`relationMin`、`relationMax`、`relationCount`、
`relationCountValues` 实现对应。本决策以 `AVERAGE`/`COUNTA` 取代 ADR 0009 中的 `AVG`/`ANY`
名称，不新增另一套聚合语义。

`cascade` 继续保留为内部迁移能力，不作为本轮普通用户的删除策略。普通产品入口只公开 `setNull` 与
`restrict`。

### 2. Go sidecar 是唯一语义与事务 owner

目标调用链是：

```text
Vue / TypeScript → WPF typed bridge → Go Product RPC → PocketBase / SQLite
```

Formula、Relation 与 Lookup 的解析、类型、校验、事务、依赖和重算只由 Go 决定。Python 可以在迁移期保留
传输适配，但不得重新解释类型、补写返回形状、实现关系规则或与 Go 同时注册同一 Product RPC。

每组 RPC 按原子 owner 切换：Go producer 与 parity tests 就绪后，接通 Host allowlist，再删除 Python
注册。失败时直接返回稳定错误，不回退到第二个 runtime。

### 3. 作者文档只属于编辑协议

canonical Formula 继续以 CEL v1 和稳定 physical name 持久化。编辑器与 Go 之间使用作者文档：

```text
FormulaAuthorDocument
├─ displaySource
├─ tokens[]
│  ├─ range
│  ├─ kind: field | relation | relationTarget
│  ├─ fieldId
│  ├─ relationFieldId?
│  └─ targetFieldId?
└─ documentRevision
```

range 使用零基 UTF-16 line/character。Go 同时生成 display source、canonical source 与 source map；Web
只负责编辑状态、装饰、键盘操作、取消和迟到响应隔离，不重写 CEL parser。FormulaSpec 不保存 display
name，字段改名不改写 canonical source；删除引用产生稳定 `#REF!` 诊断。

### 4. 所有消费者共享逻辑 ComputationPlan

Schema apply 前必须构建包含 Lookup 与 Formula 的同一逻辑计划：

```text
ComputationPlan
├─ schemaRevision
├─ nodes: LookupNode | FormulaNode
├─ localEdges
├─ externalEdges
├─ executionOrder
├─ invalidationRules
├─ costEstimate
└─ planHash
```

该接口可以投影到现有依赖表，不要求新建持久化 authority。Schema 校验、preview、Mutation 同步计算、
query freshness、backfill、fan-out、字段删除检查和 snapshot 恢复后的重建必须消费同一编译结果。

`planHash` 只用于跨阶段绑定这份编译结果：由 Go 计划编译器从 canonical Schema revision 与依赖定义
产生，供提交、异步任务恢复和 freshness 校验消费；不匹配时丢弃计划并重新编译，仍不匹配则 fail closed。
它防止 preview 后 Schema 改变却提交或恢复旧执行顺序，不作为本地文件、缓存或源码的重复完整性校验。

执行顺序固定为普通字段 → 当前行 Lookup → 本地 Formula DAG → 版本化结果 → `data.changed` 与
`changedFieldIds` → 跨表 fan-out。依赖元数据和 Schema revision 必须原子提交。

### 5. freshness 和错误状态形成公开闭集

计算结果继续绑定 `definitionVersion`、`sourceDataRevision` 与 `dependencyWatermark`。任一不匹配时，旧值
不得进入查询、筛选、排序、分组、汇总、Dashboard 或后续 Formula。

公开状态闭集为：

```text
ready | updating | failed | cancelled | invalid | too_expensive
```

相同状态必须贯穿 query page、Formula/Lookup 单元格、字段设置、任务中心和 Realtime。稳定错误包含 code、
message key、UTF-16 range（适用时）、severity 和 details。

### 6. 大规模执行以成本和查询计划约束

- Lookup 以 relation path 分组并批量加载、去重 frontier；不按行和单元格逐项读取。
- direct relation 优先使用批量 SQL/IN；多跳保持 cursor、字节预算和取消。
- fan-out 先采集查询数、扫描行数与路径访问数，再用 `changedFieldIds` 裁剪并做反向查找。
- 只有资格证据证明反向发现仍不达标时，才考虑可重建的派生边投影。
- 不以固定业务记录数作为同步/异步边界，也不改变 PocketBase 的唯一数据权威。

### 7. 迁移、回滚与能力开放

现有 canonical Formula 和 Relation 数据不改写。新依赖元数据必须可由 Schema 重建；Relation 完整性
修复遵循 inspect → preview → backup receipt → Mutation Kernel apply，不直接 SQL 修补业务记录。

新能力先以内部 capability 存在，经 contracts、runtime、Host、Web、打包产品 E2E 与 N-1 资格闭合后再
默认开启。回滚关闭 capability，但不能依赖反向改写用户公式或制造双 authoritative write。

## 后果

### 正向

- 普通用户不需要输入 tableId、fieldId、recordId 或 physical name。
- preview、提交、查询和重算消费同一语义与依赖计划。
- 大规模资格可以按查询复杂度、内存、取消和恢复验证，而不是用记录数猜测。
- 能力矩阵只有在完整产品证据存在时才标记 Closed。

### 成本

- RPC owner 切换必须跨 Go、Host、Python inventory 和四语言 contract 协调。
- Formula author document、Relation picker 与公开计算状态需要前后端共同演进。
- 完成资格需要真实打包 WPF/WebView2、10k/100k、snapshot 和 N-1 证据。

## 被否决方案

- 重写 Formula 引擎或在 TypeScript 实现第二套 CEL：产生不可验证的双语义。
- 新增 Rollup：与 Lookup 和 Formula relation aggregate 重复。
- Go 失败后回退 Python：同一请求会拥有两个语义 owner。
- 一开始引入独立边表、图数据库或通用任务队列：当前没有资格证据证明既有机制不足。
- 为让门禁通过而降低性能、覆盖率或 E2E 要求：掩盖产品缺口，不能形成放行证据。
