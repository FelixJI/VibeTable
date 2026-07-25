# PocketBase 直接替换 Directus：功能、测试与脚本矩阵

日期：2026-07-24

## 前提与结论

Directus 后端版本从未实际发布，因此本方案不包含：

- Directus 数据迁移或导入；
- Directus/PocketBase 双写；
- 双后端选择器或兼容开关；
- Directus 只读兼容层；
- 对外部 Directus 实例的版本兼容。

目标是让 PocketBase 通过 VibeTable 的产品契约和验收测试，然后直接删除 Directus
Implementation、扩展、运行时、资源、测试和打包逻辑。

状态说明：

- `≈ 原生等价`：PocketBase 已提供主要底层能力，只需 Adapter/契约映射。
- `↺ 复用产品模块`：VibeTable 已有可保留的 Interface 或核心逻辑，替换 Directus Implementation。
- `＋ 必须新增`：PocketBase 没有等价能力，需要 VibeTable 自有 Module。
- `− 删除`：未发布的 Directus 专属能力或流程不再保留。

## 一、功能等价与缺口矩阵

| 领域 | 当前能力/入口 | PocketBase 基础 | 状态 | 替换或新增工作 |
|---|---|---|---|---|
| 本地数据进程生命周期 | `.NET` 启动 Directus、首次安装 npm、监控、端口和健康状态 | 可嵌入自定义 Go binary，作为本地 sidecar 启动 | `≈ 原生等价` | 新建深 Module `LocalDataService`，负责启动、健康检查、崩溃恢复和退出；`MainWindow` 只消费启动状态 |
| 首次运行与登录 | 创建 Directus 管理员、登录窗口、token store | PocketBase 有 superuser/auth collection | `− 删除/简化` | 删除业务用户的本地 Directus 登录流程；桌面 Host 使用内部 scoped token；PocketBase Dashboard 仅作可选管理入口 |
| 表/Collection CRUD | `table_admin.*`、`DirectusService`、`TableAdminService` | Base/View/Auth collection API | `≈ 原生等价` | 保留 `table_admin.*` 产品 Interface，重写 collection schema Adapter；禁止把 PB schema 原样暴露给 UI |
| 建表字段模型 | 现在只有 `key + Directus FieldType` | PB 有 Bool/Number/Text/Email/URL/Editor/Date/Autodate/Select/File/Relation/JSON/GeoPoint | `＋ 必须补齐产品契约` | 建立 VibeTable normalized field schema，替换 Directus 类型枚举；字段类型、约束、编辑器与存储类型分层 |
| 字段约束 | 当前 UI 不可配置 required/default/unique/index/length/precision/scale 等 | PB 原生覆盖 required、部分 min/max/格式和 unique index | `≈ + ＋` | 原生可下推的约束写入 PB schema/index；其余由 VibeTable schema metadata 和 mutation validator 统一执行 |
| 前置验证 | 当前仅校验名称和重复字段 | PB 服务端可校验最终 schema/record | `＋ 必须新增` | 前端做同步可判定校验；`schema.validate/preview` 做权威远程校验；保存时服务端重复验证，返回稳定错误码和字段路径 |
| JSON 字段 | 契约列出了 JSON，但网格映射会退化成 text | PB 有原生 JSON field | `≈ + ＋` | 补 JSON editor、格式/JSON Schema 校验、查询操作符、粘贴、导入导出和 round-trip；不得继续降级为普通文本 |
| 计算/公式字段 | Directus 路径仅能读数据库生成列，不能由建表 UI 创建 | PB 没有可写 Base collection 的原生公式字段；View collection 只读 | `＋ 必须新增` | 用 `cel-go` 建受限公式引擎；结果物化到普通 PB 字段并标为只读；定义/依赖/版本存 VibeTable metadata |
| 公式实时预览 | 尚无 | 自定义 Go route 可复用同一 evaluator | `＋ 必须新增` | `formula.preview/validate` 不落库；前端不运行第二套权威 evaluator，不形成两个真相源 |
| 公式保存与重算 | 尚无 | 自定义 route + `RunInTransaction` | `＋ 必须新增` | create/update 时同事务计算；公式变更触发可恢复 backfill；加入循环、类型、空值和错误传播策略 |
| 普通记录 CRUD | `table.*`、`data.*`、`ITableRpcGateway` | Records API | `≈ 原生等价` | 保留并深化 `ITableRpcGateway`；新增 `PocketBaseTableGateway`；删除 `IDirectusRpcGateway` |
| 查询/筛选/排序/分页 | 已有 provider-neutral `TableQuery` AST，当前编译为 Directus filter | PB 支持 filter/sort/page/perPage/expand | `↺ 复用产品模块` | 保留 AST，新写 PB query compiler；导出/大表查询补稳定分页或 keyset 策略 |
| 批量写入、Paste、Import | Directus bulk-mutation 扩展提供原子/幂等写入 | PB batch 可用，但产品仍需统一写入口 | `↺ + ＋` | 保留预览 token、归一化和计划逻辑；在自定义 mutation route 中实现原子批量、幂等、公式、审计和 outbox |
| Export | Directus 分页读取，支持 CSV/XLSX 和任务 | PB Records query | `↺ 复用产品模块` | 保留编码/格式/任务逻辑，替换查询 Adapter；公式结果和 JSON 必须按产品语义导出 |
| 乐观并发 | Directus revision/updated timestamp 参与校验 | PB record update 与自定义 route 可做 revision/digest | `＋ 必须明确` | mutation contract 统一 `expectedRevision`/digest；冲突返回稳定错误并携带最新记录 |
| Realtime | Directus realtime subscribe/change notification | PB SSE 订阅 create/update/delete | `≈ 原生等价` | 新建 provider-neutral `RealtimePort`；事件名由 `directus.changed` 改为 `data.changed`，实现重连和去重 |
| 权限 | Directus permissions/profile grant | PB API rules/auth | `≈ + ＋` | PB rules 作为底层隔离；VibeTable capability/scoped token 作为产品写权限；superuser 不发给 UI/插件 |
| M2O/O2M/M2M | 已有关系 schema/data 服务和完整 UI | PB Relation field、反向查询、多值 relation | `≈ + ↺` | 保留 relation Interface、预览/幂等状态机和 UI；重写 schema/query/mutation Adapter |
| M2A | 当前用 Directus M2A/junction 语义 | PB 无直接等价多集合 relation | `＋ 必须新增` | 用 VibeTable junction + target collection/type metadata 实现；保持现有 contract 或在 normalized schema 中显式建模 |
| Lookup | VibeTable 已有虚拟 Lookup、编译器和 Directus lookup-query 扩展 | PB 无通用等价；View collection 仅能覆盖部分只读场景 | `↺ + ＋` | 保留 Lookup compiler/contract；元数据存内部 collection，查询由安全 Lookup endpoint 执行 |
| 记录附件 | Directus Files + 当前 disabled 的“云端资源附件”占位 | PB File field、单/多文件、约束、protected token、缩略图、本地/S3 存储和备份 | `≈ 原生等价` | 单机模式改称“托管附件”，放到记录 File 字段/详情中；删除独立云端 tab。只有确认需要全局资产库/跨记录复用时才新增 `vibetable_files` Module |
| 工作区文档 | 本地受管文件、版本、恢复、打开/预览/重新定位 | 与 PB File field 生命周期不同 | `↺ 直接保留` | 继续由本地 Workspace Module 管理，不上传为记录附件，不与 `pb_data/storage` 混为一个真相源 |
| 软归档/恢复 | Directus `status`、archive/unarchive | PB 无通用 archive/revision 语义 | `＋ 必须新增` | normalized schema 统一 `status` 或 `deleted_at`；所有查询默认排除归档；恢复走 mutation route |
| Activity/Revisions/Revert | Directus system collections；现有 History/Collaboration service 消费 | PB API logs 不是数据修订历史 | `＋ 必须新增` | 新建 `vibetable_audit_events`、变更集和两阶段 restore Module；保留 History 产品 contract，替换底层实现 |
| Comments/Mentions/Notifications | Directus system collections；面向多人协作 | PB 无原生等价 | `− 删除` | 产品是单人桌面应用，不建设评论、@提及、通知收件箱或协作活动流；内部 outbox 和任务进度不属于此功能 |
| Grid State/个人预设 | 当前 grid state 已存本地 SQLite；Directus presets 另有使用 | 无需依赖 PB | `↺ 直接保留` | 保留本地 Module；schema reconciliation 改用 normalized schema revision |
| Shared settings | Directus internal collection | PB Base collection | `≈ + ↺` | 保留设置 contract，迁到 VibeTable internal collection；device settings JSON 继续本地保存 |
| View/只读派生表 | Directus collection/view | PB View collection | `≈ 原生等价` | 可用于只读报表；必须在 contract 中标记 read-only，不能冒充可写公式字段 |
| Dashboards/Panels/Presets | Directus Insights system collections | PB 无原生等价 | `＋ 必须新增` | 复用现有前端模型和 `InsightsService` Interface；元数据存 internal collections，查询走安全 aggregate endpoint |
| Content Versions | Directus Content Versions | PB 无原生等价 | `＋ 必须新增或延期` | 若保留，基于 audit snapshot/版本 collection 实现；不得把 PB backup 当内容版本 |
| 聚合查询 | Directus aggregate/query 能力 | PB 标准 Records API 不等价于通用安全聚合 | `＋ 必须新增` | 编译受限 aggregate AST，自定义 endpoint 执行；禁止透传原始 SQL/任意 filter JSON |
| 文档工作区索引 | Directus workspace-index 扩展 | PB internal collections + custom route | `↺ + ＋` | 保留 workspace/outbox/idempotency contract；把索引、发布和版本 metadata 移到 PB/VibeTable Module |
| 插件包与隔离执行 | 现有 `.vtplugin`、Node Worker、capability broker、mutation plan | 与 PB 无关 | `↺ 直接保留` | 保留隔离 Worker 和 Host 校验；`DataProfilePort`/`MutationPort` 替换 Directus profile/bulk endpoint |
| Directus Flows/工作流 | Flow binding、flow/hybrid mode | PB 无等价内置工作流 | `− 首版删除` | 按已确认方向只保留代码/插件调用 VibeTable capability API；以后再做 trigger registry + outbox + retry |
| 插件网络访问 | Host 代理/Flow 混合路径 | 与 PB 无关 | `↺ + 加固` | 保留 `NetworkRequestPort`，限制域名、方法、超时、大小、凭据；插件永远拿不到 PB superuser token |
| Identifier mapping | display name -> Directus physical identifier registry | PB 仍需要安全 collection/field name | `↺ 复用并去 Directus 化` | origin 改为 `vibetable/pocketbase`；删除未发布迁移所需的 Directus orphan/import 分支 |
| 发布与兼容性 | Directus >=12<13、扩展哈希、schema snapshot | 自定义 PB binary + migrations | `↺ + 改写` | 保留签名/SBOM/update；改为 sidecar version、Go build info、migration/schema hash 和健康预检 |
| 管理后台 WebView | Directus Studio | PocketBase Dashboard | `≈ 可选等价` | 仅保留受控管理入口；导航白名单改为 PB origin，不把 Dashboard 当产品建表 UI |
| API 日志/可观测性 | Directus logs/activity 混用 | PB API logs | `≈ 仅运维日志` | 可用于诊断和指标，不能替代 `vibetable_audit_events` |

