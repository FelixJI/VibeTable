# PocketBase 审计与 Activepieces Community Edition 调研

日期：2026-07-24  
研究快照：PocketBase `v0.39.8`（2026-07-19，commit `cc4e857`）；`skeeeon/pb-audit` main commit `d384bb182a4171098f68c851eba4e1fd7a08be81`（2026-05-09）；Activepieces `0.86.3`（2026-07-17，commit `8bfbb2fd962eeded92d38359605a89fd14b2ec00`）。  
资料边界：只使用项目官方文档、官方源码仓库、官方发布页和官方容器仓库。许可证判断是工程风险分析，不是法律意见。

## 结论

1. 用户所说的 `pb-audit` 几乎可以确定是 [`skeeeon/pb-audit`](https://github.com/skeeeon/pb-audit)。GitHub 官方仓库搜索中，只有该同名项目声明自己是 PocketBase 审计库；其他名称相近的仓库是 Power BI、课程或无关审计项目。
2. `pb-audit` 可以作为实现参考或早期原型，但不应原样成为 VibeTable 的权威审计层。它能覆盖 Record create/update/delete 和 `users` 登录，并保存 JSON 快照、普通查询索引和保留策略；但一个成功写入被拆成 request/success 两条、没有关联 ID，success 条目又没有用户/IP，且审计写入失败不会阻止业务写入。
3. 当前源码还存在两个实质性请求元数据问题：PocketBase 把请求头键规范化成 `cf_connecting_ip` 这类 snake_case，而 `pb-audit` 查找的是 `cf-connecting-ip`，因此这些 IP 头不会命中；`request_url` 实际写入 `RequestInfo.Context`，它是 `default`、`batch` 等上下文，不是 URL。
4. PocketBase `v0.39.8` 已是当前版本，但 `pb-audit` 固定依赖 `v0.38.0`，没有 tag、GitHub Release 或自动化测试。仓库自称 `2.0.0` 只是源码常量/README 版本，不是可复现发布物。采用前必须 fork、固定 commit，并对目标 PocketBase 版本做编译和事务集成测试。
5. Activepieces CE 确实能做后端无关的外置工作流层，但免费 CE 的可行接法是“独立 Activepieces UI + Webhook 触发 + HTTP 回调 VibeTable”，不是嵌入 VibeTable，也不是用平台 API 自动管理流程。官方 API key 只在 Platform/Enterprise 版可用，Embedding 也明确是付费功能。
6. Activepieces 暂缓进入 VibeTable 核心路径。其单容器个人模式使用 PGLite（嵌入式 PostgreSQL）和内存队列，并非 SQLite；官方 `0.86.3` amd64 镜像压缩后仍为 369.32 MB，还要求容器运行时。对单机桌面应用而言，安装、进程、升级、资源和许可证审计成本明显高于当前收益。

## 一、`pb-audit` 的身份、版本与维护状态

### 项目消歧

最可能目标是 [`skeeeon/pb-audit`](https://github.com/skeeeon/pb-audit)，Go module 名也是 `github.com/skeeeon/pb-audit`。仓库说明、包名和用户提到的写法完全吻合。

在 GitHub 官方公开仓库搜索中还存在 `pbdev2022/pb_audit`、`audit-pb`、若干 `PBI/PBJ/PBM audit` 等相近名称，但它们没有声明 PocketBase 审计能力。没有发现第二个可竞争的、同名 PocketBase 审计库。因此后文的 `pb-audit` 专指 `skeeeon/pb-audit`。

### 版本兼容性

- [`go.mod`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/go.mod) 固定 `github.com/pocketbase/pocketbase v0.38.0`，并要求 Go `1.25.0`。
- [`audit.go`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/audit.go) 声明 `Version = "2.0.0"`，README 也称当前版本 2.0.0。
- 仓库没有 tag，也没有 GitHub Release；截至快照仅 19 个 commit、5 个 star、0 个 fork，最新 commit 是 2026-05-09 的 “updated pocketbase”。
- 当前 PocketBase 正式版已经是 [`v0.39.8`](https://github.com/pocketbase/pocketbase/releases/tag/v0.39.8)。PocketBase 本身仍是 pre-1.0，并明确不保证完整向后兼容，因此不能把 `v0.38.0` 的依赖声明外推为 `v0.39.x` 兼容保证。

结论：它是活跃时间不算久的小型第三方库，而不是 PocketBase 官方插件或成熟审计产品。VibeTable 若采用，应把它作为 vendored/forked 源码维护，而不是跟随未打 tag 的 main。

### 许可证

[`LICENSE`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/LICENSE) 是标准 MIT，允许商用、修改、合并、发布、分发、再许可和销售；分发时需保留版权与许可声明。它适合被编译进 VibeTable 自定义 PocketBase 二进制。

## 二、`pb-audit` 实际记录了什么

### Hook 和粒度

源码在 [`internal/audit/hooks.go`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/internal/audit/hooks.go) 注册三组 hook：

- API request hooks：`OnRecordCreateRequest`、`OnRecordUpdateRequest`、`OnRecordDeleteRequest`；
- model success hooks：`OnRecordAfterCreateSuccess`、`OnRecordAfterUpdateSuccess`、`OnRecordAfterDeleteSuccess`；
- auth hook：`OnRecordAuthRequest`，但硬编码只接受名为 `users` 的 auth collection。

因此成功的 API CRUD 通常产生两条日志；程序内通过 `App.Save/App.Delete` 的写入只有 success 条目；原始 SQL 不触发 Record hook。PocketBase 官方 [`JS database` 文档](https://pocketbase.io/docs/js-database/)也明确标注自定义 raw query 不触发 event hooks。

| 事件 | `before_changes` | `after_changes` | 用户/IP/HTTP | 是否确认提交 |
|---|---:|---:|---:|---:|
| `create_request` | 无 | 请求态 Record | 有意记录 | 否 |
| `create` | 无 | 提交后 Record | 无 | 是 |
| `update_request` | DB 原值 | 请求态 Record | 有意记录 | 否 |
| `update` | 无 | 提交后 Record | 无 | 是 |
| `delete_request` | 删除前 Record | 无 | 有意记录 | 否 |
| `delete` | 删除前 Record | 无 | 无 | 是 |
| `auth` | 无 | 用户 Record | auth method + 请求元数据 | PocketBase 将该 hook 定义为成功认证请求 |

关键限制是：库没有 `request_id`、`transaction_id`、`batch_id` 或 sequence 字段。要回答“哪个用户成功把哪条记录从 A 改成 B”，调用方必须猜测性地把 `update_request` 和 `update` 两条记录配对；create request 还可能没有 record ID。它并没有形成一条原子、完整的审计事件。

### 快照内容

[`internal/audit/logger.go`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/internal/audit/logger.go) 对 `core.Record` 执行 `json.Marshal`。PocketBase `v0.38.0` 的 [`Record.MarshalJSON`](https://github.com/pocketbase/pocketbase/blob/v0.38.0/core/record_model.go#L1323-L1328) 只序列化 `PublicExport()`；auth record 会始终排除 password 和 tokenKey，但其他公开字段仍会完整进入审计快照。

`before_changes` 和 `after_changes` 各自上限 2 MB。记录超过限制或序列化/保存失败时，库只打印 warning，业务写入继续。因此它不能提供合规意义上的“每次写入必有审计记录”保证。

### 用户、IP 与请求信息

- `user` 是到固定 `users` collection 的可选 relation。其他 auth collection 的主体无法归因；superuser 也会是 `null`。
- `CascadeDelete: false` 只保证审计记录不级联删除。PocketBase 删除被引用用户时会清空非必填 relation，因此历史 actor ID 仍会丢失。若要长期保留主体，应另外保存不可变的 `actor_id`、`actor_type`、`actor_display` 文本快照。
- `auth` 只覆盖 `users` 的成功 sign-in/token refresh 等，不覆盖 superuser，不记录失败登录，也不覆盖登出、读取、文件下载、schema/collection 变更。
- IP 提取实现依次检查 Cloudflare、X-Forwarded-For、X-Real-IP、Fly.io，但没有 socket remote address 回退，也无可信代理边界。

而且当前实现有确定的字段错配：

1. PocketBase `v0.38.0` 在 [`core/event_request.go`](https://github.com/pocketbase/pocketbase/blob/v0.38.0/core/event_request.go#L135-L140) 把 Header 名转换成 snake_case；`pb-audit` 只做 lowercase 后查找带连字符的键。正常的 `X-Forwarded-For` 会变成 `x_forwarded_for`，不会被命中。
2. PocketBase [`RequestInfo.Context`](https://github.com/pocketbase/pocketbase/blob/v0.38.0/core/event_request.go#L152-L174) 的值是 `default`、`batch`、`oauth2` 等请求上下文。`pb-audit` 却把它写入 `request_url`，所以该字段不是 URL path。

因此 README 中“IP、URL 请求信息完整”的说法不符合当前源码实际行为。

### 批量与事务

PocketBase `/api/batch` 是单一事务；官方 [`OnBatchRequest` 文档](https://pocketbase.io/docs/go-event-hooks/#onbatchrequest) 明确说内部 Record request hooks 收到的 `e.App` 是 batch transactional app。官方同时警告 hook 内应使用 `e.App`，复用外层 app 可能死锁。

`pb-audit` 的 logger 却保存了 Setup 时的根 `app`：

- update request 用 `logger.app.FindRecordById(...)` 读取 before；
- 所有日志用 `logger.app.Save(...)` 写入。

这不符合 PocketBase 的事务 app 规则。对 `/api/batch`，至少存在单 writer 死锁/超时风险，不能把 README 的“支持所有操作”理解为可靠批量支持。它也没有为同一 batch 的多条记录保存 batch ID。

success hook 会等业务事务提交后才执行，这是 PocketBase 官方保证；但此时审计日志是另一次独立 Save。业务已经成功后，即使审计 Save 失败也不会回滚业务。因此 success 日志与业务变更不是同一事务。

### 查询、保留与防篡改

[`internal/audit/collections.go`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/internal/audit/collections.go) 创建普通 `audit_logs` collection，并为 collection、record、timestamp、user、event type 及两个常用组合建立索引。可通过 PocketBase 普通 records API 过滤和分页查询。

[`internal/audit/retention.go`](https://github.com/skeeeon/pb-audit/blob/d384bb182a4171098f68c851eba4e1fd7a08be81/internal/audit/retention.go) 用 PocketBase cron 分别执行：

- 按 `MaxAge` 删除，100 条一批；
- 超过 `MaxRecords` 时按 timestamp 从旧到新删除，100 条一批；
- 两个限制可同时开启；清理错误只记录、不阻塞应用。

首次创建时五条 API rule 都设为只有 superuser 实际可访问；后续启动不会校验或恢复 schema/rule。它还明确跳过 `audit_logs` 自身，避免递归。这同时意味着：

- superuser 或本地 DB 操作者可以改删审计数据而不留审计痕迹；
- 若有人预先创建同名但错误 schema 的 collection，Setup 会接受它；
- 用户把 API rule 放宽后，库不会纠正；
- 直接 SQLite、raw SQL、backup restore、collection/schema 变更、hook 被解绑都可绕过。

单机应用的本地管理员最终总能修改 SQLite 文件，所以目标应是“应用内完整、可诊断的操作历史”，而不是宣称防本机 owner 篡改。

## 三、VibeTable 的 PocketBase 审计建议

既然所有写入必须经过 VibeTable，最稳的边界不是原样接入 `pb-audit`，而是把审计写入放进 VibeTable 自己的 mutation service：

```text
VibeTable mutation endpoint
  -> 鉴权并生成 operation_id
  -> 在 PocketBase transaction app 中读取 before
  -> 校验 + 公式计算 + 保存 after
  -> 同一事务写一条 append-only audit event
  -> 同一事务写 workflow outbox（若需要）
  -> commit
```

单条审计事件至少应含：

- `operation_id`、可选 `batch_id`、顺序号；
- `actor_type`、不可变 `actor_id`、显示名快照；
- `source = vibetable_api | system | import | workflow`；
- collection、record ID、operation；
- before、after 或字段级 diff；
- 时间、VibeTable request ID、客户端版本；
- 成功写入只记 committed 事件；失败尝试另进普通安全日志；
- 大字段/文件只保存哈希、元数据或受控截断，避免 2 MB 失败。

可以 fork `pb-audit` 重用 collection/index/retention 思路，但应至少修复：

1. 全面改用事件的 `e.App`；
2. 不再双条猜测配对，或加入稳定 correlation；
3. 正确读取 `e.Request.URL.Path`/remote address，而不是 `RequestInfo.Context`；
4. 支持任意 auth collection 和 superuser 文本 actor；
5. actor 使用文本快照，不能只用会被清空的 relation；
6. 审计写失败默认 fail-closed，或显式配置降级；
7. 增加 PocketBase `v0.39.8`、batch rollback、cascade delete、超大 JSON、raw SQL 绕过测试；
8. 将 audit collection 标成 VibeTable 内部资源，标准 records API 不提供 create/update/delete。

## 四、Activepieces CE：能力、架构与许可证边界

### 当前架构与自托管

Activepieces 官方[架构文档](https://www.activepieces.com/docs/install/architecture/overview)描述的组件是 React UI、Fastify App/API、Worker、Sandbox 和编译为单 JS 文件的 TypeScript Engine；任务通过队列进入 worker。仓库是 Turbo TypeScript monorepo，主要路径是：

- `packages/web`
- `packages/server/api`
- `packages/server/worker`
- `packages/server/engine`
- `packages/server/sandbox`
- `packages/pieces`
- `packages/shared`
- `packages/ee`

当前官方[安装总览](https://www.activepieces.com/docs/install/overview)有两条主要路径：

| 模式 | DB/队列 | 官方定位 |
|---|---|---|
| [单 Docker](https://www.activepieces.com/docs/install/options/docker) | `AP_DB_TYPE=PGLITE` + `AP_REDIS_TYPE=MEMORY` | 仅 personal use/testing，单机单实例 |
| [Docker Compose](https://www.activepieces.com/docs/install/options/docker-compose) | PostgreSQL + Redis | 生产/多实例 |

这里的 PGLite 是嵌入式 PostgreSQL，不是 SQLite。仓库中仍有旧部署页出现 `SQLITE3`，但对 `0.86.3` 的产品决策应以当前 Docker/安装总览为准。

单容器并不等于单进程：worker 仍会为 sandbox/engine 管理子进程。官方 2026 年生产建议是每个 worker 并发 1、约 0.5 vCPU/1 GB，warm process 约 300 MB；默认兼容模式并发为 5。[Worker 文档](https://www.activepieces.com/docs/install/architecture/workers)、[Limits](https://www.activepieces.com/docs/install/reference/limits)

官方 Docker Hub 的 [`0.86.3`](https://hub.docker.com/r/activepieces/activepieces/tags) 镜像压缩大小为 amd64 369.32 MB、arm64 360.72 MB，尚未包含 Docker Desktop/WSL2 本身的成本。官方没有 Windows 原生桌面 sidecar 安装方式。

### Webhook、HTTP、API、Embedding 和自定义 pieces

- CE 的 workflow builder、flow versioning、branch/loop/retry、Webhook、HTTP、Code 和大量开放 pieces 是其核心价值。Webhook 可作为外部后端的异步触发入口；HTTP action 可回调 VibeTable。两者分别位于 MIT 范围内的 [`packages/pieces/core/webhook`](https://github.com/activepieces/activepieces/tree/0.86.3/packages/pieces/core/webhook) 和 [`packages/pieces/core/http`](https://github.com/activepieces/activepieces/tree/0.86.3/packages/pieces/core/http)。
- 单机内部事件不要求公网：VibeTable 可以调用 localhost webhook。只有接收互联网服务的 webhook/OAuth 回调时才需要把 `AP_FRONTEND_URL` 暴露为可访问 URL。
- 官方[API Reference 总览](https://www.activepieces.com/docs/endpoints/overview)明确：API key 目前从 Platform Dashboard 创建，而多项目平台管理只在 Platform/Enterprise edition 提供。因此 CE 不能依赖受支持的 API key 流程来自动创建、发布或管理 flows。
- 官方 [Embedding 总览](https://www.activepieces.com/docs/embedding/overview)和用户自动 provision 文档明确把嵌入列为付费功能。VibeTable CE 方案不能把 builder iframe/SDK 当作免费能力。
- pieces 是 TypeScript npm packages，源码通常在 `packages/pieces`；官方支持贡献到主仓库或发布 public npm community piece。[Build pieces](https://www.activepieces.com/docs/build-pieces/building-pieces/overview)、[Community pieces](https://www.activepieces.com/docs/build-pieces/sharing-pieces/community)
- 平台级安装 private piece、限制 piece 可见性等管理能力的官方页面同样标为 paid editions。[Manage pieces](https://www.activepieces.com/docs/admin-guide/guides/manage-pieces)

所以 CE 最小、许可证风险最低的集成是：

```text
VibeTable commit + outbox
  -> localhost Activepieces Catch Webhook
  -> 用户在独立 Activepieces UI 配置流程
  -> Activepieces HTTP action
  -> VibeTable capability-scoped workflow endpoint
```

它不需要 API key、Embed SDK、白标或 private piece；VibeTable 也不应把 PocketBase superuser token交给 flow。回调 token 应限制到具体 table/action，并支持 rotation、幂等键和审计。

### 许可证逐路径边界

Activepieces 根 [`LICENSE`](https://github.com/activepieces/activepieces/blob/8bfbb2fd962eeded92d38359605a89fd14b2ec00/LICENSE) 不是“整个仓库统一 MIT”，而是明确排除两个路径：

1. `packages/ee/`
2. `packages/server/api/src/app/ee`

这两个路径适用 [`packages/ee/LICENSE`](https://github.com/activepieces/activepieces/blob/8bfbb2fd962eeded92d38359605a89fd14b2ec00/packages/ee/LICENSE) 的 Enterprise License；其他未另行限制的自有代码适用 MIT Expat。

工程含义：

- MIT 部分允许商用、自托管、修改、再分发、再许可、销售和提供 SaaS，没有 copyleft 或网络服务条款；必须保留版权与许可声明。
- EE 部分只是 source-available：生产使用需要有效 Enterprise 订阅；虽然开发/测试可免费复制修改，但其修改/patch 的使用和分发仍受 Enterprise License 约束，而且文本明确禁止未经许可的 copy/merge/publish/distribute/sublicense/sell。
- Embed SDK、白标和平台管理实现位于或依赖 EE 边界，不能因为仓库公开就按 MIT 使用。
- 根 MIT 没有授予 Activepieces 名称、Logo 或商标权。若做 CE-only fork/发行，应使用 VibeTable 自有品牌，并保留代码版权/许可证，不应暗示官方背书。
- 官方订阅条款的额外限制针对订阅/商业 Software，但其第 5.6 条也明确不限制底层 open-source license 已授予的权利。[Activepieces Terms](https://www.activepieces.com/terms)

最重要的再分发风险是：官方构建树并非天然的“只含 MIT 文件”归档。[`app.ts`](https://github.com/activepieces/activepieces/blob/eba4c8e59033f48ffc93c99dbb9af2f5afc803b7/packages/server/api/src/app/app.ts) 顶层导入多个 `src/app/ee/**` 模块，COMMUNITY 分支也会注册其中部分模块；官方 [`Dockerfile`](https://github.com/activepieces/activepieces/blob/eba4c8e59033f48ffc93c99dbb9af2f5afc803b7/Dockerfile) 先复制、编译整个 `packages/`，后续删除 `packages/ee` 源工作区，但这不能证明编译产物排除了 `packages/server/api/src/app/ee`。`AP_EDITION=COMMUNITY` 是运行时功能选择，不是“制品只含 MIT 代码”的证明。只要 VibeTable 安装包或自建镜像包含上述两个路径/其编译产物，就不能仅凭根 MIT 宣称可自由再分发。

因此：

- 内部测试：可让开发者直接从官方 Docker Hub 拉取固定 tag；
- 对用户提供可选安装：优先让用户从官方来源自行拉取固定 digest，不把镜像塞进 VibeTable 安装包；
- 商业再分发、白标或嵌入：先取得 Activepieces 书面授权；
- 若一定要分发免费 CE：制作并审计真正排除两个 EE 路径及其编译产物的 CE-only build，并重新做依赖许可证清单。现有仓库不能只看顶层 LICENSE 就完成该确认。

此外，官方 pieces 默认会从 Activepieces 注册表同步。严格离线发行必须预置并固定所需 pieces，不能假定全新实例第一次运行不访问外网。[Piece syncing](https://www.activepieces.com/docs/install/architecture/piece-syncing)

## 五、是否替代 Directus Flows

如果 VibeTable 同时支持 Directus/PocketBase，Activepieces 可以作为共同的“提交后外置自动化层”，但不能等价替代后端内事务 hook：

| 能力 | 后端 hook / Directus Flow | Activepieces CE 外置层 |
|---|---|---|
| 保存前校验、公式、拒绝写入 | 适合 | 不适合 |
| 与数据写入同一事务 | 可以 | 不可以 |
| 提交后通知/同步第三方 | 可以 | 很适合 |
| 长流程、延迟、重试、分支 | 有限/依实现 | 很适合 |
| 跨后端统一 | 否 | 是 |
| VibeTable 内嵌 UI | 原生于后端 | CE 不允许使用付费 embedding |
| 程序化 provision | 后端 API | Activepieces API key 属付费版 |

正确边界应是：

- 公式、权限、校验、审计：留在 VibeTable mutation service/PocketBase transaction；
- 工作流：只处理 committed event；
- 用 transactional outbox 防止“数据提交成功但 webhook 未发出”；
- Activepieces 回调带 operation ID，VibeTable 强制幂等；
- 不允许 workflow 直接写 SQLite 或 PocketBase 标准 mutation API。

但当前产品方向已经去掉 Directus，跨双后端的收益随之下降；Activepieces 的独立 UI、第二套数据库、容器和付费 API/Embedding 限制会变成纯额外成本。因此本次结论是：**已评估、暂缓引入**。先完成 PocketBase mutation/audit/outbox seam；未来用户明确需要可视化长流程时，再把 Activepieces 作为可选外部服务接入，而不是 VibeTable 桌面版必装组件。

## 六、建议决策

### 现在做

1. 自建 `AuditService`，与公式计算和数据 mutation 共用 PocketBase transaction app。
2. 创建 append-only `vibetable_audit_events`，标准 API 只读或完全不暴露写权限。
3. 同时定义 `WorkflowOutbox` 表和 dispatcher 接口，但先不实现 Activepieces provider。
4. 可以 fork `pb-audit` 借用 schema/index/retention 代码；不要原样依赖 main。
5. PocketBase 固定到明确版本，并把 batch、rollback、raw SQL 绕过测试放入验收。

### 暂不做

1. 不把 Activepieces 官方镜像打进桌面安装包。
2. 不使用 CE 中不可获得的 API key、Embedding SDK、白标和 private-piece 管理能力。
3. 不让 Activepieces 参与同步公式计算、保存前校验或审计事务。

### 未来触发重新评估的条件

- 用户确实需要分支、延迟、重试、人工审批、第三方 SaaS 编排；
- 可以接受 Docker/PGLite 第二数据目录和约 1 GB worker 级资源预算；
- 产品接受独立 Activepieces UI，或愿意购买 embedding/platform 许可；
- Activepieces 对 CE-only 再分发边界给出书面确认，或 VibeTable 已有可审计的 CE-only build；
- VibeTable outbox、幂等回调、capability token 和工作流审计已经完成。
