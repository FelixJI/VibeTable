# Directus 字段能力审计进度

## 2026-07-24 最终实施进度

- WP-00～WP-16 实现与各工作包自测完成。
- 已完成多轮独立 Standards、Spec、UI/可访问性审查并修复高优先级问题。
- 最终源码冻结后执行统一 `qa/next.py --ci` 发布门禁；产物、截图与报告统一写入
  `build/qa`，最终结论同步到 PocketBase 验收证据文档。

## 2026-07-24 WP09 Junction/M2A

- 已确认测试 seam：frozen schema、Relation service、MutationKernel receipt/audit/outbox、
  Lookup calculator/reverse dependency、真实 PocketBase。
- schema TDD 第一纵切完成：optional `mode`、junction source/target field、M2A
  discriminator/allowlist；旧 direct wire 保持兼容；严格拒绝非 many、可写 projection、
  缺失/重复/越界 allowlist 与混用 metadata。
- 定向 `go test ./internal/schema` 通过。

## 2026-07-23

- 已读取 `planning-with-files` 与 `grilling` 技能说明。
- 已确认当前 worktree、分支和现有未提交修改。
- 已初始化审计计划、发现和进度文件。
- 已创建并切换到 `codex/directus-field-audit`。
- 已核对现有 3 个未提交改动，确认是 Directus collection reconciliation 相关代码与测试；未改动它们。
- 阶段 1 完成；下一步梳理字段模型、建表流程、网关实现和测试。
- 已定位公式字段的 Directus 12 真实集成测试，以及 generated column 的 schema 映射。
- 已确认公式字段现状更接近“可读不可建”，而不是“完全不支持”。
- 已走完新建表 UI、共享契约、服务与 Directus collection payload 的主链路。
- 已确认关系字段能力存在于独立后端服务，而不是新建表流程。
- 已用当前 Directus 官方文档核对字段对象、标准类型、alias 和 generated column 语义。
- 已核对项目实际 Directus 版本与 SQLite 部署方式。
- 已发现 read/edit schema 对 Directus 类型和 Studio interface 的进一步能力降级。
- 已从 Directus 12 真实集成测试确认公式创建为什么绕过 FieldsService。
- 已确认关系管理 UI 覆盖四类关系与预设。
- 已确认 select/multi-select 前端编辑器存在但未接入 Directus 字段元数据。
- 已建立覆盖矩阵并识别测试证据缺口。
- 阶段 2–4 完成，进入 grilling 的产品边界确认。
- 定向测试首次因本机命令入口问题未启动，正在用仓库虚拟环境和 `npm.cmd` 重试。
- 前端定向测试通过：3 个文件、44 个测试。
- Python 测试尚未执行断言，因 pytest-cov 插件缺失；正在覆盖默认 addopts 重跑。
- `.venv` 还缺 pytest-asyncio，第二次仍未进入断言；将尝试已有系统 Python，且禁用 pytest cache。
- Codex Python 也无 pytest；按三次失败协议停止环境重试。
- 验证结果：前端 44/44 通过；Python 未执行，原因是测试依赖缺失。
- 2026-07-23：根据用户质疑重新核验 Directus 公式能力。
- 已检查当前官方 API 文档和本地安装的 Directus 12.1.1 官方源码，修正了此前对
  create-field request 的过度推断。
- 已确认用户要求补齐字段约束、前置验证与 JSON 能力。
- 下一项 grilling 决策：公式字段是否先限定为本地 Directus/SQLite 能力，远程 Directus 明确降级为只读发现。

## 2026-07-24