## 二、推荐新增的核心 Module 与 Interface

| Module | 对外 Interface | Implementation 责任 | 深度/边界 |
|---|---|---|---|
| `SchemaCatalog` | normalized tables/fields/relations/formulas + revision | 把产品 schema 映射到 PB collections、indexes 和 VibeTable metadata | UI、Python、Go 共享稳定语义，不传播 PB/Directus 字段结构 |
| `MutationKernel` | `apply/previewBatch`，返回 receipt | 约束、并发、公式、业务写入、审计、幂等和 outbox 同事务 | 所有 UI/导入/插件写入的唯一 Seam；标准 PB 写 API 不作为受支持入口 |
| `FormulaEngine` | `validate/preview/compile/dependencies/backfill` | 受限 CEL、类型系统、依赖 DAG、循环检测、版本和重算 | 前端只显示预览结果，不拥有权威求值 Implementation |
| `AuditHistory` | list change sets、preview restore、apply restore | append-only audit、before/after、关系 delta、恢复计划 | PB API logs 只作运维日志；审计失败必须使业务 mutation 回滚 |
| `RealtimePort` | subscribe/unsubscribe + normalized events | PB SSE、重连、去重、schema/data event 分类 | 产品事件不得带 `directus.*` 名称 |
| `LocalDataService` | start/status/stop/admin URL | sidecar 生命周期、端口/token、日志、崩溃恢复、备份协调 | 从 `MainWindow.xaml.cs` 抽离，避免 UI 窗口承担进程管理 |
| `QueryPort` | provider-neutral query/aggregate AST | PB filter compiler、relation expand、稳定分页、安全聚合 | 表读取与 Insights 共用可审计的查询边界 |
| `PluginCapabilityHost` | data profile、mutation、network、interaction | 复用 Worker/包/权限；调用上述产品 Interface | 删除 Flow-first 和 Directus bulk bridge，保留 mutation-plan/Host 验证 |

