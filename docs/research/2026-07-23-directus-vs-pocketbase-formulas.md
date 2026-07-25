# Directus 12、PocketBase 与公式字段实现比较

日期：2026-07-23  
目的：判断 VibeTable 是否应采用“前端计算后通过 API 持久化”，以及 Directus 在没有原生 API 公式字段时相对 PocketBase 的实际价值。

## 结论

1. **前端计算并把结果作为普通字段写入 Directus API，可以让结果参与 API 返回、筛选、排序和导出，但不能把客户端提交值当成可信计算结果。**
2. 推荐的职责划分是：
   - 前端用同一公式引擎提供即时预览、语法提示和类型检查；
   - Directus 12 服务端 Hook 在 `items.create` / `items.update` 的阻塞阶段重新计算并覆盖客户端值；
   - 计算结果存入普通物理字段，由 Items API 正常查询；
   - 公式修改和关系依赖变化通过服务端重算任务处理。
3. **PocketBase 同样没有“可写 Base Collection 上的原生公式字段”。**它的独特能力是 View Collection：用 SQL `SELECT` 建立单独的只读集合，可表达聚合和任意查询；普通 Records API 可以对视图字段筛选、排序。但是它不是原表中的可编辑字段，而且没有 create/update/delete 和 realtime 事件。
4. PocketBase 的 response enrichment 可以临时把计算值加入 API 响应，但该值是在查询后补充，不能自然参与数据库筛选、排序和分页。要在可写 Base Collection 中保存公式结果，PocketBase 也需要服务端 Hook。
5. 因此，是否使用 Directus 不应由“原生公式字段”决定。对 VibeTable 而言，Directus 的主要价值是多数据库/既有数据库适配、REST + GraphQL、字段级权限和校验、成熟的 Data Studio、Flows、版本与审计、前后端扩展体系。PocketBase 的优势则是单文件、SQLite、部署简单、内建迁移和一等 SQL View Collection。

## 1. 前端计算后写 API：何时成立

假设有：

```text
amount = quantity * unit_price
```

前端可以在用户编辑 `quantity` 或 `unit_price` 时立即计算 `amount`，然后在同一个 API 请求里提交三个字段。由于 `amount` 是普通数据库列，Directus Items API 后续可以正常返回、筛选、排序和导出它。

这比由客户端在每次读取时临时计算更实用，但有以下边界。

### 1.1 客户端值不可作为权威值

如果当前用户对 `amount` 有写权限，那么用户也可以跳过 VibeTable，直接调用 Items API 写入任意值。前端代码、隐藏输入框和 Studio 的 `readonly` 都不是安全边界。

Directus 的官方权限模型支持分别配置：

- item permissions；
- field permissions；
- field validation；
- presets。