- 用户确认主要产品形态是单机 SQLite，并提出 Directus/PocketBase 双后端选择。
- 已恢复并更新 planning-with-files 记录；自动 session-catchup 因环境无可用 Python 改为手工恢复。
- 本轮使用 research 调查公式依赖，使用 codebase-design 检查双 Adapter seam。
- 不修改业务代码；先输出计算真相源设计、端点差异与双后端成本。
- 已完成公式引擎候选核验：Formula.js 不是 parser；HyperFormula 许可/模型不合适；mathjs/JSEP/小型自有 AST evaluator 是候选，但 PocketBase Goja 必须真实原型验证。
- 已完成现有 seam 审计：`ITableRpcGateway` 可深化，`IDirectusRpcGateway` 与 Python `DirectusService` 不应由 PocketBase 仿制。
- 正在整理最终讨论结论；等待后台研究报告落盘。
- 公式引擎一手资料报告已写入 `docs/research/2026-07-24-formula-engine-options.md`。
- 本轮研究与 seam 设计已完成，没有修改业务代码；等待用户确认后进入实施计划。
- 用户确认所有写入必须经过 VibeTable，公式权威计算可以收口到统一 mutation 模块。
- 新一轮调研开始：`pb-audit` 与 Activepieces CE；重点核验许可证和可分发/嵌入边界。
- 已定位 Activepieces 的 Context7 官方文档索引；正在核对 CE 自托管拓扑、API/Webhook、嵌入和企业版边界。
- 已确认 Activepieces 是 MIT CE + 商业 `packages/ee` 的混合许可；单机可用 PGLite/内存队列，生产推荐 PostgreSQL/Redis。
- 已确认 `pb-audit` 为 MIT，能提供记录级前后值审计，但管理员归因、同库可篡改和审计失败不阻断是明确边界。
- 已确认 Activepieces 管理 API Key 与官方 Embedding 均为付费版能力；CE 的安全集成面应收敛为“独立 UI 配置 + Webhook 触发 + HTTP 回调 VibeTable”。
- 已区分 CE 普通 Flow Webhook 与付费的 Platform Event Streaming；VibeTable 应自己维护 outbox 并调用普通 Webhook。
- 用户把目标改为逐步去除 Directus；Activepieces 暂退出核心路径，开始检查现有插件 Worker 与 PocketBase Adapter 的可复用边界。
- 已确认现有插件的 mutation-plan 安全模型可保留，仅需替换 Directus profile、bulk endpoint 和 Flow binding。
- 已用 PocketBase 官方文档确认自有 Go binary + `RunInTransaction` 能承载 CEL、原子审计/幂等/outbox；提交后 hooks 只用于异步派发。
- 后台研究报告已落盘：`docs/research/2026-07-24-pocketbase-audit-activepieces-ce.md`。
- 已形成去 Directus 架构决策：`docs/research/2026-07-24-directus-exit-architecture.md`；本轮未修改业务代码。
- 用户确认无需迁移；开始输出 PocketBase 等价功能、新增能力、测试与脚本适配矩阵。
- 已完成第一轮 Directus 文件与测试清单扫描；发现一次不存在目录导致的组合命令退出，已改为窄范围独立扫描。
- 递归 PowerShell manifest 枚举超时；后续统一用 `rg --files`，不重复该命令。
- 已获取所有项目 manifests、RPC 注册面与 `DirectusService` 方法清单，开始按“产品 interface / Directus implementation”分类。
- 已展开 composition root，确认产品 modules 可保留、Directus 总装配对象必须拆除；开始核对 PocketBase 原生覆盖范围。
- 已用 PocketBase 当前官方文档核对字段、集合、批量、查询、实时、规则和日志原生能力；开始映射到现有 VibeTable modules。
- 已审查 .NET table interfaces 与 MainWindow 启动链；确认 `ITableRpcGateway` 可保留，Directus session/startup/admin 实现需整组替换。
- 已扫描生产/测试中的 Directus 引用和 build/dev/release tooling，形成删除、改名、替换三类。
- 已完成 PocketBase 直接替换功能矩阵：区分原生等价、复用产品 Module、必须新增和删除。
- 已确认公式采用服务端单一真相源：同一 Go evaluator 提供 preview，并在 mutation transaction 内物化，不由前端维护权威副本。
- 已列出 Python、.NET、Vue、真实后端集成、四个 Directus 扩展、Release/Dev/QA/Handoff/E2E 测试的删除、改写与新增项。
- 已列出 `dev.py`、`build_next.py`、`versioning.py`、`release.py`、QA、local-directus、迁移脚本、桌面运行时和发布 manifest 的适配项。
- 已删除架构报告中的 Directus 迁移阶段，明确 Directus 从未发布，不做迁移、双写或兼容层。
- 最终矩阵已写入 `docs/research/2026-07-24-pocketbase-replacement-matrix.md`；本轮未改动业务代码，也未运行产品测试。
- 用户确认产品是单人桌面应用；已把 Comments/Mentions/Notifications/协作活动流从“待新增”改为“删除”，并把 `CollaborationService` 中仍有价值的 activity/revert 断言归入 History/AuditHistory。
- 已按 PocketBase 0.39.9 官方文档/源码完成文件能力核验并写入 `docs/research/2026-07-24-pocketbase-file-capabilities.md`。
- 已确认“云端资源附件”只是 disabled 的 Directus Files 占位；矩阵改为删除独立云端 tab，PB File field 作为记录内“托管附件”，本地工作区文档保持独立。
- 已核对 PocketBase v0.39.9 FileField 与路径源码：原生按 `collectionId/recordId/filename` 形成每表/每记录命名空间，但不支持每表任选本地目录或独立 S3 后端。
- 已将附件首版边界冻结为 PocketBase 原生托管附件；任意 per-table 存储后端仅在出现第二个真实 Adapter 时再设计。
- 已完成目标进程拓扑、深 Module/Interface、normalized schema、internal collections、Go API、Mutation contract、公式 F1/F2 和附件规格。
- 已拆分 WP-00 至 WP-16 共 18 个可合并工作包，并给出 M0–M5 纵切顺序、文件映射和 Directus 删除门槛。
- 已补齐 Grid State、View、聚合、Insights、Content Version、Identifier Mapping、插件网络、Workspace index、观测和可选 Dashboard 的归属。
- 已完成 unit/contract/real integration/E2E/fault-injection/package smoke 测试矩阵，以及 Dev/QA/Release/安装脚本改写计划。
- 完整计划已写入 `docs/plans/2026-07-24-pocketbase-implementation-plan.md`；相对研究链接、关键能力覆盖和 `git diff --check` 已通过。
- 本轮仅修改规划/研究文档，未修改业务代码，未运行产品测试。
# 2026-07-24 PocketBase 实施执行日志

