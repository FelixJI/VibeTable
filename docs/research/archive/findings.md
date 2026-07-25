# Directus 字段能力审计发现

## 2026-07-24 最终结论

- 产品运行时已完全脱离旧后端；历史名称仅允许存在于 `docs/research/archive` 与 ADR。
- 数据真相源收口到 PocketBase sidecar：所有写入经过统一 MutationKernel，审计、
  幂等、revision/digest、公式物化与 durable realtime outbox 同事务提交。
- renderer 仅能访问显式产品 RPC 白名单，session secret、物理 provider 细节和错误
  details 不进入 WebView。
- 发布身份覆盖 Go、Python、.NET、renderer、契约、迁移与发布工具链关键输入；
  最终 gate 绑定 commit、sourceHash、package fingerprint 与 E2E 证据。
- UI 采用飞书式现代简约蓝灰语义系统，表格密度、状态层级、暗色对比、键盘交互和
  空态文案均纳入自动化与真实 WebView2 验收。

## 2026-07-24 实施发现

- WP-12 不需要把工作区文档写入 PocketBase；当前实现以 provider-neutral
  `WorkspaceIndexPort` 和独立 SQLite 设备状态库保存元数据，原有原生 Workspace
  继续拥有二进制、不可变 revision、restore 和 outbox。
- WP-12 主审验证了对 `pb_data` 的显式拒绝、metadata-only RPC、持久重开、
  幂等发布、immutable conflict、unlink 不删文档，以及现有 Workspace/宿主回归。
- WP-03 对 decimal 的能力声明是有意保守的：映射到 PocketBase Number，
  `Exact=false`；在 SQLite/Go/JSON/JS 精确往返证据出现前不宣称精确十进制。
- WP-03 的 View collection 暂以稳定 `schema.view.unsupported` 拒绝，因为当前
  normalized contract 尚无 query definition；这优于生成不可执行的伪 View。
- 独立审查确认 WP-02 第一版尚有停止取消卡死、迟到日志 secret 泄漏、无自动崩溃
  恢复、ready/health identity 未校验和 Ready/exit 竞态；这些不是测试命名问题，
  均已进入强制修复清单。
- PocketBase 路由会把 request body 包装为自动 rewind 的
  `RereadableReadCloser`；严格 JSON “第二次 Decode 必须 EOF”的通用写法会误读同一
  JSON。当前路由先限长读取到独立字节切片，再进行严格单值解码。
- WP-03 独立审查发现字段 rename 未保留 PB field ID 会静默丢数据（P0）；同时冻结
  v1 TableDefinition 与 Go wire model 漂移、部分约束 validate 后静默丢失、自关联和
  field metadata 复合唯一键存在缺口。WP-05 已暂停，必须先完成这些修复。
- 工作区 revision 投影不能只靠一次 RPC：正式 commit 在 ref CAS 成功后写入
  `.backup/outbox/revisions` metadata-only durable outbox；宿主按可达链发布，失败不
  阻断本地文档且保留 outbox，成功才清理。CAS 冲突 revision 不进入 outbox。
- Query snapshot 不是普通哈希：一旦被 selection/paste/export 当作权威凭据，客户端
  可见字段的 SHA-256 可自行重算。修复方向是每次 sidecar session 注入密钥的 HMAC，
  服务端 nonce 纳入签名，并在 session 重启后自然失效。
- Query page 的 rows、filtered count、total count、schema/data revision 必须来自同一
  PocketBase transaction；仅在查询前读取 revision 会产生“签发即过期”的混合快照。

## 已知事实

- 当前目录是主 worktree：`C:/Users/felji/PycharmProjects/vibetable`。
- 当前分支为 `main`，相对 `gitee/main` ahead 3。
- 工作区已有 3 个未提交修改：
  - `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`
  - `desktop/src/VibeTable.Desktop/Services/DirectusTableGateway.cs`
  - `desktop/tests/VibeTable.Desktop.Tests/DirectusTableGatewayTests.cs`
- 另有一个 detached worktree：`.worktrees/test-coverage-85`。
- 这些现有修改的归属和内容尚待只读检查；必须保留。
- 已创建并切换到 `codex/directus-field-audit`。
- 工作区中没有额外的 `AGENTS.md` 文件；遵循会话给出的“使用简体中文对话”。
- 3 个现有业务改动是在 Directus 打开数据库时先执行 identifier mapping reconciliation，并增加调用顺序测试；它们与本次字段审计相邻但不是本轮新增，必须保留。
- 仓库已经显式支持“读取数据库生成列作为公式字段”：`backend/adapters/directus/schema.py` 有 generated/computed column 说明。
- 真实 Directus 12 集成测试 `tests/backend/integration/test_plugin_directus_12.py` 会：
  1. 先通过 SQL `ALTER TABLE ... ADD COLUMN ... GENERATED ...` 创建数据库生成列；
  2. 刷新/补 Directus 字段元数据；
  3. 断言 `schema.is_generated == true`；
  4. 断言 VibeTable 将其映射为 `decimal` 且 `editable == false`；
  5. 验证显示和 CSV 导出。
- 因而“计算/公式字段完全不支持”不是事实；当前明显缺的是在 VibeTable 新建表流程中**创建**这种字段，读取/展示/导出链路已有覆盖。
- 既有产品设计明确提出“不把 Directus 完整重写成 VibeTable 原生管理后台”，这可能是字段创建 UI 精简的原始边界，需要与用户确认是否仍成立。
- 新建表 UI 的字段行只有 `name + type`，没有 nullable/required/default/unique/index/length/precision/scale/interface/options/readonly/hidden/note/validation/condition 等配置位。
- UI 的类型选项完全来自共享契约 `TABLE_FIELD_TYPES`；提交后只发送 `{ key, type }`。
- Python `FieldDefinition` 也只有 `key + type`，因此遗漏不是单纯前端没画控件，而是 wire contract 和服务模型有意精简。
- `TableAdminService._build_collection_body` 对每个用户字段固定：
  - `required: false`
  - `is_nullable: true`
  - 只写 translations
  - 不配置 Studio interface/options、默认值、唯一性、索引、长度和数值精度
- 新表固定使用 UUID 主键，并固定增加 status/sort/date_created/user_created/date_updated/user_updated；主键策略和系统字段同样不可选。
- 新建表的 runtime profile 把所有用户字段加入 create/update 白名单；若未来加入 generated/readonly 字段，这里必须同步排除，否则写入路径会与 schema 只读语义冲突。
- 关系不是“完全遗漏”：后端已有独立关系 schema 服务，覆盖 M2O、O2M、M2M、M2A，以及 file/files/translations 预设，并带 preview/apply 与 revision 校验；但它没有并入新建表弹窗。
- 当前 Directus 官方文档把字段分为：
  - 标准数据库列（数据类型 superset）；
  - `alias` 虚拟字段（无数据库列，常用于 O2M/M2M 和 presentation）；
  - Studio 的 interface/display/meta 配置，它们不是新的底层数据类型。
- 官方 glossary 的标准 superset 列出：string、text、boolean、binary、integer、bigInteger、float、decimal、timestamp、dateTime、date、time、json、csv、uuid、hash、alias。VibeTable 的 16 个独立存储类型与其相比只少 `alias`；契约测试还明确断言 `alias` 因缺少关系配置而应被拒绝。
- 当前官方 Data Model 文档还单列 6 个 geospatial 类型，因此项目的 16 个基础存储类型不能宣称覆盖全部 Directus 类型；是否可用取决于数据库。
- Directus Fields API 的**读取模型**明确暴露 `schema.is_generated` 与
  `schema.generation_expression`，但当前官方“创建字段”请求模型没有列出这两个属性。
  上一轮基于旧/宽泛 API 描述推断“创建请求也允许传入 generation_expression”不准确，
  现以 Directus 12.1.1 实际源码为准修正。
- 官方建议已有数据库列可由 Directus 自动发现再配置 Studio 元数据；这与仓库集成测试“先 SQL 建 generated column，再 PATCH Directus meta”一致。
- 官方资料：
  - https://docs.directus.io/reference/system/fields
  - https://docs.directus.io/app/data-model/fields
  - https://docs.directus.io/user-guide/overview/glossary