## 三、测试修改矩阵

| 测试组 | 动作 | 当前文件/目录 | 目标与新增断言 |
|---|---|---|---|
| Python Directus Adapter | `删除并替换` | `tests/backend/adapters/test_directus_*.py`、`test_g3_schema_capability.py` | 新建 PB query/schema/realtime/auth/transport Adapter tests；`test_coerce.py` 中纯类型逻辑可改名复用 |
| Python 总装配 | `删除并替换` | `tests/backend/application/test_directus_service.py` | 测 provider-neutral composition root，断言产品 Module 不 import Directus/PB 具体类型 |
| Flow binding | `删除` | `tests/backend/application/test_flow_binding_manager.py` | 首版不保留 Flow；未来 trigger registry 另建独立测试 |
| 真实后端集成 | `替换` | `tests/backend/integration/test_plugin_directus_12.py` | 启动真实自定义 PB binary，覆盖 schema、CRUD、SSE、formula、audit、file、relation 和重启 |
| RPC 错误映射 | `改写` | `tests/backend/rpc/test_directus_errors.py` | 改为 normalized mutation/schema/query error 到 JSON-RPC error 的映射 |
| 产品 application tests | `保留业务断言，换 Adapter` | `test_table_admin_service.py`、paste/import/export/relation/lookup/history/insights/files/settings/document workspace/plugin/release/identifier mapping | mock `SchemaCatalog/MutationPort/QueryPort`，删除 `/items`、`/fields`、Directus token/profile 等实现断言 |
| 多人协作契约与服务 | `删除；历史断言先并入 History` | `backend/application/collaboration_service.py`、`backend/contracts/collaboration.py`、`test_collaboration_service.py`、C2 collaboration fixture/Python/.NET contracts | 删除 comments/mentions/notifications RPC；其中 activity/revert 若有 History 尚未覆盖的断言，先迁入 `AuditHistory`/History tests |
| Table admin contracts | `扩展` | `tests/backend/contracts/test_table_admin_contract.py`、contract fixture | normalized 类型；required/default/unique/index/length/precision/scale/choices/JSON schema/formula definition；互斥和跨字段校验 |
| .NET Directus infrastructure | `删除并替换` | `desktop/tests/VibeTable.Infrastructure.Tests/Directus/` | 新建 `LocalDataService`、PB launch options、health、token、process output、crash recovery、offline first-run tests |
| .NET Table gateway | `改写` | `DirectusTableGatewayTests.cs`、`DirectusCollectionFilterTests.cs` | `PocketBaseTableGatewayTests.cs`、PB query compiler；保留分页/编辑/并发/history/paste 产品断言 |
| .NET RPC gateway | `改写/拆分` | `JsonRpcDirectusGatewayTests.cs`、`FakeDirectusRpcGateway.cs` | table/schema/realtime/admin 等小 Interface fakes；删除巨型 Directus fake |
| .NET 管理认证 | `删除/替换` | `DirectusAdminAuthenticatorTests.cs` | sidecar bootstrap、scoped token、可选 Dashboard session 和导航隔离 |
| Vue 建表 | `扩展` | `CreateTableModal.test.ts`、`tableAdminStore.test.ts`、`tableAdminService.test.ts`、`tableAdminValidation.test.ts` | 约束表单、条件可见性、公式/JSON 编辑、同步错误、服务端 field-path 错误、只读结果字段 |
| Vue 网格/查询 | `扩展` | editorFactory、createGrid、queryAdapter、paste tests | JSON editor/formatter、formula readonly、choices、精度/长度/required、PB normalized realtime event |
| Vue 关系/Lookup | `保留并换 fixture` | FieldManager、relationLookup store/service/grid tests | PB relation + VibeTable M2A/Lookup；保持 preview/revision/idempotency 断言 |
| Vue History/Insights | `保留并换来源` | history stores/drawer、dashboard、workspace tests | 不再期待 Directus IDs/system collections；改为 VibeTable audit/change-set/internal metadata |
| Vue Plugin | `保留并去 Flow` | plugin store/service/surface/action tests | 只保留 manual code action + capability/mutation/network ports；删除 flow/hybrid 断言 |
| 四个 Directus 扩展 | `删除并由 Go tests 替换` | `directus/extensions/*/src/__tests__/` 共 15 个测试文件 | mutation/idempotency/transaction、Lookup、插件 confirm/progress、workspace index 迁到 Go package/unit/integration tests |
| Directus extension 工具 | `删除` | `tests/test_extension_lockfiles.py`、`tests/test_plugin_extension_manifest.py` | 改由 Go module checksum、vendor/build reproducibility 检查承担 |
| 文档迁移 | `删除` | `tests/test_document_workspace_migration.py` | 同时删除对应脚本；这不是本地 schema migration test，而是明确的 Directus Files 数据迁移 |
| Release tooling | `大幅改写` | `tests/test_release_tooling.py`、E2/E3 release fixture/contracts | 验证 sidecar binary、version/build info、migration hash、SBOM、checksum、离线首启；删除 Directus compatibility |
| Dev/QA gate | `改写` | `tests/test_dev.py`、`tests/test_next_gate.py` | 添加 Go build/test/race/coverage、PB integration stage；删除四扩展循环和 Directus flags |
| Architecture guard | `改写` | `tests/test_architecture.py` | 禁止生产代码出现 `backend.adapters.directus`、`IDirectusRpcGateway`、`DirectusService`；锁定写入必须经过 `MutationKernel` |
| Handoff/能力清单 | `重写` | `tests/test_handoff_v2_protocol.py`、`qa/handoff_dependencies.json`、`directus-b4-contracts.json` | capability 改为 `vibetable.schema/mutation/formula/audit/realtime/...`；`extensionHashes` 改为 sidecar/schema/migration hashes |
| E2E smoke | `改写并扩展` | `tests/e2e/test_next_readonly_smoke.py` 及启动 smoke | 新增真实建表、写入、formula/JSON/约束、SSE、重启保留和无网络首次启动 |