- B03/WP-03 已完成主实现：normalized schema、17 个 internal collections、
  PocketBase field/index compiler、Describe/ValidateChange/ApplyChange/GetRevision
  和真实 PocketBase 集成测试；子代理全量 Go test/vet 通过，待主线程统一复核。
- B05/WP-12 已完成主审：Python 工作区索引 8/8、.NET Workspace 75/75、
  Desktop 宿主 26/26、Vue store 5/5 全绿；实现保持元数据与 `pb_data`
  隔离，并保留本地版本、恢复和关联语义。
- B01/WP-01 与 B02/WP-02 已交给独立只读审查代理，等待按严重度回报后修复。
- B01/WP-01 与 B02/WP-02 独立审查发现 5 个 P1、3 个 P2、1 个 P3；
  .NET 生命周期修复已回派原审查代理，主线程已补真实路由鉴权、DB/schema
  readiness 与 build schema identity。
- SchemaCatalog 已接入受 session header 保护的 list/describe/validate/apply HTTP
  路由；真实 sidecar 黑盒覆盖 apply、describe、list、严格无效 JSON、内建 API/Admin
  未授权访问和优雅退出。
- B02 审查修复已由主线程复核：PocketBase 定向 12/12、Infrastructure 115/115、
  Desktop 220/220；停止取消、迟到 secret 日志、自动崩溃恢复、identity/schemaReady、
  Ready/exit 竞态和不安全 Admin 入口均已收口。
- B04/WP-04 QueryPort 主实现完成，包含参数化 AST compiler、JSON/关系、aggregate、
  snapshot、稳定分页与真实 PB 集成；正在独立只读审查，HTTP/SchemaSource 纵切待
  WP-03 契约修复后由主线程接线。
- B04 独立审查发现未生产装配、快照可伪造、page/count 非一致视图、archive/NULL
  语义、aggregate 资源与别名、取消传播及主键硬编码等问题；内部修复已回派审查代理，
  主线程保留最终 SchemaSource/HTTP 接线所有权。
- WP-12 独立审查问题已修复：workspace RPC 脱离 Directus 总装配并始终注册；
  revision 全身份校验禁止跨文档复用；SQLite schema version 启动即拒绝不兼容；
  C# 新增 publishIndexBatch、可达版本链同步和 durable metadata outbox。
- 当前统一回归：Python backend 581 passed / 3 skipped；Vue 90 files / 548 tests；
  .NET solution 441/441 全绿。