- 项目锁定 Directus `12.1.1`，兼容范围是 `>=12 <13`，本地运行数据库是 SQLite；因此 VibeTable 若提供公式创建，至少要先解决 SQLite generated column 语法和迁移边界，外部 Directus 的跨数据库兼容会更复杂。
- 网格面对 Directus 类型时会做有损归一化：
  - `bigInteger → integer`
  - `float/decimal → decimal`
  - `dateTime/timestamp → datetime`
  - 除 integer/decimal/boolean/date/datetime/time 外全部回退为 `text`
- `ColumnSchema` 虽声明支持 `json`，当前 `_map_data_type()` 从不会返回 `json`；因此 JSON 实际被当文本处理，这是一个明确的实现缺口/死分支。
- 网格 editor 仅有 boolean、number、date、text 四类；choices 永远传空数组。Directus 的 interface/options/display 配置（选择器、Markdown/WYSIWYG、代码、颜色、文件、标签、地图等）不会映射成 VibeTable 的原生编辑体验。
- 公式缺失原因已由真实集成测试注释直接确认：Directus 12 的 `FieldsService` **有意不创建**带 `generation_expression` 的列。仓库实现的支持流程是：
  1. Directus API 创建普通 collection；
  2. SQLite DDL 创建 `GENERATED ALWAYS AS (...) VIRTUAL` 列；
  3. 清 Directus schema cache；
  4. 用 Fields API 配置被发现字段的只读/显示元数据。
- 这意味着公式不是只需往 `TABLE_FIELD_TYPES` 增加 `"formula"` 即可；需要数据库方言级 DDL、表达式验证、安全模型、依赖字段、结果类型、循环依赖/变更策略和 schema cache 刷新。
- 已直接检查项目安装的官方 Directus 12.1.1 / `@directus/api` 37.0.1：
  - `FieldsService.addColumnToTable()` 只按 type、nullable、default、PK、unique、index
    构造 Knex 列，完全没有读取 `schema.generation_expression` 或 `schema.is_generated`。
  - `@directus/schema` 14.0.0 会针对 SQLite/Postgres/MySQL/MSSQL/Oracle 等数据库
    introspect generated/computed column，因此 Directus 的“支持”是识别和暴露已有生成列。
- Directus 官方 2024 Changelog 展示过 Calculated Field Interface 扩展；官方说明其值只在
  interface 中可见，不会出现在 API 响应。它与数据库 generated column 是两种不同能力，
  不能满足 VibeTable 的 API 读取、导出、筛选或排序需求。
- 对 VibeTable 当前的本地 SQLite 部署，如果目标是“公式值出现在 Directus API、
  VibeTable 网格与导出中”，确实需要绕过标准 FieldsService 创建数据库 generated column。
  不一定要调用 `sqlite3.exe`；可以由受控后端/Directus endpoint 在事务中执行 SQLite DDL。
- 前端确实有 `FieldManagerDrawer` 与 relation lookup store/service，因此关系能力不仅是后端孤立代码；仍需检查其 UI 范围是否覆盖全部关系创建配置。
- 关系管理 UI 已确认覆盖创建/更新/删除 M2O、O2M、M2M、M2A，nullable、on-delete、display template、file/files/translations 预设，以及 junction context fields；还支持 Lookup 虚拟列。因此关系字段不是主要遗漏，问题是它与建表向导割裂、字段命名约束也不同。
- 前端编辑器工厂本来支持 single-select 和 multi-select，但 .NET `EditorFor()` 从不生成这两类，且 `GetEditSchemaAsync()` 固定传空 choices；这属于“已有前端能力未接通 Directus meta.options”的明确遗漏。
- 全仓库未发现网格 schema 消费 Directus field `meta.interface` 或 `meta.options` 的代码；当前编辑体验确实按底层归一化类型决定，而不是按 Directus Studio 配置决定。
- Directus 官方当前完整类型表包含 16 个基础存储类型、6 个 geospatial 类型（Point、LineString、Polygon、MultiPoint、MultiLineString、MultiPolygon）和 alias。
- 没有找到 `TableAdminService` 对真实 Directus 执行“新建表后插入/读取”的集成测试；现有真实 Directus 集成主要覆盖公式读取与关系/Lookup，table-admin 测试主要是 mock transport 和契约测试。这意味着“16 种都能在 Directus 12.1.1 + SQLite 实际建成并可写”尚无一张端到端证据表。
- 新建 collection 的高级选项也被固定：UUID 主键、accountability=all、archive=status、sort=sort、versioning=false；singleton、版本控制、集合图标/说明/显示模板、主键策略等不可选。
- 系统 `status` 字段 payload 带 choices，但没有明确设置 select interface；而网格端又不读取 choices，因此 Directus Studio 与 VibeTable 两侧都存在状态字段未必得到预期下拉体验的风险，值得加真实集成/桌面验收。
- 定向前端测试通过：CreateTableModal、editorFactory、FieldManagerDrawer 共 3 个测试文件、44 个测试。
- Python 定向测试未能运行：仓库 `.venv` 缺 pytest-cov/pytest-asyncio，Codex 自带 Python 无 pytest；没有安装或改动依赖。

## 覆盖矩阵

| 能力 | 创建 | 读取/展示 | 编辑 | 结论 |
|---|---|---|---|---|
| 16 个基础存储类型 | 新建表契约允许 | 全部可读但有损归一化 | 仅 4 类通用 editor | 类型名覆盖，语义/UX 未完整覆盖 |
| 6 个地理类型 | 不支持 | 回退文本/无地图语义 | 不支持 | 明确缺失；本地 SQLite 默认也未必具备空间扩展 |
| alias presentation | 新建表不支持 | 可能按文本/关系归一化 | 无 presentation 编辑 | 除关系用途外缺失 |
| M2O/O2M/M2M/M2A | 独立字段管理器支持 | 支持 relation/lookup | 专用关系路径支持 | 能力已有，但与建表割裂 |
| file/files/translations | 关系预设支持 | 支持 | 通过关系管理 | 已覆盖主要关系预设 |
| generated/公式列 | 不支持创建 | 支持、强制只读、可导出 | 不可编辑（正确） | “可读不可建” |
| Lookup 虚拟列 | 支持关系路径/聚合 | 支持、只读 | 定义可管理 | 不是任意行内公式 |
| nullable/default/unique/index/length/precision/scale | 新建表不可配 | 部分读取 precision/scale | 部分校验 | 大面积缺失 |
| interface/options/display/validation/conditions | 新建表不可配 | 基本不消费 | choices 链路未接通 | 明确缺失 |
| 主键/集合级配置 | 固定策略 | 可读取外部表（要求单主键） | 不适用 | 产品化简，不是 Directus 全覆盖 |

## 待核实

- 字段类型定义及 UI 可选项。
- 新建表请求如何映射到 Directus 字段 schema/meta。
- 计算/公式能力在 Directus 中究竟属于原生存储字段、界面显示格式还是扩展。
- 关系、别名、文件、JSON、几何、生成列等能力是否遗漏。

## 2026-07-24 PocketBase 直接替换的模块边界

- `ITableRpcGateway` 是桌面端已经存在且最值得保留的产品 Interface；PocketBase Adapter 应直接实现深化后的该接口。`IDirectusRpcGateway` 是后端专属 Interface，应删除而不是让 PocketBase 模仿。
- `DirectusService` 和 `build_directus_service_from_environment()` 是浅而宽的总装配器，同时组装表、关系、文件、历史、协作、Insights、插件与工作区。替换时应建立 provider-neutral composition root，并按能力拆成深 Module，避免再造一个同样宽的 `PocketBaseService`。
- 可以保留 Interface/核心逻辑、仅替换 Implementation/Adapter 的产品 Module：
  - 表查询 AST、分页、筛选、排序与标准 CRUD；
  - `TableAdminService` 的建表意图与校验流程；
  - Relation/Lookup 的预览、幂等和生命周期状态机；
  - Paste/Import/Export 的预览令牌、归一化、任务和导出格式；
  - Grid State、设备设置、命令、快捷键；
  - History 的读取/预览恢复/应用恢复 contract；
  - Document Workspace 的 outbox/idempotency contract；
  - 插件包、隔离 Worker、mutation-plan/Host 校验模型；
  - 发布签名、哈希、SBOM 和更新状态。