来源：[Directus Access Control](https://directus.com/docs/guides/auth/access-control)、[Directus Permissions API](https://directus.com/docs/api/permissions)

但是，通用 validation 规则不等于任意 Formula.js 表达式的服务端重算器。若禁止普通用户写 `amount`，前端又不能直接持久化它；若允许写，则服务端不能信任它。因此要么使用可信后端代理，要么更直接地让 Hook 覆盖客户端提交值。

### 1.2 多写入入口会产生不一致

只在 VibeTable 前端计算，会漏掉：

- Directus Data Studio；
- 外部 REST/GraphQL 客户端；
- 导入、脚本、Flow；
- 批量更新；
- 以后新增的移动端或其他客户端。

只要某个入口更新依赖字段却没有同时更新结果，物化值就会过期。

### 1.3 部分更新、并发和关系依赖

- PATCH 往往只提交变更字段；计算必须基于“数据库当前记录 + PATCH delta”的合并结果。
- 如果客户端先 GET、计算、再 PATCH，期间另一个请求修改依赖字段，客户端可能写回旧结果。
- `author.name`、子项合计等关系公式，在关联记录变化时不会触发当前行前端重新提交。
- 修改公式定义后，历史数据不会自动更新，需要批量回填和可观察的重算状态。

这些不是 Directus 特有问题；PocketBase 的可写物化公式也面临同样问题。

### 1.4 可接受的前端直写范围

只有下列场景可以把纯前端持久化当作短期 MVP：

- 单用户或明确只有 VibeTable 一个写入入口；
- 公式值只是展示缓存，不参与金额、权限、配额等业务判断；
- 产品接受短暂或长期不一致；
- 提供全表“重新计算/修复”命令；
- API 明确标记该结果为派生缓存而非权威数据。

这不适合作为通用表格产品的最终一致性模型。

## 2. 推荐的无 SQLite DDL 方案

VibeTable 可以完全不直接建立 SQLite generated column：

```text
前端编辑
  ├─ 本地公式引擎：预览、报错、类型提示
  └─ POST/PATCH 原始字段
          ↓
Directus 阻塞 Filter Hook
  ├─ 读取公式元数据
  ├─ 合并当前记录与请求 delta
  ├─ 重新计算并覆盖结果字段
  └─ 与原写入处于同一事务
          ↓
普通物理字段
  └─ Items API / filter / sort / export
```

Directus 官方 Hook 文档说明，Filter Hook 在事件发生前执行，可以修改或取消 payload，并可访问当前 database transaction、schema 和 accountability；官方也提醒阻塞 Hook 的性能影响以及重复触发同一事件造成递归的问题。

来源：[Directus Custom API Hooks](https://directus.com/docs/guides/extensions/api-extensions/hooks)

推荐实现约束：

- 客户端提交的结果字段一律忽略或由 Hook 覆盖；
- 普通 API 角色不给计算结果字段 update 权限；若 Directus 在权限检查阶段早于 Hook 拒绝额外字段，客户端只提交源字段；
- Hook 使用服务端保存的公式定义，不接受每次请求临时传入公式；
- 同一 AST/函数清单生成前端和服务端 evaluator，避免语义漂移；
- 第一版优先同一行公式；
- 关系公式需要依赖图、反向失效和重算队列；
- 批量更新必须逐记录读取有效输入并分别计算；
- 公式变更触发 backfill，并记录失败行；
- 计算字段应在 UI 和权限上不可手工修改。

这样既避免 VibeTable 直接操作 SQLite DDL，也保留 Directus 标准 Items API 的查询能力。代价是结果是物化缓存，需要自行维护重算一致性。

## 3. PocketBase 是否原生支持公式

以下核验基于当前 PocketBase 官方文档（文档页面显示 v0.39.x）。

### 3.1 Base Collection：没有公式字段

PocketBase 官方字段清单包括 Bool、Number、Text、Email、URL、Editor、Date、Autodate、Select、File、Relation、JSON、GeoPoint，没有 Formula/Computed/Generated 字段。

来源：[PocketBase Collections](https://pocketbase.io/docs/collections/)、[PocketBase JavaScript Collection Operations](https://pocketbase.io/docs/js-collections/)

因此，PocketBase 并没有比 Directus 多一个“可写表内原生公式字段”。

### 3.2 View Collection：原生支持 SQL 计算视图

PocketBase 有一等 View Collection：

- 由普通 SQL `SELECT` 定义；
- 可进行聚合或任意自定义查询；
- 是独立、只读的 collection；
- 没有 create/update/delete；
- 因为没有这些写操作，也不产生 realtime 事件。

官方示例直接用 `LEFT JOIN` 和 `count()` 生成 `totalComments`。

来源：[PocketBase View Collection](https://pocketbase.io/docs/collections/#view-collection)

PocketBase Records API 对 collection 字段提供分页、筛选、排序和字段选择，所以 View Collection 暴露出的计算列也可以作为该只读 collection 的查询字段。

来源：[PocketBase Records API](https://pocketbase.io/docs/api-records/)

这是 PocketBase 在公式/聚合方面相对 Directus Fields API 最明确的产品优势。但它有几个重要区别：

- 公式结果位于一个单独的只读 API collection，而不是原 Base Collection 的一列；
- 编辑仍需写 Base Collection，读取分析结果则访问 View Collection；
- UI 需要处理两个 collection 的身份与字段映射；
- SQL 表达式绑定 SQLite，不能直接形成跨数据库抽象。

### 3.3 Response enrichment：可临时返回，不能自然 filter/sort

PocketBase 的 `OnRecordEnrich` 可以在内建记录响应和 realtime 序列化时添加临时 computed property。官方示例计算 `computedScore`。

来源：[PocketBase Go Event Hooks — OnRecordEnrich](https://pocketbase.io/docs/go-event-hooks/#onrecordenrich)、[PocketBase JavaScript Event Hooks](https://pocketbase.io/docs/js-event-hooks/)

该值是在记录查询/富化阶段添加。它适合：

- 按请求用户动态显示值；
- 不落库的展示属性；
- 脱敏或补充响应。

但数据库在查询时并不知道这个临时属性，因此它不能像真实列或 View 字段那样自然参与 SQL filter、sort、分页和索引。

### 3.4 Hook：可实现可写表的物化公式

PocketBase 提供 Record create/update/validate/request hooks，Hook 可以在持久化前修改 Record；批处理请求也会触发相应 record hooks。

来源：[PocketBase Go Event Hooks](https://pocketbase.io/docs/go-event-hooks/)、[Extending PocketBase](https://pocketbase.io/docs/use-as-framework/)

所以 PocketBase 可以像建议中的 Directus Hook 一样，把公式值写入普通 Number/Text 字段。它仍然是定制服务端逻辑，不是原生公式字段。

### 3.5 Generated column：不是 PocketBase 一等字段模型

PocketBase 使用 SQLite，但官方 collection field 列表没有 generated-column field。历史上的 Computed/RawSQL field 提案最终倾向于使用 View Collection，并没有形成 Base Collection 的 generated field 类型。

来源：[PocketBase 官方仓库 issue #311](https://github.com/pocketbase/pocketbase/issues/311)

可以在自定义迁移里执行 SQL，并不等于 PocketBase Dashboard、Collection API 和字段验证会把 generated column 当成一等可创建字段。因此不能把“底层 SQLite 能做”描述成“PocketBase 原生可写公式字段”。

## 4. API、关系、权限、迁移和扩展比较

| 维度 | Directus 12 | PocketBase 0.39.x | 对 VibeTable 的意义 |
|---|---|---|---|
| 可写 Base 表原生公式字段 | 无 | 无 | 两者都需要 Hook/物化或数据库机制 |
| SQL 计算视图产品化 | 可接数据库能力，但 Fields API 没有 PocketBase 式公式向导 | 一等 View Collection，SQL `SELECT`，只读 | PB 对报表/聚合读取更直接 |
| 查询 API | REST、GraphQL、SDK、Realtime | JSON REST、SDK、SSE Realtime | Directus 的 GraphQL 和通用查询栈更强 |
| filter/sort | 普通列可用；物化公式可直接使用 | Base/View 字段可用；enrichment 临时字段不可自然使用 | 两者物化后都满足网格 |
| 关系 | 多种关系模型，含 Many-to-Any | Relation 字段，支持嵌套/反向 relation 的 expand/filter/sort，文档限定最多 6 层 | Directus 更适合复杂、异构内容模型；PB 足够覆盖常见关系 |
| 权限 | Policy + collection/action + item + field + validation + preset | 每 collection 的 list/view/create/update/delete rules，同时作为记录过滤器；可引用请求与关系 | Directus 原生字段级权限 UI 更适合“字段约束前置” |
| Hooks/扩展 | 前端 Interface/Layout/Module/Panel + 后端 Hook/Endpoint/Operation + Bundle | Go framework 或内嵌 JavaScript hooks/routes | Directus 更适合同时扩展管理 UI 与 API；PB 服务端扩展更轻 |
| 迁移 | Schema snapshot/diff/apply API；自定义迁移 | 内建 JS/Go DB migrations，Dashboard/API 变更默认 automigrate | PB 对单实例 SQLite 应用的版本化体验更直接 |
| 数据库 | PostgreSQL、MySQL/MariaDB、MSSQL、Oracle、SQLite 等，能包装既有 SQL 数据库 | 仅 SQLite，官方说明只支持单机纵向扩展 | VibeTable 若要支持多数据库或既有企业数据库，Directus 优势成立 |
| 非技术用户后台 | Data Studio、文件、Insights、Flows、版本、审计等 | Dashboard 主要用于管理 collections、records、auth、files、settings | Directus 更像可交付给业务用户的管理平台 |
| 运维形态 | Node/Docker/Cloud，能力更重 | 单文件、自包含、SQLite | 本地单机场景 PocketBase 明显简单 |

相关官方来源：

- [Directus API：REST 与 GraphQL](https://directus.com/docs/api)
- [Directus Extensions](https://directus.com/docs/guides/extensions)
- [Directus Schema snapshot/diff/apply](https://directus.com/docs/api/schema)
- [Directus Database configuration](https://directus.com/docs/configuration/database)
- [Directus Content Versioning](https://directus.com/docs/guides/content/content-versioning)
- [PocketBase API Rules and Filters](https://pocketbase.io/docs/api-rules-and-filters/)
- [PocketBase Relations](https://pocketbase.io/docs/working-with-relations/)
- [PocketBase Migrations](https://pocketbase.io/docs/js-migrations/)
- [PocketBase FAQ](https://pocketbase.io/faq/)

## 5. Directus 的优势是否真的适合 VibeTable

### 优势成立的前提

如果 VibeTable 的方向包括以下任一项，继续使用 Directus 有充分理由：

- 将来支持 PostgreSQL、MySQL 等，而不只是一份本地 SQLite；
- 连接和管理既有 SQL schema；
- 需要 REST 与 GraphQL；
- 需要面向组织的字段级权限、行级权限、校验和 presets；
- 希望把 Data Studio、文件资产、Flows、Insights、版本/审计一起提供；
- 已经有 Directus 12 扩展和 Items API 适配投入。

Directus 官方明确提供 collection/action 级权限，并在其下提供 item、field、validation、preset；Database 配置也明确覆盖 `pg`、`mysql`、`oracledb`、`mssql`、`sqlite3`。

来源：[Directus Access Control](https://directus.com/docs/guides/auth/access-control)、[Directus Database configuration](https://directus.com/docs/configuration/database)

### 优势不成立或被高估的情况

如果 VibeTable 的真实边界始终是：

- 单用户或小团队；
- 本地单机；
- 永远只用 SQLite；
- 主要界面完全由 VibeTable 自己提供；
- 不使用 Directus Studio、GraphQL、Flows、Insights、内容版本；
- 更重视单文件部署和 SQL View。

那么 Directus 的很多能力只是额外体积，PocketBase 会更匹配。PocketBase 官方明确说明它只支持单服务器纵向扩展、只使用 SQLite，同时强调其目标是自包含应用；这既是限制，也是本地产品的简化优势。

来源：[PocketBase FAQ](https://pocketbase.io/faq/)

## 6. 对 VibeTable 的建议

### 当前建议：保留 Directus，采用前端预览 + Directus Hook 物化

原因：

1. 不需要直接执行 SQLite generated-column DDL；
2. 计算字段仍是普通 Directus 字段，可通过标准 Items API 返回、筛选、排序和导出；
3. 服务端重新计算消除伪造和多客户端语义分叉；
4. 可以复用当前 Directus 12 扩展打包机制；
5. 不会把公式能力绑死到 SQLite SQL；
6. 未来若换 PostgreSQL，公式元数据和 Hook 语义仍可迁移。

建议第一版范围：

- 普通物理结果列；
- Formula.js 风格但采用明确白名单；
- 只支持同一行标量字段；
- 前端即时预览；
- Directus Filter Hook 权威重算；
- 创建前语法、引用字段和返回类型验证；
- 禁止用户直接编辑计算结果；
- 公式修改支持全表重算；
- 明确错误、空值、日期和除零规则。

第二阶段再加入：

- 关系字段；
- 聚合；
- 依赖图与反向重算；
- 后台重算队列和状态；
- 公式索引建议；
- 循环依赖检测。

### 不建议

- 仅依赖 VibeTable 前端提交权威计算值；
- 让普通 API 用户拥有计算结果字段的自由写权限；
- 把 Directus Labs 的 Studio-only Calculated Interface 当成 API 公式；
- 因为 PocketBase 有 View Collection，就认为它解决了“可写原表公式列”问题；
- 在尚未确认 VibeTable 只做单机 SQLite 产品前，为公式能力整体迁移到 PocketBase。

## 最终判断

“前端算完再通过 API 写入”在交互体验上是对的，在数据权威性上只完成了一半。正确完成方式是：

> 前端负责快速预览，服务端 Hook 负责权威重算，普通字段负责 API 查询。

PocketBase 的 View Collection 确实比 Directus Fields API 更直接地支持 SQL 计算读取，但 PocketBase 也没有可写 Base Collection 的原生公式字段。对当前 VibeTable，公式缺口更适合通过一个受控的 Directus 12 Hook 补齐，而不是为此更换后端。只有当产品明确收敛为“SQLite、本地单机、自有前端、无需 Directus 平台能力”时，PocketBase 才应成为整体架构替代候选。
