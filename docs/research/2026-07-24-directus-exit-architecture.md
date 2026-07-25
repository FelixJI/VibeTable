# VibeTable 去 Directus 架构决策

日期：2026-07-24

## 决策

VibeTable 不再以“长期维护 Directus/PocketBase 双后端等价”为目标。目标架构是：

- PocketBase 成为唯一运行时数据后端；
- VibeTable 自己拥有字段约束、公式、mutation、审计、幂等和插件能力；
- Directus 实现从未实际发布，因此不提供迁移、导入、双写或兼容开关；PocketBase 纵切达到产品契约后，直接从安装包、运行时和源码移除 Directus；
- Activepieces 已完成评估，但暂不进入核心路径；
- 插件/代码自动化只能通过 VibeTable capability/mutation API 写数据，不能直接写 PocketBase。

## 目标进程边界

```text
Vue / .NET Desktop
        |
        v
VibeTable Python RPC Host
  - 查询/写入契约
  - 插件 Worker 与 capability broker
  - 导入导出、任务与 UI 交互
        |
        v
VibeTable PocketBase Go Sidecar
  - 自定义 mutation / formula preview 路由
  - schema 与权限校验
  - cel-go 公式编译/执行
  - transaction：数据 + 审计 + 幂等 + outbox
        |
        v
PocketBase SQLite
```

标准 PocketBase 写 API 不作为受支持入口；桌面 UI、导入、插件和未来自动化统一经过 VibeTable mutation port。查询可以继续使用 PocketBase 查询能力，但由 Adapter 转译 VibeTable 的查询 AST。

## 公式引擎

### 选择

采用自定义 PocketBase Go 二进制和 `cel-go`，不再为 Directus/Node 维护第二套权威 evaluator。

第一版公式范围：

- 同一行的确定性标量表达式；
- number、string、boolean、date/datetime、JSON 基础访问和 null；
- 静态字段依赖；
- 固定时区、稳定错误码和版本化函数白名单；
- 禁止赋值、循环、动态字段名、网络、随机数和隐式当前时间。

### 保存语义

1. 客户端只提交源字段，不允许写公式结果字段。
2. PocketBase mutation route 在事务内加载旧值、验证约束、计算公式并写入普通结果字段。
3. 公式结果字段可被 PocketBase API 正常查询、筛选、排序和导出，但 schema 标记为只读。
4. 保存响应返回持久化后的完整行；前端不维护第二个真相源。
5. 公式编辑器需要即时反馈时，调用同一 Go evaluator 的 preview/validate 路由，不落库。
6. 公式定义保存 expression、canonical AST/hash、result type、dependencies、engine version 和 status；修改公式触发可恢复的 backfill。

## PocketBase mutation 内核

建议建立一个版本化入口，例如：

```text
POST /api/v1/vibetable/mutations
POST /api/v1/vibetable/formulas/preview
```

一次 mutation transaction 依次完成：

1. 验证调用身份、scope、schema revision 和 idempotency key；
2. 读取 before；
3. 应用类型、nullable、required、default、unique、长度、精度和 JSON schema 等约束；
4. 编译/执行受限 CEL，写入公式结果；
5. 保存业务记录；
6. 写入一条 committed audit event；
7. 写入 outbox event；
8. 提交并返回 after、revision、operation ID。

事务内只做数据库和确定性计算；外部 HTTP、通知和插件执行必须在提交后消费 outbox。

## 审计

不原样采用 `skeeeon/pb-audit` 作为权威审计。它可以作为 MIT 代码参考，但当前版本存在：

- request/success 双事件无稳定 correlation；
- success 事件缺 actor/IP；
- 请求 IP/URL 字段实现与 PocketBase 的 RequestInfo 结构不匹配；
- batch 中未使用事件 transaction app，存在死锁/一致性风险；
- 审计失败默认不阻断业务写入；
- 固定 PocketBase 0.38.0、无 tag/release/tests。