- PocketBase 原生可等价承接的底层能力：Base/Auth/View collections，Bool/Number/Text/Email/URL/Editor/Date/Autodate/Select/File/Relation/JSON/GeoPoint，required 等字段约束与唯一索引，记录 CRUD、筛选/排序/分页、relation expand、显式启用的事务批处理、SSE create/update/delete、API rules、认证、文件字段、API logs。
- PocketBase 原生没有足够等价物、必须由 VibeTable 新增的 Module：
  - 统一 mutation transaction：记录写入、公式物化、审计、幂等键和 outbox 同事务；
  - 公式定义、解析、依赖图、循环检测、类型检查、试算、物化、批量重算；
  - `vibetable_audit_events` 与可验证的 before/after、变更集和两阶段恢复；
  - 软归档/恢复语义；
  - Comments/Mentions/Notifications（后续已根据单人桌面产品边界改为删除，不再建设）；
  - Presets/Dashboards/Panels、Content Versions 和安全聚合查询；
  - M2A、Lookup 元数据与查询执行；
  - Document Workspace 索引；
  - 插件触发器注册与代码调用 capability API。
- PocketBase View collection 是 SQL 派生的只读集合，可用于报表或只读 Lookup 原型，但不能作为可写表上的公式字段。
- `pb-audit` 只能作为 fork/参考；其管理员归因、同库可篡改、审计失败不阻断等边界不满足权威审计。权威审计必须纳入 VibeTable mutation transaction。
- 不保留 Directus 登录/首次创建管理员这一层产品流程。桌面端应启动本地 PocketBase sidecar，业务请求使用内部 scoped token；PocketBase Dashboard 仅作为可选管理入口。
- Directus 从未发布，采用 replace-then-delete：先让 PocketBase 通过相同的产品 contract/acceptance tests，再删除 Directus Adapter、扩展、启动器、资源和测试；不做迁移、双写、兼容开关或 Directus 导入。

## 2026-07-24 测试与脚本初步分类

- 整组删除并用 PocketBase/Go 对应测试替换：
  - `tests/backend/adapters/test_directus_*.py`
  - `tests/backend/application/test_directus_service.py`
  - `tests/backend/integration/test_plugin_directus_12.py`
  - `tests/backend/rpc/test_directus_errors.py`
  - `desktop/tests/VibeTable.Infrastructure.Tests/Directus/`
  - 四个 `directus/extensions/*` 的测试
  - `tests/test_extension_lockfiles.py`
  - `tests/test_plugin_extension_manifest.py`
- 保留业务断言、替换 fixture/mock/命名：table admin、paste、import/export、relation、lookup、history、insights、files、settings、document workspace、plugin platform/execution、identifier mapping 和 Vue 工作区测试。多人 collaboration 服务/契约删除；其中 activity/revert 的独有断言先并入 History。
- .NET 需要替换或去 Directus 化：
  - `DirectusTableGatewayTests.cs` -> `PocketBaseTableGatewayTests.cs`
  - `JsonRpcDirectusGatewayTests.cs` -> provider-neutral/PocketBase RPC gateway tests
  - `DirectusAdminAuthenticatorTests.cs` 删除或改为 sidecar bootstrap/token tests
  - `DirectusCollectionFilterTests.cs` -> PocketBase query compiler/filter tests
  - `FakeDirectusRpcGateway.cs` -> 按 table/schema/realtime seam 拆分的 fakes
  - WebView navigation tests 改成可选 PocketBase Dashboard origin。
- 需要改写的工具链测试：
  - `tests/test_release_tooling.py`：验证 PocketBase binary、迁移文件、SBOM、checksum，不再断言 local-directus/extensions。
  - `tests/test_dev.py`：验证 Go sidecar 构建/启动、端口和环境变量；Vue/插件 Worker 的 Node 测试单独保留。
  - `tests/test_next_gate.py`：Directus extension coverage stage 替换为 Go test/race/coverage 与 PocketBase integration stage。
  - `tests/test_architecture.py`：锁定 provider-neutral composition root，并禁止重新引入 Directus 依赖。
  - `tests/test_handoff_v2_protocol.py`：能力名和 `extensionHashes` 改为 sidecar/schema/migration hashes。
- 需要新增的高风险 acceptance/integration tests：
  - 受约束字段建表后真实写入/拒绝行为；
  - JSON 创建、编辑、筛选、导入导出 round-trip；
  - 公式解析/类型/循环/依赖重算/空值/错误传播/试算；
  - 同事务验证 record + formula + audit + idempotency + outbox；
  - PocketBase 重启、崩溃恢复、并发修改和 SSE 重连；
  - 文件上传/下载/删除与权限；
  - 审计读取、预览恢复、应用恢复和防篡改边界；
  - 打包产物在无网络环境首次启动，确保不再执行终端用户 `npm ci`。

## 2026-07-23 产品确认

- 用户确认字段约束必须补齐，至少不能继续让新建表只暴露 `name + type`。
- 用户希望能前置的验证尽量在前端完成，同时后端仍须做权威校验。
- JSON 不能再降级成普通文本，需要补齐类型映射、编辑、校验和读写链路。
- 公式字段需要继续确认部署边界：本地 SQLite 可由受控后端执行 generated-column DDL；远程 Directus 仅靠标准 Fields API 无法完成同一操作。

## 2026-07-24 双后端与公式方向