- 已建立线程目标：完成实施计划全部 WP，并进行独立审查与验证。
- 已读取 `planning-with-files` 与 `route-subagents` 技能。
- 已恢复仓库状态：当前分支 `codex/directus-field-audit`，仓库约 781 个文件。
- 已确认实施计划共 762 行，包含 WP-00～WP-16、M0～M5 和完整验收矩阵。
- 已确认开始时存在 3 个用户未提交 Directus 相关修改，实施中必须保留。
- 首次 session-catchup 因 `python` 不在 PATH 失败；随后使用 Codex bundled Python 成功运行。
- 已完整阅读实施计划的架构、数据模型、API、公式、附件、工作包、测试、发布和删除门槛。
- 已启动 3 个只读子代理，分别负责工作包拆解、Directus 耦合盘点和现有改动/工具链审计。
- 基线：Vue 89 files / 545 tests 全绿。
- 基线阻塞：Go 与 .NET SDK 缺失；Python 环境缺 pytest-asyncio 且旧 cache ACL 异常。
- 已获取官方 Go 1.26.4 便携工具链并验证 `go version`；Go sidecar 可进行真实构建。
- 已获取官方 .NET SDK 10.0.100 便携工具链；桌面项目可进行真实编译与测试。
- B00/WP-00 第一版已由子代理完成：JSON Schema bundle、8 个 fixtures、兼容规则和
  Python 规范测试；子代理定向结果 5 passed，等待主代理审查与跨语言补齐。
- E0 完成：已形成 20 个细分批次和共享热点单一所有者规则。
- 主代理复核 B00 schema 共 41 个定义，Python 规范测试 5/5 通过。
- 已将 Go 工具链完整解压到 `.tools/go-full/go`，恢复标准库编译能力。
- .NET 基线 421/421 通过；Python 基线 727 通过、4 跳过、2 个环境失败。
- B00 跨语言门槛：Python 5/5、C# 2/2、TypeScript 2/2、Go round-trip 随 B01
  `go test ./...` 通过。
- B01 主审：`go test ./...`、`go vet ./...`、`go build -trimpath` 通过；随后真实进程
  黑盒发现 ready 握手时机缺陷，已回派修复并要求新增黑盒测试。
- B02 主审：Infrastructure 6/6、Desktop 3/3 定向测试通过；未改用户冻结文件。
- B01 ready/shutdown 真实进程黑盒通过；B02 已接入“鉴权 shutdown → 等待退出 →
  超时强杀”策略，更新后 Infrastructure 7/7、Desktop 3/3 通过。
- E1/M0 完成；MainWindow 最终组合接线按拆解归入 WP-14。
- 已并行启动 B03/WP-03 SchemaCatalog 与 B05/WP-12 工作区文档解耦。
# 2026-07-24 实施续记

- B03 独立审查已定位并正在修复：字段重命名必须保留 PocketBase field ID；Go wire
  类型必须与冻结 v1 契约一致；已验证但无法实现的约束必须稳定拒绝；支持自关联；
  字段 metadata 唯一键改为 `(table_id, field_id)`。
- B04 独立审查已定位并正在修复：QueryPort 必须有生产 SchemaSource/HTTP 路由；
  snapshot 改为带随机 nonce 的 HMAC；page/count/revision 同事务；归档策略 fail-closed；
  聚合、NULL、operator、group logic、取消和主键语义补齐。
- B02 + WP12 独立复核确认 B02 supervisor 核心路径无新增问题，但真实桌面 composition
  仍待 WP14 纵切接入。
- WP12 新发现并正在修复：CAS 分叉 revision 不能丢出发布 outbox；发布必须验证主头祖先
  连续性；生产 link 不能按正则默认授权；CreatedAt 必须端到端保留；残缺旧库不能被
  误标为当前 schema。
- B03 第二轮独立审查已修复：约束类型适用性/重复约束/field-level index 引用、
  persisted field type change、完整 frozen v1 strict decode、schema/data revision 双份
  invariant 与 JS safe integer、存储错误脱敏、partial internal schema 启动拒绝。
- B04 已完成生产装配：session purpose-derived HMAC key、normalized schema→query
  descriptor adapter、`/query` 与 `/query/validate-snapshot`，真实 sidecar 黑盒通过。
- 当前 Go 定向门槛：auth/app/query/queryschema/schema/schemaapi/migrations/cmd 全绿；
  MutationKernel 集成仍在子代理 TDD 中，尚未计入全量绿灯。

# 2026-07-24 M1/M2 续记