权威审计应由 VibeTable mutation route 在同一 `RunInTransaction` 中写入。事件至少保存 operation/batch ID、actor 文本快照、source、before/after 或 diff、schema/formula version、客户端版本与时间。`pb-audit` 可被 fork 后作为额外的绕过检测，但不是主真相源。

## 插件与代码自动化

现有插件系统应保留：

- 隔离 Node Worker；
- package/schema 校验；
- capability 白名单；
- 私有存储、任务、交互和插件审计；
- 插件返回 mutation plan，Host 校验并执行。

需要拆除的 Directus 耦合：

- `directus_service.plugin_service` composition root；
- “Directus profile”命名和校验实现；
- `DirectusBulkMutationAdapter` 与 `/vibetable-bulk-mutation/apply`；
- `directus_flow_uuid`、Directus Flow binding 和相关兼容声明。

替代方案：

- `DataProfilePort`：提供 provider-neutral schema 与字段 grant；
- `MutationPort`：把 mutation plan 发送到 PocketBase VibeTable route；
- `NetworkRequestPort`：Host 代理外部 HTTP，按插件声明限制域名、方法、超时、响应大小和凭据；
- 第一阶段只支持手动 action；schedule/event/webhook 以后用本地 trigger registry + outbox + retry 扩展，不先建设可视化工作流引擎。

插件不应拿到 PocketBase superuser token，也不应开放原生 Node 网络。外部代码若不是 `.vtplugin` Worker，应使用短期、scope 限定、可撤销的 VibeTable API token，并强制 idempotency key。

## 替换顺序

### M0：冻结产品契约

- 定义 normalized schema、mutation request/receipt、formula definition、audit event 和 capability error。
- 明确 null、JSON、date/time、decimal、unique 与公式错误语义。

### M1：PocketBase Go sidecar 骨架

- 固定 PocketBase 版本；
- 健康检查、生命周期和本地认证；
- migration、backup/restore；
- 禁用或限制标准写 API。

### M2：公式纵切

- `cel-go` 最小函数集；
- preview/validate；
- 单行 create/update 事务内物化；
- 依赖、循环、类型、null、回滚和 backfill 测试。

### M3：约束、审计与批量

- 字段约束和 JSON；
- mutation receipt、幂等和 optimistic concurrency；
- append-only audit + outbox；
- batch 全回滚与大字段策略。

### M4：桌面数据路径切换

- `PocketBaseTableGateway` 实现现有 provider-neutral table seam；
- 查询 AST、分页、relations、files 和 SSE realtime；
- 默认新项目只创建 PocketBase workspace。

### M5：插件去 Directus

- `DataProfilePort` / `MutationPort`；
- 删除 Directus Flow binding；
- 加入受控 `network.request`；
- 将现有插件测试改为 provider-neutral + PocketBase integration。

### M6：删除 Directus

- PocketBase 通过现有产品契约及新增公式/审计 acceptance tests；
- 删除 Directus Adapter、四个扩展、local-directus 运行时、首次登录/管理员创建流程及对应测试；
- 删除 Directus 构建、发布、manifest、环境变量和资源打包逻辑；
- 不创建任何 Directus workspace 导入或迁移工具。

## 第一个可验证里程碑

只做一张 PocketBase 表和两个公式字段：

```text
subtotal = quantity * unit_price
total = round(subtotal * (1 + tax_rate), 2)
```

必须同时证明：

- create/update 只能经 VibeTable mutation route；
- 前端 preview 和保存结果来自同一 CEL evaluator；
- 用户提交伪造的 `subtotal`/`total` 会被拒绝；
- 公式错误使整次写入回滚；
- 业务记录、审计、幂等键和 outbox 原子提交；
- 重放相同 idempotency key 不产生第二次写入；
- 插件 mutation plan 经过同一入口得到相同结果。

这个纵切通过后，再扩充完整字段约束、JSON、关系和文件；不要先做整个 PocketBase Adapter 再补公式与审计。