- 用户确认产品主要面向单机 SQLite，希望讨论 Directus 与 PocketBase 双后端可选架构。
- 用户不建议应用直接操作 SQLite generated-column DDL。
- 用户质疑前端乐观计算的必要性：后端计算延迟可能足够低，并担心前后端形成两个真相源。
- 初步设计原则：公式定义与服务端执行结果只能有一个权威语义；前端若保留计算，应只是可丢弃的预览，不参与最终一致性判断。
- PocketBase 同样没有可写 Base Collection 的原生公式字段；它的 View Collection 是独立只读查询模型。可写表公式仍需 Hook 物化。
- 当前待核实：适合同时运行于浏览器与 Directus/PocketBase 服务端的安全公式解析/执行依赖，以及现有 VibeTable 数据接口能否形成两个真实 Adapter 的 seam。
- 现有 `DirectusService` 不是后端无关模块：它直接依赖 `DirectusAuthBroker`、`DirectusClient`、Directus realtime、profile manifest，并负责装配 Files、Flows、History、Insights、Relations、Plugins 等大量 Directus 专属能力。
- `DirectusClient` 也不是一个很薄的通用 CRUD Adapter：其 interface 包含 Directus `/fields`、`/relations`、`/permissions/me`、自定义 endpoint capability negotiation、Directus filter 编译、archive 语义与乐观并发。
- 因而不应简单实现一个同名 `PocketBaseClient` 然后逐方法模仿。真正的双后端 seam 应位于 VibeTable 需要的“表数据/表 schema/表管理/实时订阅”等能力接口；Directus 专属 Files/Flows/Insights 等应通过 capability 明确暴露或在 PocketBase 下禁用，而不是伪造等价实现。
- 桌面层其实已经有一个真实 seam：`ITableRpcGateway`，而 `DirectusTableGateway` 是现有 Adapter。这个 seam 覆盖列表、schema、分页查询、单元格更新、插入、删除、history/restore、snapshot、grid state 和 paste。
- 但 `ITableRpcGateway` 的注释、revision/source 字符串、错误文案和部分语义仍写死 Directus；历史恢复与原子 paste 又依赖 Directus 修订表和 bulk-mutation endpoint。因此它是可深化的候选 seam，不是 PocketBase Adapter 可以无条件直接实现的完成品。
- `IDirectusRpcGateway` 则明显是 Directus 专属 interface，包含 auth、Directus schema、relation/lookup RPC 和订阅。PocketBase 不应该实现它；应新增后端无关的数据 interface 或让 `PocketBaseTableGateway` 直接满足深化后的 `ITableRpcGateway`。
- 现有 `TableQuery` 已经是后端无关的闭合查询 AST（filter/sort/offset/limit），非常适合作为双后端共享 interface；分别由 Directus 和 PocketBase query compiler 转译。
- `ColumnSchema` 目前名称与注释 Directus 化，但其 8 种归一化数据类型和编辑性/nullability/precision/scale 本身可作为共享 schema 投影；需要增加后端 capability/公式元数据，而不是复制两套 UI schema。
- HyperFormula 是完整的无头 spreadsheet parser/evaluator，支持浏览器和 Node、依赖图与约 400 个函数，但其模型以 sheet/cell address 为中心；开源许可为 GPLv3，VibeTable 当前 MIT 分发若不改许可通常需要商业许可，不适合作为默认依赖。
- Formula.js 的定位是 Excel 函数实现集合，不应把它误当成完整的公式字符串 parser、AST 和依赖分析器；需要另配 parser/evaluator。
- mathjs 提供 `parse → AST → compile/evaluate`，AST 可遍历 symbol 以做字段依赖分析，浏览器/Node 都可运行；官方安全指南要求对不可信表达式禁用 `import`、`createUnit`、`evaluate`、`parse`、`simplify`、`derivative`、`resolve` 等能力。因此它是候选基础，但必须构造白名单实例，不能直接暴露完整 mathjs。
- PocketBase JS Hook 运行在 Goja/JSVM，不是 Node 或浏览器：只支持 CJS `require()`，依赖 `window/fs/fetch/buffer` 等运行时 API 的 npm 包可能无法工作，且 Goja 并非完整 ECMAScript 兼容。任何“前端与两后端复用同一 npm 包”的设想都必须先做 JSVM 兼容性原型；否则应共享公式语言/测试向量，而非强求共享二进制实现。
- 当前 `@formulajs/formulajs` 4.6.0 是 MIT，提供浏览器、ESM 与 CJS 构建；其包描述仍是“Excel formula functions 的 JavaScript 实现”。它适合作为白名单函数实现来源，但不负责 VibeTable 字段引用语法、AST、循环依赖和权限。
- `fast-formula-parser` 是 MIT 的 Excel parser/evaluator，但当前 npm 版本 1.0.19 已约 6 年未发布，依赖较旧 Chevrotain 7；可作为原型对照，不宜未经维护性审查直接定为长期核心。
- PocketBase 的 Records 端点为 `/api/collections/{collection}/records[/{id}]`，支持事务型 `/api/batch`；Directus 是 `/items/{collection}[/{id}]`，本项目还依赖自定义 bulk endpoint。CRUD 路径差异只是表面，真正差异包括查询语法、schema/collection 管理、archive、revision/history、permission、文件和 realtime 语义。
- PocketBase 普通字段（JSON 除外）默认非空并使用各类型零值回退；Directus 可直接表达 nullable。这会影响共享 `ColumnSchema`、空值公式语义、筛选和导入，必须由 capability/Adapter 明确处理，不能简单做端点字符串替换。
- Directus Items 查询原生支持 `fields/limit/offset/meta/sort/filter/search` 并可返回 `filter_count/total_count`；现有 `compile_directus_query()` 已把 VibeTable AST 映射到 `_contains/_eq/_between/_in/...`。PocketBase 使用单个 filter 字符串和不同操作符，且没有等价的全字段 `search` 参数；需要独立 `compile_pocketbase_query()`，不能复用 Directus 参数树。
- 两边 CRUD 都容易映射：Directus `/items/{collection}` 对应 PocketBase `/api/collections/{collection}/records`；PocketBase 还有事务 `/api/batch`。但 Directus realtime 是 WebSocket 订阅协议，PocketBase realtime 是 SSE；已有桌面 notification sink 可以隐藏传输差异。
- 当前 Python RPC 注册逻辑以 `build_directus_service_from_environment()` 为总开关，几乎所有表、关系、导入导出、协作、文件、Flow、插件、history 和 workspace 功能都挂在 `directus_service` 下。双后端需要拆出 provider-neutral composition root，而不是再加一个平行的大型 `if pocketbase_service` 复制分支。
- `jsep` 是 MIT、浏览器/服务端可用的轻量表达式 parser，可输出 AST 并允许自定义/移除运算符；它不负责安全求值和 spreadsheet 函数，适合与我们自己的白名单 AST evaluator 组合。
- 若把 PocketBase 作为可编译 Go framework，而非只用预编译二进制+JSVM，则 CEL-Go（Apache-2.0）或 Expr（MIT）可提供安全、非图灵完备/保证终止、类型检查的服务端表达式。但 Directus/前端没有天然完全等价的同实现，因此会增加跨后端语义一致性成本。
- 当前最值得原型验证的跨后端方向是：把一个极小、无 `eval`、无 Node/browser API 的 parser+AST evaluator 打成单文件 CJS；Directus Node Hook 和 PocketBase Goja Hook 使用同一 bundle。前端不执行公式，而是调用后端 preview/validate endpoint。
- 公式“单一真相源”不要求前端和后端都安装同一大型 spreadsheet 引擎；最干净的本地方案是前端完全不求值：
  - 源字段提交后等待服务端保存响应，服务端返回包含公式结果的整行；
  - 公式编辑/未提交草稿若要即时预览，则调用后端 `formula.preview/validate`，明确标记为不落库预览；
  - 服务端 Hook/route 是唯一 evaluator，持久化结果是唯一可查询真值。
- 因为产品是 loopback 单机，行内标量公式求值通常远小于网络/序列化开销；没有必要为了几十毫秒再维护一个前端 evaluator。可以只乐观显示用户刚编辑的源单元格，公式单元格显示 pending，收到服务端整行后替换。
- 推荐定义 provider-neutral Formula interface：`validate(definition,schema)`、`preview(definition,draftRow)`、`saveDefinition(...)`、`recalculate(scope)`；Directus Adapter 映射自定义 endpoint+Hook，PocketBase Adapter 映射 custom route+record Hook。
- 公式定义建议放入 VibeTable 自有 registry（而非绑定 Directus field meta），至少保存 `collectionKey/fieldKey/expression/canonicalAst/resultType/dependencies/engineVersion/status`。这样可随 Directus/PocketBase 导出迁移，且 PocketBase 无需伪造 Directus metadata。
- 双后端的共同核心应限制为表/字段/行 CRUD、查询 AST、批量、关系基础、realtime、formula、导入导出；History、Flows、Insights、Comments、GraphQL、M2A 等作为可选 capability。前端按 capability 隐藏或降级，禁止 PocketBase Adapter 伪造 Directus 功能。
- 一个 SQLite 文件不能在 Directus 与 PocketBase 间直接切换共用：两者系统表、字段模型和迁移机制不同。“双后端选择”应是项目级 provider 选择，切换需受控导出/导入和公式 registry 迁移。
- 用户最终确认所有业务写入都必须经过 VibeTable。由此可以把公式求值、写入校验、审计事件生成和幂等性统一放在 provider-neutral mutation 模块；Directus/PocketBase 只负责持久化 Adapter。标准后端 API 可读，但直接写不属于受支持路径。
- 待核验的新假设：PocketBase 可由 `pb-audit` 提供足够的记录级审计；Activepieces CE 可替代 Directus Flows，成为两个后端共同的工作流引擎。

## 2026-07-24 审计与工作流补充发现