- WP-12 已完成第二轮 Python/C# 独立审查与修复：跨进程 Ref CAS、
  稳定 conflict ref、严格发布回执、UTC RFC3339、SQL head CAS；C# 四套
  测试 453 项全绿，Python 定向 35 项与 Ruff 全绿。
- WP-06B 已进入真实纵切：CEL relation 解引用、跨表目标字段静态验证、
  `vibetable_formula_dependencies` reverse metadata、10k fan-out 上限、
  durable cursor/job 与取消后恢复已实现；当前等待 WP-05 审查将受限
  `system/formula-fanout` 空更新加入白名单后完成集成测试。
- WP-11 增加 `task.changed` durable outbox/SSE、严格
  `/events/reconcile`，支持 schema/data revision 的 `none`、
  `refresh-data`、`reload-schema` 决策。
- WP-15 发布闭环新增受 sidecar session 保护的备份 list/create/restore
  产品 API：创建前强制附件引用/hash/orphan 完整性门槛，归档返回
  SHA-256，restore 前自动创建 `pre_restore_*` 安全副本；真实 PB 备份
  集成测试已通过。
- WP-06B 交叉白名单已收口：只有
  `system/formula-backfill` 与 `system/formula-fanout` 可提交空 update；
  relation formula、Lookup reverse dependency、fan-out 取消/恢复的真实
  PocketBase 集成测试全绿。

- WP-05 MutationKernel 已完成：严格 frozen v1 request/receipt/event、单条与原子批量、
  row/data revision、digest guards、幂等 replay/conflict/expiry、audit/idempotency/outbox
  同事务、提交后 publisher、archive/restore/delete、fault injection 和真实 PB 测试。
- 主线程已把 MutationKernel 与真实 `formula.Calculator` 接入生产
  `/api/vibetable/v1/mutations/{preview,apply}`，并将 formula.Error 原样穿透；
  黑盒进程通过 schema→formula preview→mutation apply/replay→query/snapshot。
- WP-06A 第一版已实现 cel-go 编译/类型检查、静态依赖、DAG/cycle、增量重算、
  AST hash、cost/timeout/长度/节点/递归/集合限制、确定性字符串与数学函数、UTC
  timestamp、schema validate/apply 阻止无效公式和 mutation 事务内物化。
- WP-06A 独立审查发现并正在修复生产输入适配（json.Number、PB JSONRaw/DateTime）、
  日期/min/max 函数、深层集合限制、严格 route、缓存身份和 `vibetable_formulas`
  metadata 原子持久化；migration 已改为 `(table_id, field_id)` 唯一。
- WP-07 已扩充权威 audit envelope：data revision、actor、occurred_at 和
  `(change_set_id, sequence)` 唯一约束。新增 Go AuditHistory：change-set 分组、
  table/row/cell/archived 过滤、字段裁剪、两阶段 HMAC token、过期/篡改/一次性、
  preview 后 digest conflict，以及 restore 必须回到 MutationKernel。
- WP-07 真实 PB 测试覆盖：历史查询、cell 过滤、操作者、批量 change set、token
  篡改与复用、stale preview、恢复后公式重算和新 audit/outbox receipt。
- WP-12 Python 第二轮修复完成：provider-neutral record_exists、生产 Directus 临时
  resolver、UTC CreatedAt、existing-v1-before-repair、严格 SQLite schema/index 形状、
  SQL conditional CAS 与双连接并发测试；35 个定向测试及 Ruff 全绿。
- WP-06A 异作者终审完成并修复全部已识别缺口；当时 `go test ./...` 与
  `go vet ./...` 全绿，M1 最小纵切完成。
- WP-07 已接入 production `history-restore` purpose key、同一 MutationKernel 和
  `/history/change-sets`、`restore-preview`、`restore-apply`；独立审查继续收紧归档
  restore、schema drift、token 状态机、扫描预算和严格 HTTP 映射。
- WP-08 第一版完成：setAttachments mutation seam、multipart upload staging、PB 原生
  File 生命周期、metadata 同事务、HMAC 短期 capability、protected download、thumbnail、
  引用/缺失 metadata/孤儿扫描及真实故障回滚；生产 app 已接线，已启动异作者审查。
- WP-08 异作者终审完成：上传句柄按请求隔离且幂等、multipart 引用精确绑定、全局
  256 MiB 暂存预算、冻结 `/files/token`、严格 400/404/500、capability/thumbnail MIME、
  原名安全和 hash/size/孤儿缩略图完整性；审查时全量 Go test/vet 通过。