### 必须新增的验收场景

| 风险面 | 最小验收 |
|---|---|
| 约束 | 每种约束既有前端预检，也有绕过 UI 后的服务端拒绝；错误码、字段路径和值脱敏稳定 |
| JSON | object/array/scalar/null round-trip；非法 JSON/JSON Schema 拒绝；粘贴、筛选、导入、导出不丢类型 |
| 公式 | parser/type/null/rounding/date/string；依赖顺序、循环、错误传播；公式字段不可伪造写入 |
| 原子性 | record、formula result、audit、idempotency 和 outbox 全部成功或全部回滚 |
| 重算 | 公式变更 backfill 可暂停/恢复/重试；旧 engine version 可识别；并发写入不混入旧结果 |
| 审计/恢复 | create/update/archive/restore/batch/file/relation 都产生 change set；preview/apply revision 校验 |
| 安全写入口 | UI、import、paste、plugin 都走同一 mutation route；PB 标准写 API 被拒绝或不对客户端暴露 |
| 进程与发布 | sidecar 崩溃恢复、端口冲突、数据库锁、SSE 重连、升级失败回滚、无网络首次启动 |
| 文件 | 单/多文件约束、protected token、缩略图、Range、上传失败补偿、孤儿清理、记录与本地 storage 的备份恢复一致性 |