- Context7 已定位 Activepieces 官方文档库 `/activepieces/activepieces`；后续用官方文档核对自托管、Webhook/API 与 CE/EE 边界。
- `pb-audit` 的初步资料显示它同时记录请求尝试和提交成功事件，包含前后值、用户与请求元数据；需要进一步确认许可证、PocketBase 版本约束、管理员身份归因及日志防篡改边界。
- 架构硬约束：Activepieces 只能消费 VibeTable 的提交后事件，并通过 VibeTable mutation API 回写；不能直接写 Directus/PocketBase，否则公式、验证、审计和幂等会被绕过。
- Activepieces 官方安装文档将单容器 PGLite + 内存队列定位为最快部署方式；生产拓扑使用 PostgreSQL + Redis，应用和 Worker 也有独立资源要求。因此它不是“复用业务 SQLite 的一个小库”，而是额外的 Node 服务、工作流数据库与队列。
- Activepieces 仓库是混合许可证：Community Edition 为 MIT；`packages/ee` 的 Enterprise License 仅允许无订阅的开发/测试，生产使用、复制分发和出售受商业许可限制。
- 官方嵌入文档存在 JS SDK/JWT/iframe 方案，但历史官方公告与 `packages/ee` 许可证共同表明嵌入、白标和平台多租户能力不能默认视为 MIT CE 能力；必须按企业功能处理，除非获得 Activepieces 的明确书面许可。
- `pb-audit` 当前文档标注 MIT、版本 2.0.0；它自动建 `audit_logs`、避免递归、支持过滤与保留策略。但审计失败明确“不阻断业务操作”，管理员操作的 `user` 为空，日志又与业务数据处于同一 PocketBase 数据库，因此它适合产品操作历史/诊断，不是不可抵赖的合规审计。
- Activepieces 根许可证精确排除了两个目录：`packages/ee/` 与 `packages/server/api/src/app/ee`；只有这些范围以外的项目自有代码才按 MIT Expat 许可。分发构建产物时不能只看仓库首页的“MIT”，必须生成依赖/源码归属清单并证明未打入 EE 代码。
- Activepieces 官方 API 文档明确：用于管理多个项目的 API Key 目前只在 Platform 和 Enterprise edition 提供。因此 CE 不能按“VibeTable 自动创建/管理工作流项目和 Flow”的方案设计；CE 可行的最小集成是由用户在 Activepieces UI 建好并发布 Flow，VibeTable 调用其 Webhook，Flow 再调用 VibeTable mutation API。
- Activepieces 官方 Embedding 文档明确标注为 paid editions。VibeTable 若要在自己的界面中用 JWT/SDK/iframe 自动配置与白标嵌入，必须购买/谈判商业许可；不能把它作为 MIT CE 默认能力。
- `pb-audit` 的维护成熟度偏低：pkg.go.dev 显示没有 tagged/stable Go module 版本，当前为 2026-03 的伪版本；GitHub 规模仅约 4 stars、0 forks。README 内的 `Version = 2.0.0` 不等于发布了 Go v2 tag，采用时应固定 commit 并由 VibeTable 自己维护兼容性测试。
- Activepieces 的 CE Webhook 运行能力与“平台事件流”要区分：普通 Flow 的 Webhook trigger 是核心工作流能力；平台级 Event Streaming（审计事件转发）官方明确要求 Enterprise + Audit Logs。本项目只需由 VibeTable 自己投递 mutation/outbox 事件，不依赖后者。
- Activepieces 当前官方价格页把 SSO、RBAC、Audit Logs 等列在付费 Ultimate，并把 CE 描述为 MIT、self-hosted、core features only；因此 CE 能替代 Directus Flows 的基础触发/分支/重试/HTTP 集成，但不能承诺补齐 Directus 的治理和平台管理能力。

## 2026-07-24 去 Directus 目标调整

- 用户最新目标不是让两个后端长期保持完全等价，而是逐步摆脱 Directus：先实现 VibeTable 自有公式引擎和 mutation 内核，再引入 PocketBase；Directus 只作为过渡迁移来源。
- 仓库已有可复用的插件内核：隔离 Node Worker、capability 白名单、私有存储、包校验、插件审计、任务运行时，以及“插件返回 mutation plan，Host 校验后再写入”的机制。它比让任意插件直接拿 PocketBase 凭据调用 API 更符合“所有写入经过 VibeTable”。
- 当前插件内核的主要 Directus 耦合点也已明确：composition root 从 `directus_service.plugin_service` 取服务；mutation plan 校验引用 Directus profile；执行器固定调用 `/vibetable-bulk-mutation/apply`；Flow binding、兼容声明和字段命名包含 Directus。应替换这些 Adapter/命名，不应重写整个插件系统。
- 去掉跨 Directus 兼容要求后，公式引擎选择显著简化：采用自有 PocketBase Go 二进制并在唯一写入端点中使用 `cel-go`。前端只调用同一后端的 preview/validate，不需要 `@bufbuild/cel` 作为第二套 evaluator。
- 自有 PocketBase 二进制也是引入 `pb-audit` 的前提，因为它是 Go library，不是可直接安装到官方预编译 PocketBase 的独立扩展。
- PocketBase 官方支持 `RunInTransaction(fn)`，并要求事务内始终使用 `txApp`；因此可把幂等检查、源字段写入、CEL 结果字段、统一审计记录和 outbox 事件放进同一事务。事务内不能执行外部 API 或耗时工作，插件/通知由提交后的 outbox 异步触发。
- 官方 success hooks 会延迟到事务提交后执行，事务失败则不触发；这适合唤醒 outbox dispatcher，但不适合把“必须与业务记录原子存在”的审计记录只放在 after-success hook 中。
- 如果插件代码需要直接调用外部 API，应新增 Host 代理的 `network.request` capability（域名/方法/超时/响应大小/凭据白名单），而不是把原生网络或 PocketBase 管理 token 暴露给 Worker。

## 2026-07-24 无迁移直接替换