- WP-09 第一纵切完成：schema.apply 同事务维护 relation/Lookup metadata；建表前验证
  relation/target/output storage；MutationKernel 精确校验 relation 目标与 cardinality；
  统一 computed pipeline 物化 direct Lookup；新增 describe/search/delta/lookup query API，
  真实 PB 覆盖关联作者搜索、写入、清空和只读 Lookup 重算。
## WP10 Python 纵切（子代理 repo_coupling_audit）

- 已读取 planning-with-files 与 TDD 完整说明、tests/mocking 参考。
- 已确认测试仅 mock PocketBase/文件系统等系统边界。
- 已完成首轮源码/测试文件枚举；发现输出过大被截断，下一步按公共类和协议定向读取。
- 已定位 `.venv` Python；session-catchup 缺失问题已有替代路径。
- 已添加 PocketBase HTTP client seam 的首个红测试；当前失败首先暴露测试环境 addopts/cache 问题，已切换到隔离 cache。
- PocketBase adapter 红→绿：frozen mutation、QueryPort、JSON 值、session header、结构化错误测试 3 passed。
- 新增 `backend/adapters/pocketbase/{client,transport}.py`，仅允许产品路由与 loopback origin。
- PocketBase bulk mutation 第二条 TDD 切片首次运行按预期失败：发现 schemaRevision 不能从 legacy profile 可靠推导，接口将改为显式传入 preview 绑定 revision。
- bulk mutation schema revision 修复后，adapter + paste 回归 19 passed。
- import 原子语义测试已反转；首次运行被系统 TEMP 权限阻断，下一轮使用隔离临时目录以获取真实红灯。
- import 原子语义获得真实红灯：两个测试均证明旧实现发送 2 个独立 POST，下一步收敛为单请求。
- import apply 已改为单请求、提交前取消、失败全回滚、稳定幂等键；首轮全文件测试暴露一个机械性 `uuid` import 回归。
- import/paste/PocketBase adapter 回归 46 passed；原子 import 两条反转测试均已绿。
- Export 第四条 TDD 切片已红：9 个测试确认现服务仍要求 Directus client；测试已改为公开 `QueryPagePort` seam，并加入 JSON/附件 manifest round-trip。
- Export QueryPort/JSON/附件 manifest 已绿：9 passed。
- PB Lookup 路由测试已红：client 尚无 describe/query 方法。
- PB Lookup client 红→绿：`lookups/describe` 与 `lookups/query` 产品路由 4 passed；transport 支持安全 query 参数。
- PocketBase Lookup export provider 已实现 catalog/revision/field 映射；首轮行为测试仅剩测试文件漏 import 的机械错误。
- Lookup provider 测试已绿（5 passed）；合并 Export 测试时忘传已知 `--basetemp`，9 个错误均为同一 TEMP 权限环境问题，不是代码失败。
- relation 显式 unique-match 测试已红：PB provider 尚未实现；测试固定了 relations/schema/QueryPort 组合 seam。
- PB relation import provider 红→绿：normalized relation catalog + target schema unique constraint + QueryPort eq，6 passed；跨表 create/upsert 明确 fail closed，不回退直写。
- 已向根代理发送集中 composition wiring；本代理未保留 `directus_service.py` 改动。
- paste 并发 token claim 测试已加入；首次红灯被第二次策略刷新缺少 fake response 提前截断，补齐边界响应后继续验证真正的双提交竞态。
- paste 并发测试取得真实红灯（同 token 进入 bulk 两次），现已通过服务级原子 claim + 首个 idempotency key 绑定修复；17 passed。
- import 幂等 prefix 变更测试红→绿；服务串行 claim token、首次 prefix 绑定，失败重试只能复用同 key；28 passed。
- WP10 六组定向测试合并 64 passed。
- Ruff 首轮报告 3 个纯排序问题，限定自动修复后全目标 `All checks passed`；修复后测试仍为 64 passed。
- __main__ fail-closed composition 测试已红：集中 helper 尚不存在；下一步仅在允许范围内增加 PB service wiring，不触碰 dirty `directus_service.py`。
- __main__ helper 已实现；首轮绿灯被测试 fixture 的 legacy manifest 校验提前阻断，非装配实现错误。
- fail-closed composition + logging 测试 3 passed。
- 最终 Ruff：All checks passed。
- 最终 WP10 定向 pytest：68 passed。
- 范围扫描确认 `backend.__main__` 只装配 `PocketBaseBulkMutationClient`、PB relation/lookup providers 与 QueryPagePort；命中的 Directus URL 仅存在于 WP16 待删除的 legacy 类定义。