## 四、脚本、资源、CI 与打包适配矩阵

| 文件/目录 | 动作 | PocketBase 替代 |
|---|---|---|
| `scripts/dev.py` | `改写` | 构建 web + Python + Go sidecar；flags 改为 sidecar 路径/端口/数据目录；删除 `--directus-url`、`--no-directus-auto` 和 `VIBETABLE_DIRECTUS_*` |
| `scripts/build_next.py` | `大幅改写` | 新增 Go binary 构建/复制、migration/schema resources、build info 和 checksum；删除 Directus extensions/local-directus stage |
| `scripts/versioning.py` | `改写` | 版本矩阵从 `directusExtensions` 改为 `pocketBaseSidecar`、schema/migration version；保留 Python/.NET/Web 一致性 |
| `scripts/release.py` | `小幅改写` | 继续调用 release build，但签名/SBOM/manifest 验证加入 Go binary，移除 Directus compatibility |
| `scripts/extension_manifest.py` | `删除` | 不再有 Directus extension manifest；Go packages 由 `go.mod/go.sum` 和 build metadata 管理 |
| `scripts/local_directus/` | `删除` | 发行包直接携带版本固定的自定义 PB binary，不在终端用户机器执行 `npm ci` |
| `scripts/migrate_directus_files_to_workspace.py` | `删除` | Directus 未发布，不提供迁移工具 |
| `scripts/vibetable_plugin.py` | `改写一处运行时解析` | esbuild 不再从 `scripts/local_directus/node_modules` 回退；使用独立的 build-only Node/esbuild 或已打包 Worker runtime |
| `qa/next.py` | `改写` | `directus-test/typecheck/build` 改为 Go fmt/vet/test/race/build 和真实 PB integration |
| `qa/package_check.py` | `改写` | 检查 sidecar executable、build info、migration hash、schema assets、SBOM/checksum |
| `qa/handoff.py` | `改写` | 删除 Directus schema snapshot/extension hashes，输出 sidecar/schema/migration/capability hashes |
| `qa/handoff_dependencies.json` | `重建` | 删除所有 `directus.*`、扩展路径、未发布迁移记录和外部 Directus 环境阻塞；按产品 capability 重列证据 |
| `qa/README.md`、`scripts/DEV.md` | `改写` | 文档改为 Go/PB sidecar 的开发、调试、数据目录、端口、恢复和发布流程 |
| `directus/` | `最终整目录删除` | 其中 bulk mutation、Lookup、plugin bridge、workspace index 的仍需能力先迁入 Go/VibeTable Modules |
| `backend/adapters/directus/` | `最终整目录删除` | `PocketBaseTableGateway`、PB query/schema/realtime Adapter；纯 coerce 可提升到 normalized schema Module |
| `backend/infrastructure/directus_*` | `删除` | plugin interaction/trigger 使用 provider-neutral ports |
| `backend/application/directus_service.py` | `删除` | provider-neutral composition root，按 schema/query/mutation/history/plugin 等能力装配 |
| `desktop/src/VibeTable.Infrastructure/Directus/` | `替换` | `LocalDataService/PocketBase` infrastructure；`NodeRuntime` 只有插件 Worker 确有运行时需要时才独立保留 |
| Directus 三个窗口及认证/Gateway | `删除/替换` | sidecar startup status + 可选 PB Dashboard；产品数据网关实现 provider-neutral Interface |
| `LaunchPaths.cs`、`.csproj`、安装资源 | `改写` | 解析和复制 PB sidecar、data/migrations/logs；移除 local-directus、extensions、blueprints/capabilities |
| 发布 manifest | `重建字段` | `pocketBaseSidecarVersion`、`goBuildInfo`、`schemaVersion`、`migrationHash`、`binarySha256` |