- 用户确认 Directus 后端版本从未实际发布，不存在需要保留的线上 Directus 数据或兼容承诺。
- 因此删除此前“Directus 只读迁移来源”的假设：不开发迁移器、不双写、不保留运行时 provider selector。
- 实施清单应采用 replace-then-delete：先让 PocketBase Adapter 通过同一外部 seam 的契约测试，再删除 Directus implementation、专属扩展、安装脚本和测试夹具。
- Directus 耦合横跨 Python Adapter/Application、.NET 首启/进程监督/登录、Directus 扩展、插件 Flow、契约 fixtures 和大量测试，不只是替换 REST URL。
- 明确可整组删除的候选包括 `backend/adapters/directus/`、`backend/infrastructure/directus_*`、桌面 `Infrastructure/Directus/`、Directus 首启/登录窗口、`scripts/local_directus/`、文件迁移脚本及真实 Directus 集成测试；删除时机必须在 PocketBase 对应纵切和外部契约测试通过之后。
- `DirectusService` 当前是一个浅而宽的总装配对象：除 auth/CRUD/realtime 外，还持有 paste、import/export、collaboration/history、document workspace、table admin、insights、lookup、relation、file tools、settings/commands 和 plugin modules。去 Directus 时应把这些 VibeTable 自有 modules 从该对象移到 provider-neutral composition root，而不是做 `PocketBaseService` 同构复制。
- 现有 RPC 名称也分两类：`directus.*` 是应更名/替换的后端泄漏；`table_admin.*`、`lookup.*`、`relation.*`、`data.*`、`file.*`、`plugin.*` 等是可保留的产品 interface，只需替换内部 Adapter。
- 仓库同时打包 Python、.NET、Vue/Node、四个 Directus extensions 和 local Directus npm 项目；PocketBase 方案将新增 Go module/build，同时应删除 Directus extensions 与 local Directus package，净减少一个 Node 服务和四套扩展构建。
- RPC 清单显示产品 interface 已按领域拆得较好：`table.*`、`table_admin.*`、`lookup.*`、`relation.*`、`data.*`、`history.*`、`workspace.*`、`plugin.*` 可继续作为测试 seam；应把 `directus.read/create/update/schema/subscribe/...` 提升为 `table.*`/`session.*` 等 VibeTable 命名。
- Directus 专属高级能力包括 Activity/Revisions/Revert、Comments/Mentions/Notifications、Presets、Content Versions、Dashboards/Panels、Files、Flows；PocketBase 不会自动提供这些同名产品功能，需要由 VibeTable 自有 collections/modules 实现或明确删减。
- `build_directus_service_from_environment()` 是当前最大耦合点：它一次性创建所有产品 modules，并把同一 Directus client/auth/profile/transport 传遍 import/export、history、files、insights、workspace 和 plugins。PocketBase 替换首先应建立 provider-neutral composition root，再逐个接 Adapter。
- PocketBase 当前原生 collection/field 足以覆盖基础表模型：Base/Auth/View collections；Bool、Number、Text、Email、URL、Editor、Date、Autodate、Select、File、Relation、JSON、GeoPoint；字段 required/max 等约束与唯一索引可由 Go collection schema 创建。
- PocketBase 原生覆盖分页过滤排序、relation expand、事务 batch、SSE record create/update/delete realtime、API rules、认证、文件字段和 API logs，因此基础 CRUD、关系引用、文件、权限、实时和批量可以等价实现。
- PocketBase 原生 API logs 不是数据修订历史；官方资料未提供 Directus 等价的 Activity/Revisions/Revert、Comments/Mentions/Notifications、Presets/Dashboards/Panels、Content Versions 或通用 aggregate API。其中审计/恢复、Insights 等若保留必须由 VibeTable 实现；Comments/Mentions/Notifications 因单人产品边界直接删除，不能标成“PB 原生等价”。
- 产品边界进一步确认：VibeTable 是单人桌面应用，因此 Comments/Mentions/Notifications 和多人 activity feed 不属于缺口，而是应删除的 Directus 遗留范围。审计/历史用于个人追溯与恢复，任务进度用于后台操作反馈，outbox 用于可靠内部派发；三者都不等同于协作通知。
- PocketBase 0.39.9 原生 File field 已覆盖单/多文件、大小/MIME、上传/替换/追加/删除、protected token、图片缩略图、Range、本地/S3 storage 和含本地上传文件的 `pb_data` 备份。基础记录附件无需自研。
- 当前 `FileWorkspaceView` 的“云端资源附件”按钮是 disabled，`WorkspaceRequestDispatcher` 也明确只支持工作区文档；它是未接通的 Directus Files 记录附件占位，不是已经存在的云端文件系统。
- 单机 PocketBase 下附件默认仍在本机 `pb_data/storage`，建议删除独立“云端资源”tab，将记录附件改名“托管附件”并放入具体记录的 File 字段/详情；本地工作区文档继续承担受管版本文件。
- PB File 字段属于记录作用域，并非 Directus 风格的全局资产库。只有确认跨记录复用、全局搜索、文件夹/标签、独立生命周期、版本、丰富元数据或去重/引用计数需求时，才新增 `vibetable_files` Module。
- 文件二进制不在 SQLite 中；PB 用数据库事务加文件补偿维持一致性，而非跨存储 ACID。必须测试上传失败清理、旧文件清理失败、进程中断、孤儿文件与备份恢复一致性。
- PocketBase 0.39.9 官方 `core.FileField` 配置项只有字段名、hidden/presentable/help、maxSize、maxSelect、mimeTypes、thumbs、protected、required；没有 per-collection 本地目录或 per-field 存储后端配置。上传 Implementation 固定调用 `record.BaseFilesPath() + "/" + filename`，文件系统后端由全局 `app.NewFilesystem()` 提供。
- 因此要区分两种说法：PocketBase 会按 collection/record 自动形成隔离的对象键/目录命名空间，这是原生行为；“给某个表选择任意本地目录或不同 S3 后端”不是标准 FileField 原生选项，若产品需要必须由 VibeTable 自定义 AttachmentStorage Module/route 实现。
- PocketBase View collection 是 SQL query 派生且只读，适合只读报表/Lookup 原型，不适合作为可写表的计算字段；公式仍需 mutation transaction 内物化。
- `.NET` 已有真实的 table seam：`ITableRpcGateway` 覆盖 workspace open、表列表、分页/查询、编辑 schema、单元格/行 CRUD、历史恢复、snapshot、grid state 和 paste。`PocketBaseTableGateway` 应直接满足这个 interface；`IDirectusRpcGateway` 不应保留或由 PocketBase 模仿。
- `MainWindow.xaml.cs` 同时承担 Directus 进程监督、首启、管理员创建、登录/凭据、Python 启动、RPC 装配、Admin WebView、schema reconcile 和 table gateway lazy binding。PocketBase 替换不能只改类型名；应把本地数据进程生命周期提取成深 module，再让 MainWindow 只消费启动状态。
- PocketBase 自带管理 Dashboard，可在需要时替换 Directus Studio WebView；但 VibeTable 单机默认写入应使用内部 capability token/自定义 route，不必保留“业务用户先登录本地 Directus”这一层 UI。
- 发布/开发脚本目前把 Directus 当一等制品：`dev.py` 构建全部扩展并注入 `VIBETABLE_DIRECTUS_*`；`build_next.py` 打包 local-directus、扩展和 portable Node/npm；`versioning.py` 把每个扩展写入发布 manifest。PocketBase 替换需新增 Go build/固定版本/校验和/sidecar layout，并删除这些 Directus stage。
- Portable Node 仍可能因 Vue 构建和隔离插件 Worker 而保留，但不再需要为 Directus 在终端用户首次启动执行 `npm ci`；可显著缩小安装和联网首启逻辑。是否还能删除完整 Node runtime 取决于插件 Worker 是否改用内嵌运行时，当前阶段不应一起删除。
- 测试应按三类处理：Directus Adapter/扩展/启动测试删除并由 PocketBase 对应测试替换；产品 module tests 去 Directus 化后保留；跨语言 contract/UI tests 主要改 fixture 和文案，不应重写业务断言。
# 2026-07-24 PocketBase 实施发现

- 实施计划实际路径为 `docs/plans/2026-07-24-pocketbase-implementation-plan.md`。
- 工作包具有强依赖链，不能把全部 16 个包同时写入；适合先并行做基线/拆解和
  M0 的互斥部分，再按纵切里程碑逐批推进。
- Codex bundled Python 路径可用：
  `C:\Users\felji\.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe`。
- 现有 `task_plan.md`、`findings.md`、`progress.md` 是上一轮研究留下的未跟踪制品；
  本轮在文件顶部追加实施状态，保留历史研究内容。
- 目标架构明确为 Vue → .NET Host → Python Broker → Go PocketBase sidecar；Go 是
  schema/query/mutation/formula/audit/attachment/realtime 的权威实现。
- 产品协议必须从 `directus.*` 收敛为 provider-neutral 命名空间，且
  `ITableRpcGateway` 应保留、`IDirectusRpcGateway` 应删除。
- normalized schema 至少覆盖 scalar/relation/lookup/formula/attachment/system，
  约束为版本化 discriminated union；SchemaCatalog 负责 validate/apply。
- MutationKernel 的事务边界必须同时覆盖业务记录、公式物化、审计、幂等键和 outbox。
- 公式使用 `cel-v1`，F1 是同记录依赖，F2 是 relation/Lookup 反向依赖；附件复用
  PocketBase File field，元数据表不拥有第二份二进制生命周期。
- 仓库尚无 `go.mod`，说明 WP-01 需要从零引入 `sidecar/` Go module。
- 当前测试资产已覆盖大量 Directus adapter/application/RPC 以及关系、Lookup、
  import/export、history、plugin、workspace 等产品契约，可作为等价替换回归基础。
- 本机发现 .NET、Node/npm，但没有 Go；WP-01 之后的真实 sidecar 构建与测试需要
  在工作区提供可复现的 Go 工具链。
- 三个现有用户改动的语义是“打开数据库时，先 reconcile identifier mappings，
  再列出 collections”，并移除 MainWindow 中稍后的重复 reconcile；替换时应把
  “先 reconciliation 后发布表列表”的顺序契约迁入 provider-neutral 启动/打开路径。
- 最终验收明确要求 real sidecar integration，而非仅 mock；因此不能以 Python fake
  或仅合同 fixture 代替 Go binary 验收。
- Vue 基线完整通过：89 个测试文件、545 个测试。
- .NET 基线无法启动：`global.json` 要求 SDK 10.0.100，本机未安装任何 .NET SDK。
- Python 基线未进入收集：当前 `.venv` 缺 `pytest-asyncio`，严格 warning 将未知
  `asyncio_mode` 升级为错误；同时已有 `.pytest_cache` ACL 不可写。