## WP14 UI/RPC 纵切（子代理 repo_coupling_audit）

- 已完整读取 frontend-design、TDD、tests/mocking 与 planning-with-files 技能说明。
- 已确认公开 seam 与禁改文件边界，尚未修改 .NET/Vue 生产代码。
- session-catchup 两种解释器入口均不可用；已记录环境错误并直接读取共享规划文件恢复。
- 已读取 WP14、12.1、12.5；完成首次 .NET/renderer 文件与 Directus 耦合枚举。
- 已定位 renderer 可达旧命名仅为 realtime `directus.changed`；其余请求已经是产品命名，但 host gateway/registry 仍有 Directus 类型耦合。
- 已定位 host 迁移点为 `WorkspaceRequestDispatcher`、`RelationLookupRpcRegistry` 与新产品 gateway；旧 Directus physical 文件继续保留。
- 已盘点 CreateTableModal 与字段管理：确认 normalized scalar/formula/attachment 配置和公式/JSON/附件 UI 尚未实现。
- 已读取 sidecar frozen schema/realtime/formula contracts，确定 TS/.NET DTO 与 UI 请求形状的权威来源。
- realtime tracer bullet 红→绿：renderer 改用 frozen `data.changed`，relation Lookup/dashboard 消费 `tableId`，host allowlist 拒绝旧 `directus.changed`；Vue 8/8、.NET 1/1。
- .NET 产品 gateway/closed registry 红→绿：固定方法、取消、typed data.changed、credential guard，定向 8/8。
- formula debounce/cancel/stale coordinator 红→绿：3/3；web-grid typecheck 当前通过。
- WP14 renderer 全量回归：95 个文件、570 个测试全部通过；产品 UI 定向 9 个文件、60/60 通过，typecheck 通过。
- WP14 Desktop 产品/关系/Lookup/router 定向回归 37/37 通过；全量 235 项中 233 通过，唯一两个失败均为旧 `HostStartupOptions.ShouldAutoStartLocalDirectus` 资产探测基线，明确交由 WP16 连同 Directus 物理实现删除。
- WP14 已完成产品命名 RPC、`data.changed`、全字段 schema 创建/路径错误、JSON 无损编辑、公式预览协调、托管附件、realtime reconcile；历史恢复复用现有两阶段实现。
- JSON 编辑器 4/4、托管附件 cell 3/3、normalized field draft 5/5、CreateTable/store/validation 35/35、schema validate→apply service 7/7、realtime reconcile 3/3。
- WP14 合并定向结果：Vue 9 文件 60/60；.NET gateway/router/error map 30/30。
## 2026-07-24 续：WP11 客户端、产品 RPC 与中段门禁

- 新增 Python PocketBase SSE 客户端：严格 SSE/JSON/大小校验、`Last-Event-ID` 续传、10k 有界去重、指数退避、游标过期 reconcile 后重新订阅。
- `PocketBaseClient` 新增 `/events/reconcile` 产品调用；Python composition 将 `data.changed`/`task.changed` 原样通知 Host，revision gap 产生确定性的失效通知。
- 新增 closed `PocketBaseProductDataService`，renderer 可达的 schema/query/mutation/formula/file/realtime/relation/Lookup 只能映射到固定 sidecar 产品路由；递归拒绝凭据字段和非 JSON/超限参数。
- 产品 RPC/实时定向测试 14 项通过，Ruff 通过。
- Go 全量首次仅失败于 migration checksum 与 Windows TempDir 清理偶发；更新 manifest SHA 后迁移测试及两项隔离重跑通过；`go vet ./...` 发现并修复 audit 初始化失败路径的 job context 泄漏后通过。
- WP15 子代理已完成固定 sidecar 构建、发布资产/hash/build-info/license/SBOM、真实 sidecar smoke、开发/QA/package/LaunchPaths 第一轮；handoff、全局 Flow/Directus 清理及安装器外部验收留给 WP16。
- 已释放 WP15 子代理并转派 WP09 Junction/M2A/Lookup 完整实现与真实 PB 集成测试。