Node 的处理需要分开：

- Directus 运行时 Node 和终端用户首次 `npm ci` 可以删除；
- Vue 构建仍需要 build-time Node；
- 现有隔离插件 Worker 如果继续使用 Node，则应成为独立、版本固定的插件运行时，不能再借用
  `local_directus/node_modules`。

## 五、建议实施顺序

1. 冻结 normalized schema、field constraints、formula definition、mutation receipt、audit event
   和 capability error contracts。
2. 建自定义 PB Go binary 与 `LocalDataService`，先通过启动、健康、重启、离线打包测试。
3. 完成 SchemaCatalog、表 CRUD、JSON 和约束；让建表 UI/contract tests 先切换。
4. 完成 MutationKernel + FormulaEngine + AuditHistory 的最小纵切。
5. 切换普通表查询/写入、Paste/Import/Export、SSE 和 common relations/files。
6. 迁入 M2A/Lookup、History restore、Workspace index 和 Insights；删除多人协作 RPC/契约。
7. 插件切换到 `DataProfilePort/MutationPort`，删除 Flow binding。
8. 让 release/E2E/architecture gates 全部转绿后，一次性删除 Directus 目录、运行时、窗口、
   fixtures、tests 和打包字段。

这里的关键验证顺序是 `replace-then-delete`，不是保留两个 Adapter。Directus 专属代码只有在
对应 PocketBase/VibeTable 产品契约已经被相同或更强的测试覆盖后才删除，但不会进入任何发布
产物或迁移工具。