- GitHub 官方 `v0.39.9/go.mod` 已核验存在，要求 Go 1.25.0；计划版本并非无效标签。
- 官方 Go 1.26.4 便携工具链首次解压被超时中断；完整副本现已安装到
  `.tools/go-full/go`（标准库 55 个一级目录），并由 `.gitignore` 排除。
- Microsoft 官方 .NET SDK 10.0.100 已安装到 `.tools/dotnet`，与 `global.json` 完全匹配。
- .NET 基线在沙箱外完整通过：Contracts 29、Workspace 74、Desktop 215、
  Infrastructure 103，共 421 个测试。
- Python 在修复环境并使用仓库内 basetemp 后：727 passed、4 skipped、2 failed；
  两个失败均为已知环境类失败：WebView2 renderer 在当前执行环境退出、插件 esbuild
  遍历到沙箱拒绝目录。其余 backend/contract/tooling 测试均通过。
- B02 生命周期实现具有清晰 Process/HTTP seam，secret 只经环境变量传给 sidecar，
  LocalDataStatus 不暴露 endpoint/secret；但当前 StopAsync 直接强杀进程，仍需增加
  优雅 shutdown 首选路径，KillProcessTree 仅作为超时兜底。
- B01 单元/编译门槛未捕获真实启动缺陷：ready 记录写在 `OnServe event.Next()` 之后，
  黑盒启动时 stdout 无握手且进程保持运行；M0 必须以真实二进制测试补洞。
# 2026-07-24 实施审查补充

- Workspace revision 已经落盘但 ref CAS 冲突时，仍必须留下 durable publish intent；否则
  本地历史和 provider-neutral metadata index 会永久分叉。
- metadata index 的 `main_head` 只能由明确标记为主线、且从当前 indexed head 连续可达的
  批次推进；分支 revision 可以被索引，但不能隐式切换主头。
- 生产环境的文档关联授权必须由真实 schema/catalog 和 record existence resolver 决定，
  不能把“名字符合正则”视为存在或授权。
- schema meta 缺失不等于全新数据库；若任何 workspace 表已存在，必须先核验完整结构，
  再决定升级或返回稳定的不兼容错误。
## WP10 Python 纵切（子代理 repo_coupling_audit）

- TDD seam：现有 application service/RPC 公共契约；新增 PocketBase 产品 HTTP 边界。
- 规划文件由根任务共享，本工作包只追加命名小节，不覆盖已有内容。
- 当前 paste 明确绑定 `DirectusClient`、`BulkMutationClient` 与 Directus 字段策略；import/relation provider 也直接调用 Directus extension。
- 现有 export 已有 authoritative lookup provider seam，但普通分页仍走 Directus client。
- 当前 shell 的 `python` 不在 PATH；后续需从工作区虚拟环境或依赖清单定位解释器。
- session-catchup 因解释器不在 PATH 未运行，已通过现有计划/进度文件和工作树手工恢复上下文。
- 工作区存在 `.venv/Scripts/python.exe`，后续测试使用绝对路径。
- 实施计划的核心验收明确要求：JSON 导入/导出不变形、paste/import 无半提交、1k 行原子提交或完整回滚、所有写入统一进入 MutationKernel。
- 现有 relation import 测试直接断言 `/vibetable-bulk-mutation/relation-import` 与 Directus schema proof，需迁移为产品 mutation port 断言而保留业务语义。
- Sidecar 产品 API 已提供：
  - `POST /api/vibetable/v1/mutations/preview`
  - `POST /api/vibetable/v1/mutations/apply`
  - `POST /api/vibetable/v1/query`（`page`/`readRows`/`aggregate`）
  - relation/lookup 产品路由。
- frozen mutation request 必须完整带 `contractVersion=1.0`、request/idempotency/table/schema、operations、actor、expectedRevision、expectedDigest；Python 只负责编排和 wire 翻译。
- 旧 Import result 文档写明“分块、非整文件原子”，与 WP10 的“中途失败无半提交”冲突；需要通过一个 frozen mutation request 提交全部行，或明确补偿/事务 seam，不能继续逐块直接提交。
- 当前 Python 装配完全由 `build_directus_service_from_environment` 提供 paste/import/export；WP10 应保持 RPC 方法名不变，仅替换这些服务的 data-plane 依赖，Directus 其余模块留到 WP16。
- import 的现实现逐 chunk 捕获异常后继续，确实会半提交；PB MutationKernel 支持多 operation 单请求，应把全部有效行编译成一次 apply，`chunkSize` 只保留为进度/UI 分组兼容参数。
- relation 显式解析的 preview 语义已较完整（relationId + unique matchField + exact 0/1/多匹配）；需要将 schema/query/apply 的实现从 Directus URL/extension proof 换成产品 Schema/Query/Mutation port。
- Host 当前只将 session secret 传给 sidecar 子进程；Python 尚无 sidecar 地址/secret 注入。WP10 可提供环境构造 seam，但真正进程装配仍依赖 WP12/Host 把 loopback 地址与 secret 安全传入 Python。
- 旧测试明确接受 import 第一 chunk 成功、第二 chunk 失败的半提交；该断言必须反转为单 frozen request 失败时零提交。
- `ExportService` 可最小收窄为一个 `QueryPagePort` 协议；保留现有文件写出和模板逻辑，普通导出不再依赖 `DirectusClient.read_items`。
- sidecar 鉴权头固定为 `X-VibeTable-Session`。
- relation 产品 API 的 search target 可承担 import 的显式匹配查询；当前请求只有 relationId/query/offset/limit，返回 recordId/label，并不支持任意 unique matchField 的结构化精确筛选。为保证精确 matchField 语义，WP10 adapter 应优先通过 QueryPort 对目标表构造 `eq` 过滤，并由 normalized schema 提供目标表/字段。
- `PocketBaseClient` 应解析非 2xx ProductError，保留 code/path/details/retryable，禁止把 session secret 放 URL 或错误消息。
- MutationKernel 的 `expectedRevision`/`expectedDigest` 是单记录全局 guard；批量 paste/import 不能携带逐行 guard。当前 WP10 可继续 apply 前复核 preview 行并将实际写入统一送入 MutationKernel，但检查与提交之间仍有 TOCTOU，需后续扩展 frozen mutation contract 的 per-operation guard 才能彻底消除。
- Sidecar `maxOperations`/body limit 会限制 1k 导入能否单请求提交；import 应在提交前让 adapter/preview 明确返回产品上限错误，不能重新退回会半提交的多请求 chunk。
- 现有 Directus composition 可按 `VIBETABLE_SIDECAR_URL` + `VIBETABLE_SIDECAR_SESSION_SECRET` 条件替换 WP10 服务依赖；Host 目前尚未提供前者。
- 旧 non-relation upsert 实现把无 `target_row_key` 的 `PastePlanRow(kind=update)` 最终编译为 create，属于潜在静默错误且没有测试。PocketBase 路径必须先用 QueryPort 按唯一键解析 recordId，再生成 update；在该解析 seam 完成前应 fail closed。
- Import 原子 apply 可一次提交最多 1000 operations/1 MiB，正好覆盖计划中的 1k 行门槛；超正文仍由 MutationKernel 明确拒绝并保持零提交。
- PB Lookup 产品路由返回标准 QueryPort Page；`lookups/describe` 提供 lookupId→physicalName/fieldId 映射，因此导出 provider 无需读取当前网格页或 Directus extension。
- PB relation search 路由只按通用 label 搜索，不适合作为任意 unique matchField 的精确解析；relation import 应组合 `relations/describe`、目标 `schema.describe` 与目标表 QueryPort `eq`。
- `directus_service.py` 是既有 dirty/非 WP10 所有权；已撤回本代理装配改动并向根代理发送精确 PocketBase wiring。新 ExportService 构造已是 QueryPagePort-only，因此根装配必须同步后才可启动 data.export。
- 现有 paste/import token 的“检查后调用”没有原子 claim，并发 apply 可在 consumed 标记前重复提交不同 idempotency key；WP10 仍需绑定首个 idempotency key并串行化 token apply。
- 上述 token 竞态已修复：paste/import apply 进入服务级串行区，首次 idempotency key/prefix 被 token 绑定；pending/失败只能同 key 重试。
- `backend.__main__` 已集中 fail-closed wiring：有合法 loopback URL + 256-bit session secret 时启用 PB ports；缺一项即禁用 paste/import/export legacy data paths。

### WP10 剩余风险

- Host/WP12 仍须把 sidecar 实际 loopback URL 和同一 session secret 安全注入 Python；当前缺失时 RPC 按设计不可用。
- MutationKernel frozen v1 只有单记录全局 guard，批量 paste 没有逐 operation revision/digest guard，仍存在 apply 前复核与事务提交间的 TOCTOU。
- create-if-missing relation 与 upsert 需要跨表原子合同；当前 PB provider 明确返回 `relation_cross_table_transaction_unavailable`，不会回退 Directus 直写。
- legacy `BulkMutationClient` / `DirectusRelationImportProvider` 代码仍保留供 WP16 删除；生产 `backend.__main__` 不再注册这些实例。

## WP14 UI/RPC 纵切（子代理 repo_coupling_audit）

- 公开测试接缝已由父任务验收范围确认：.NET 产品 gateway/renderer allowlist；Vue 产品 RPC client、状态协调器和可见组件行为。
- `MainWindow.xaml.cs`、`DirectusTableGateway.cs`、`DirectusTableGatewayTests.cs` 属于明确禁改的 dirty 文件；本工作包只新增产品实现并给根代理 composition 合并说明。
- 设计方向沿用现有桌面数据工具的高密度、工业化界面语言，不做独立主题重写；视觉改动优先服务字段约束、路径错误和异步状态的可读性。
- planning session-catchup 因系统没有 PATH Python/py 未执行；已通过共享计划文件恢复当前工作包上下文。
- 12.1 的硬性单测项是：.NET gateway contract/取消、Web message allowlist、secret 不入 renderer；Vue 全字段/约束、field-path error、JSON、formula debounce/cancel/stale、attachment、history restore、删除协作入口。
- 12.5 与 WP14 直接相关的产品纵切包括全字段建表、同路径 schema error、JSON 无损、公式预览/循环、relation 联动、附件重启/恢复、stale conflict、SSE reconcile。
- 现有 .NET 产品表格主抽象是 `ITableRpcGateway`，但注释仍是 Directus；同时存在大而旧的 `IDirectusRpcGateway`/`JsonRpcDirectusGateway`。
- renderer 仍可达 `directus.changed`（dashboard/relation Lookup）且 WebView allowlist 明确允许；这必须迁移为 `data.changed`，旧 physical 类可保留到 WP16。
- 搜索结果过大被截断；后续按 `bridge.request/on`、具体 service 和 component 测试窄读，避免把 legacy physical 文件与真正 renderer 可达路径混为一谈。
- renderer 的请求名称已经大多是产品用例（schema/relation/lookup/history/table/dashboard）；生产源码唯一字面量 `directus.*` 事件是 `directus.changed`，由 relation Lookup 与 dashboard 两处订阅。
- `WebMessageRouter` 的 host notification allowlist 也只需把 `directus.changed` 收敛到 `data.changed`；它本身没有通用 `directus/rpc` 入站通道。
- web-grid 使用 Vitest/Vue Test Utils，Node 目标 24；.NET Desktop 测试为 MSTest net10.0-windows。
- 当前工作树含多个代理和用户的未提交改动；WP14 将只触碰声明所有权内文件，尤其不会格式化或顺手改动 G3/Directus dirty 文件。
- `WorkspaceRequestDispatcher` 与 `RelationLookupRpcRegistry` 是 host 路由迁移的实际接缝：前者持有 `IDirectusRpcGateway`，后者把产品请求名绑定到该旧接口。
- 旧 `IDirectusRpcGateway` 混合登录/用户/物理 CRUD 与产品 schema、history、relation、Lookup，不适合作为 PB 产品边界；WP14 应新增较小 `IProductDataRpcGateway`，只承载 renderer 可达产品用例。
- Python 代码搜索没有发现一处集中 RPC 注册表；产品方法名需要以现有 `JsonRpcDirectusGateway` 的 relation/history 映射和既有 Python dispatcher 为准逐项核对。
- 当前 CreateTableModal 仅发送 `{key,type}`，支持的是 Directus 时代 16 个 storage type；没有 normalized `kind/dataType/constraints/editor/formula/attachmentPolicy`，也没有服务端 path error 状态。
- 现有字段管理抽屉只覆盖 relation/Lookup，且这部分产品 RPC 与预览流程较完整；适合保留并与新 scalar/formula/attachment schema editor 并列，而不是重写。
- 计划定义 normalized field kind 固定为 scalar/relation/lookup/formula/attachment/system，公共约束和类型映射远超当前 `TABLE_FIELD_TYPES`，必须升级 TS 契约和 store 表单。
- renderer 目前不存在公式编辑服务或托管附件组件；WP14 必须新增独立、可测的小组件/协调器，而不是把异步逻辑塞进现有大抽屉。
- sidecar 的 schema 真实 wire 为 `schemaapi.Change { definition, expectedRevision }`；验证返回 normalized definition + capabilities，apply 返回下一 revision 的 definition。
- frozen table fixture 可直接作为 TS/.NET 产品 DTO 的独立真值：它覆盖 scalar、relation、lookup、formula、attachment、system 和主要约束。
- realtime 产品事件固定为 `data.changed`，含 eventId/sequence/schemaRevision/dataRevision/changeSetId/tableId/recordIds/operation；旧 `uid/collection/event/data/invalidateQuery` 形状不能继续冒充。
- 现有 Python broker 仍注册 `directus.*` CRUD，但产品 relation/Lookup/history/paste 已独立命名。新增 .NET 产品 gateway 不应对未实现的 Python RPC 做静默回退；composition 需等根代理/后续工作包提供产品 broker 端。
- 新 .NET 产品 seam 已固定 9 个 renderer 直接能力和 14 个 relation/Lookup 能力；payload guard 拒绝 sessionSecret/access/refresh/password/provider token，接口不存在 generic invoke。
- HostBridge 自己还维护一份 TS 双向白名单，单改 contracts/WebMessageRouter 不够；已同步加入产品 RPC 与 `data.changed`，并由 typecheck 护栏验证。
- 公式 UI 的取消采用 AbortController + generation 双保险：边界请求可协作取消，忽略 abort 的旧请求也无法覆盖新结果。
- 现有历史抽屉已经覆盖查询失败/不可用/重试、restore token 过期、无可恢复字段、诊断和显式确认，不需要另造第二套历史 UI。
- normalized CreateTable 现已按 frozen field contract 生成 scalar/relation/lookup/formula/attachment/system，并用精确 `fields[i].…` 路径合并本地和服务端错误。
- 产品错误从 JSON-RPC 到 renderer 时只投影 code/path/message/retryable，主动丢弃 details，防止 provider secret 通过错误详情进入 WebView。
- WP14 最终 Desktop 回归证明产品纵切自身为绿：定向 37/37；全量仅剩两个旧 Directus 自动启动资产探测测试，二者不是 PocketBase 产品路径，应由 WP16 随物理 Directus 启动代码和测试一起删除。
- 根 composition 必须显式创建 `JsonRpcProductDataGateway`、调用 `WorkspaceRequestDispatcher.SetProductDataGateway`，将其 typed `DataChanged` 事件以 `data.changed` 发布，并在 shutdown 前解除订阅/释放；不得把 sidecar URL/session secret 注入 renderer DTO。

## WP09 Junction/M2A 审查发现

- 当前 v1 schema 的 relation 只有 `targetTableId/cardinality/deletePolicy/junctionTableId`，
  尚不能严格表达 junction source/target 字段或 M2A allowed target tables。
- `sidecar/internal/relation`、`lookup`、`mutation/relation.go` 已有 direct relation 第一纵切；
  本轮应在这些公共 seam 上增量扩展，不能另建旁路写入。
- 旧 Web relations DTO 已能表达 `m2a`、junction context 和 discriminator，但不是 frozen
  产品 schema 的权威来源；新增字段必须以 `contracts/v1/contracts.schema.json` 与 Go strict
  decode 为准并保持 optional。
