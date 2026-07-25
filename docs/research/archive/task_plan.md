# PocketBase 等价替换与适配清单

## 2026-07-24 最终收口

- [x] WP-00～WP-16 全部实施完成，生产数据面统一为本地 PocketBase sidecar。
- [x] 旧后端运行时、扩展、认证/启动 UI、协作/Flow 入口及相关生产依赖已物理删除。
- [x] normalized schema、查询、统一 mutation、公式、关系/Lookup、附件、历史恢复、
  实时 outbox、Python/.NET/renderer 纵切与发布工具链均已完成。
- [x] 建表 UI 覆盖完整字段约束、typed enum、复合/唯一索引、附件缩略图策略；
  表格与管理界面按飞书式蓝灰语义层级、紧凑密度和键盘/无障碍要求完成重构。
- [x] 独立 Standards、Spec 与 UI 审查发现均已逐项修复；最终发布门禁、真实桌面
  WebView2 场景、故障注入、升级与包校验结果记录于验收证据文档。

## 2026-07-24 历史实施状态（保留当时快照）

以下未勾选项是实施过程中的历史快照，不代表顶部“最终收口”的当前状态。
最终状态和可重复证据以
`docs/plans/2026-07-24-pocketbase-acceptance-evidence.md` 为准。

- [x] WP-00：冻结 v1 契约与四语言 round-trip。
- [x] WP-01：真实 PocketBase sidecar、迁移、鉴权、健康与黑盒生命周期。
- [x] WP-02：深生命周期 supervisor 核心完成；真实 `MainWindow` composition 留在 WP-14
  纵切统一接入，避免双后端。
- [x] WP-03：SchemaCatalog、字段映射、约束 fail-closed、rename 数据保留、
  self relation、metadata invariant、真实 PB 集成。
- [x] WP-04：QueryPort、HMAC snapshot、事务一致性、JSON/relation/aggregate/archive，
  并已接入生产 SchemaSource 和 `/query` 路由。
- [x] WP-05：MutationKernel（事务、revision/digest、幂等、audit/outbox）已实现并
  接入生产 HTTP；真实 PB 原子批量/冲突/重放/故障测试通过，等待异作者终审。
- [x] WP-06A：CEL 编译、类型、静态依赖、DAG、资源限制、预览与事务内物化完成；
- [x] WP-06B：relation 解引用、Lookup/relation reverse dependency metadata、
  10k fan-out、durable task cursor、取消/重启恢复与 task.changed 已完成；
  独立审查已修复 JSONRaw/json.Number、UTC 日期函数、深层限制、缓存身份和 metadata。
- [x] WP-07：审计权威事件字段、change-set 查询与两阶段 restore→MutationKernel
  已实现并接入生产路由；独立审查正在修复归档恢复语义、schema drift、资源上限与 token 状态。
- [x] WP-08：PB File field、multipart mutation、metadata、`/files/token`、capability download、
  thumbnail、内容 hash/size、integrity/orphan scan、故障回滚及资源上限完成并通过独立审查。
- [ ] WP-09：relation/Lookup schema 元数据、引用验证、目标搜索、delta→MutationKernel、
  直接 relation Lookup 事务内物化和产品 HTTP 已完成第一纵切；继续 M2A 与反向依赖。
- [ ] WP-10～WP-11、WP-13～WP-16：按依赖链继续。
- [x] WP-12：Python 第二轮审查修复已完成；C# 文件级 CAS、receipt、conflict ref
  与 schema 加固仍在子代理收尾，完成后统一全量回归。

## 2026-07-24 实施执行

### 当前目标

依据 `docs/research/archive/2026-07-24-pocketbase-implementation-plan.md` 完成 WP-00 至 WP-16，
最终删除生产路径中的 Directus 依赖，并通过 unit、contract、integration、E2E、
fault-injection 与 package smoke 验收。

### 执行阶段

- [x] E0：基线盘点、保护用户改动、将 16 个工作包继续拆成可并行文件所有权
- [x] E1 / M0：WP-00～WP-02（契约、Go sidecar、桌面生命周期）
- [x] E2 / M1：WP-03～WP-06A（schema、query、mutation、公式 F1 最小纵切）
- [x] E3 / M2：WP-07～WP-08（审计恢复、托管附件）
- [ ] E4 / M3：WP-06B、WP-09～WP-11（WP-06B/WP-10/WP-11 sidecar 已完成，
  WP-09 junction/M2A 与 WP-11 client reconcile 待收口）
- [ ] E5 / M4：WP-12～WP-14（工作区、设置/插件、UI/RPC 纵切）【WP-12 已完成并主审】
- [ ] E6 / M5：WP-15～WP-16（开发/CI/打包、Directus 与协作功能删除）
- [ ] E7：独立审查、修复、全量验证、完成定义逐项核对

### 续行收口状态

- [x] WP-11 Python SSE：断线重连、游标续传、重复事件、cursor gap reconcile、产品通知接线
- [x] Python closed 产品 RPC 第一纵切：schema/query/mutation/formula/file/realtime/direct relation/Lookup query
- [x] 中段 Go migration checksum 与 `go vet` context leak 修复
- [ ] WP-09 Junction/M2A/Lookup：已转派独立子代理实施与自审
- [ ] WP-07/08 历史附件二进制归档/恢复：已转派独立子代理实施与自审
- [ ] WP-14 MainWindow composition、sidecar secret 仅 Host→Python 注入、完整 UI 纵切
- [ ] WP-15 handoff/依赖清单和外部安装器验收
- [ ] WP-16 物理删除 Directus/Flow/协作入口并跑全套 CI

### 执行约束

- 保留并避免覆盖开始时已有的三个未提交改动：
  `MainWindow.xaml.cs`、`DirectusTableGateway.cs`、`DirectusTableGatewayTests.cs`。
- 写入型子任务按互斥目录或文件所有权并行；依赖未满足的工作包顺序推进。
- 子代理输出只是证据，主代理必须复核 diff、测试结果与完成定义。
- Directus 删除仅在替代纵切及门槛验证完成后进行。
- `MainWindow` 对 `LocalDataService` 的最终接线归入 WP-14/B15B；M0 先冻结并验证
  深生命周期 Module，避免在用户脏文件上建立临时双后端组合。

## 目标

由于 Directus 后端版本尚未实际发布，不考虑数据迁移和双后端兼容。在既有能力矩阵基础上，输出一份可以直接拆成工程任务的 PocketBase 完整实施计划：覆盖目标 Module/Interface、数据模型、公式和统一写入、托管附件、桌面 sidecar、产品功能切换、测试、脚本、CI、发布以及 Directus 删除门槛；本轮只产出计划，不修改业务实现。

## 阶段

- [x] 阶段 1：确认 worktree、分支、现有改动与仓库约束
- [x] 阶段 2：梳理当前字段模型、建表流程、Directus 网关与测试
- [x] 阶段 3：对照 Directus 官方能力，建立覆盖矩阵
- [x] 阶段 4：识别缺口、原因、风险和建议优先级
- [x] 阶段 5：确认计算字段必须通过普通 API 查询，且不直接操作 SQLite DDL
- [x] 阶段 6：核验 Directus Labs、服务端 Hook 与 PocketBase 计算能力
- [x] 阶段 7：核验可复用的公式解析/执行依赖及安全边界
- [x] 阶段 8：检查现有数据访问 seam，设计 Directus/PocketBase 双 Adapter
- [x] 阶段 9：给出决策矩阵、推荐分期与待用户确认项
- [x] 阶段 10：核验 `pb-audit` 的真实项目、能力、维护状态和许可证
- [x] 阶段 11：核验 Activepieces CE 架构、自托管能力、嵌入接口和许可证
- [x] 阶段 12：按“去 Directus”目标重构结论：公式内核 + PocketBase Adapter + 现有插件运行时
- [x] 阶段 13：列出 Directus 耦合拆除顺序、迁移边界和首个可验证里程碑
- [x] 阶段 14：按领域枚举当前功能、入口、Directus 依赖与 PocketBase 等价能力
- [x] 阶段 15：枚举必须新增的 VibeTable/PocketBase 能力，含公式、约束、JSON、审计、插件
- [x] 阶段 16：定位需要修改、替换或删除的单元测试、集成测试、脚本、资源与 CI
- [x] 阶段 17：输出功能/测试/脚本三张实施矩阵和建议删除顺序
- [x] 阶段 18：核验 PocketBase 按 collection/record 组织文件与“按表指定存放位置”的真实能力，冻结托管附件边界
- [x] 阶段 19：定义目标 Module、Interface、PocketBase collections、RPC/事件和进程拓扑
- [x] 阶段 20：按依赖顺序拆分可独立合并的工程工作包，并映射现有文件/新增目录/删除目录
- [x] 阶段 21：为每个工作包定义单元、契约、集成、E2E、故障注入和发布验收门槛
- [x] 阶段 22：定义纵切顺序、Directus 删除门槛、风险控制、回退方式和完成定义
- [x] 阶段 23：输出完整实施计划并交叉检查与已有研究矩阵一致

## 当前状态

已完成：PocketBase 完整实施计划已落盘，并与研究矩阵、现有文件清单、附件能力和测试/发布门槛完成交叉检查。

## 决策

- 审计期间允许创建分支和调查记录，不修改业务代码。
- 产品取舍按 grilling 要求一次确认一个问题。
- 用户明确产品主要是单机 SQLite，允许把 PocketBase 作为可选后端纳入设计。
- 计算字段不采用数据库 generated-column DDL；优先讨论普通字段物化。
- 本轮先研究与设计，不修改业务实现。
- 推荐取消前端权威公式求值；后端提供试算端点和事务内物化。
- 推荐项目级选择 Directus 或 PocketBase，不允许两个后端同时打开同一 SQLite 文件。
- 推荐深化 `ITableRpcGateway` 并按 capability 拆分可选能力，不让 PocketBase 实现 Directus 专属 interface。
- 用户确认：所有业务写入必须经过 VibeTable；不承诺外部客户端直接写所选后端。
- 因此公式、审计和 mutation 一致性可以集中在 VibeTable provider-neutral 写入模块，后端 Hook 不再是首要前提。
- 用户提出用 `pb-audit` 补齐 PocketBase 审计，并调研 Activepieces CE 作为公共工作流层，必须单独核验许可证。
- 用户进一步明确目标是摆脱 Directus，而不是长期维护 Directus/PocketBase 等价；Activepieces 暂退出核心路径。
- 工作流/插件代码只能调用 VibeTable capability/mutation API，不能直接调用 PocketBase 写接口；优先保留现有“插件返回 mutation plan，Host 校验后执行”的安全模型。
- 用户确认 Directus 后端从未发布，因此不做迁移工具、双写、兼容开关或 Directus 数据导入。
- 目标是 PocketBase 直接替换 Directus；旧 Directus Adapter、扩展、安装与测试在 PocketBase 纵切覆盖后应删除，而不是长期保留第二个 Adapter。
- 产品是单人桌面应用；删除多人协作评论、@提及、通知收件箱和活动流，保留个人审计/历史、任务状态和内部 outbox。
- 保留本地工作区文档；PocketBase File field 作为记录内“托管附件”，删除独立“云端资源附件”tab。
- PocketBase 原生以 `collectionId/recordId/filename` 组织托管附件，因此每张表天然具有独立存储命名空间；首版不提供任意目录或 per-table 独立存储后端配置。若未来确需该能力，再引入第二个真实存储 Adapter 与对应 Seam。

## 错误记录

| 错误 | 尝试 | 处理 |
|---|---:|---|
| 首次实施盘点的 manifest 枚举误扫 `scripts/local_directus/node_modules`，输出被截断 | 1 | 后续所有枚举显式排除 `**/node_modules/**` 和运行时缓存目录 |
| .NET 基线因缺少项目锁定的 SDK 10.0.100 无法启动 | 1 | 记录为工具链缺失；后续提供/安装可复现 SDK 后再跑 |
| Python 基线因缺 pytest-asyncio 和 `.pytest_cache` ACL 失败 | 1 | 后续禁用 cacheprovider 并补齐锁定开发依赖，不重复原命令 |
| Go 便携工具链下载/解压命令在 180 秒超时 | 1 | 只读检查确认下载与解压已实际完成，随后单独运行 `go version` 验证成功 |
| 首次 Go 解压只有 6 个标准库目录，`go version` 正常但无法编译 | 2 | 不删除旧目录，解压到 `.tools/go-full/go`；确认 55 个标准库目录后通知 sidecar 代理重跑 |
| 用 PowerShell 双引号内联 Python 读取 `$defs` 时变量被展开为空导致 KeyError | 1 | 改用 Node 拼接键名读取，确认 schema 含 41 个定义；不重复该内联写法 |
| pytest-asyncio 与 pydantic-core 的现有 dist-info/DLL 损坏 | 1 | 使用 uv.lock/pyproject 指定版本强制重装，恢复测试收集与 pydantic import |
| Python 默认 temp、`C:\tmp` 在子进程内均因 ACL 不可写 | 2 | 预创建仓库内 `.codex-test-tmp` 作为 basetemp，完整基线可运行 |
| .NET 沙箱内读取 Windows SDK 元数据被拒绝 | 1 | 按规则在沙箱外重跑同一测试命令，421 个测试全部通过 |
| 主审 Go 命令在 `sidecar/` cwd 下误用仓库根相对 `.tools` 路径 | 1 | 改用已确认的绝对工具链和缓存路径重跑，不重复相对路径 |
| Go 主审在沙箱内无法创建默认 module cache | 1 | 按规则沙箱外重跑；test/vet/build 全部通过 |
| B01 真实进程黑盒无 ready 输出且遗留进程 | 1 | 立即终止遗留进程；回派修正 OnServe ready 时机并增加真实二进制黑盒测试 |
| B01 复跑全量时与 B03 同时扩展 migration manifest，出现瞬态 embed map 错误 | 1 | B01 停止触碰/验证 migrations 并收束；B03 完成后由主线程统一跑 Go 全量 |
| 并行环境检查返回退出码 1，只保留了部分输出 | 1 | 已获得 git/worktree 状态；后续拆分检查失败项，避免重复同一组合命令 |
| 分支创建、AGENTS 搜索、diff 的并行调用因单项失败整体无输出 | 2 | 改用逐项捕获状态并先只读确认分支，避免无法区分成功与失败 |
| 沙箱内无法写入 `.git/refs`，首次创建分支失败 | 1 | 经用户授权范围内的提权重试后成功创建分支 |
| 大范围 `rg`/文件枚举返回退出码 1 且输出被截断 | 1 | 已保存关键命中；后续按已定位文件做窄范围读取，不再重复全仓库搜索 |
| 前端 editor 搜索的 PowerShell 引号未闭合 | 1 | 改用单一简单 pattern 或直接读取已定位组件，不复用复杂引号 |
| field meta consumer 的复杂正则括号未闭合 | 1 | 改用多个固定字符串搜索或直接检查 schema 映射；不重复该正则 |
| 定向测试首次启动失败：`uv` 不在 PATH，PowerShell 禁止执行 `npm.ps1` | 1 | Python 改用仓库 `.venv\Scripts\python.exe -m pytest`；前端改用 `npm.cmd` |
| Python 测试被项目默认 `--cov` 参数阻断，虚拟环境没有 pytest-cov | 1 | 用 `-o addopts=""` 覆盖默认参数运行同一批测试，不安装新依赖 |
| `.venv` 仍缺 pytest-asyncio，且 `.pytest_cache` ACL 不可写，Python 测试二次未收集 | 2 | 检查系统 Python 是否有仓库要求的插件；重跑时禁用 cacheprovider |
| Codex 自带 Python 也没有 pytest，无法无安装地运行 Python 定向测试 | 3 | 停止重复尝试；记录为环境验证阻塞，静态证据与前端测试结论仍有效 |
| planning-with-files 的 session-catchup 无法启动：`python` 不在 PATH | 1 | 改用 `py -3`，不重复原命令 |
| `py -3` 也找不到已安装 Python | 2 | 放弃自动 catchup，改为手工读取规划文件、`git diff --stat` 与 `git status` 恢复上下文 |
| 并行文件清单因若干不存在的目录使 `rg --files` 返回 1，只保留 Directus/测试清单 | 1 | 已得到关键 Directus 文件清单；后续只扫描实际存在的目录，并让各命令独立返回 |
| PowerShell 递归枚举 manifests 超过 20 秒，导致并行 RPC 清单一并丢失 | 1 | 不再使用 `Get-ChildItem -Recurse -Include`；改用 `rg --files -g`，RPC 注册单独查询 |
| `rg` 在 Windows 上直接接 `*.cs` 路径通配符返回路径语法错误，输出同时被截断 | 1 | 已保留 `MainWindow` 关键命中；后续用 `-g \"*.cs\"` 配合目录参数或直接读指定文件 |
| Web 工具直接打开 PocketBase v0.39.9 GitHub blob 返回 cache miss | 1 | 改用已连接的 GitHub 官方仓库文件读取，成功取得同一 tag 的 `core/field_file.go` |
| GitHub 代码搜索包含 Go 方法签名括号时返回 422 query parsing | 1 | 不重复复杂查询；改用简单固定字符串 `BaseFilesPath` 或直接读取已知官方源码文件 |
| Schema HTTP 黑盒首次把 PocketBase 的可重读 Body 当普通流，第二次 EOF 检查重新读到了同一 JSON | 1 | 先在 1 MiB 上限内一次性读取原始 body，再在独立 `bytes.Reader` 上做严格单值/未知字段解码；全量 Go 测试复跑通过 |
| WP-12 revision 发布首版让投影异常阻断本地 document.list，导致 2 个 Desktop 测试失败 | 1 | 将元数据发布改为 durable outbox + best-effort 投影：本地文档保持权威可用，失败保留 outbox，成功后清理；Workspace 75/75、Desktop 220/220 复跑通过 |
| 独立 workspace RPC 装配使真实 Python backend 握手能力从 9 增至 16，旧 Infrastructure 测试仍写死旧集合 | 1 | 更新验收清单加入 7 个 provider-neutral workspace 方法；Infrastructure 115/115 复跑通过 |
## WP10 Python 纵切（子代理 repo_coupling_audit）

- [x] 盘点现有 paste/import/export/relation 契约与测试 seam
- [x] 以失败测试定义 PocketBase HTTP port 与 frozen mutation 请求
- [x] 实现 paste/import preview、幂等、取消、分块失败回滚
- [x] 实现 JSON round-trip、显式 relation 解析、QueryPort/Lookup 导出
- [x] 相关 pytest、ruff 与范围审查

### WP10 错误记录

| 错误 | 尝试 | 处理 |
|---|---:|---|
| `python` 不在 PATH | 1 | 改用 `.venv/Scripts/python.exe` |
| 读取不存在的 `sidecar/internal/query/executor.go` | 1 | QueryPort 实现实际在 `port.go`，不再重复该路径 |
| pytest 缺少 pytest-cov 插件 | 1 | 定向测试使用 `-o addopts=`，全量证据另行说明 |
| 根 `.pytest_cache` 被并行任务占用/拒绝访问 | 1 | 使用 WP10 专属 `.tmp/wp10-pytest-cache` |
| `CollectionProfile.capability_hash` 是计算属性，测试不能注入 `schema_7` | 1 | mutation port 显式接收 preview 绑定的 `schema_revision`，避免混淆 legacy capability hash |
| pytest 默认 TEMP 根被并行任务占用/拒绝访问 | 2 | 环境变量未覆盖已缓存 temp 根，最终使用唯一 `--basetemp=.tmp/repo_coupling_audit_wp10` |
| 原子 apply 改写时误删仍被 token mint 使用的 `uuid` import | 1 | 恢复 import；idempotency 随机值已独立改为 token 派生稳定值 |
| Export 大块补丁因旧文档乱码上下文不匹配 | 1 | 改为按 import、协议、循环和 render 的小块精确补丁 |
| PocketBase client lookup 补丁上下文过宽未匹配 | 1 | 先读取当前文件，再按常量/Protocol/方法/helper 精确拆分 |
| Ruff 可执行文件 CreateProcess 被 sandbox 拒绝 | 2 | `python -m` 与直接 exe 均失败；已确认二进制存在，下一步按权限规则请求仅 Ruff check 的提升执行 |
| composition test 的最小 legacy manifest 缺少 capability allowlists | 1 | 复用完整允许字段集合，测试装配而非 manifest 校验 |

## WP14 UI/RPC 纵切（子代理 repo_coupling_audit）

- [x] 盘点 WP14、12.1/12.5 验收矩阵及现有 .NET/Vue 产品接缝
- [x] 红→绿建立 .NET 产品命名 gateway、取消、错误映射与 renderer 安全边界
- [x] 红→绿迁移 Vue 可达 RPC 到产品命名并覆盖 schema/query/mutation/formula/JSON/附件/历史/relation/Lookup/realtime reconcile
- [x] 红→绿扩展 CreateTableModal/字段编辑器的全字段、约束、路径错误与异步状态
- [x] 运行 .NET、web-grid 定向/全量测试和 `directus.*` 可达性审查
- [x] 汇报必须由根代理安全合并到 dirty `MainWindow.xaml.cs` 的 composition 入口

### WP14 错误记录

| 错误 | 尝试 | 处理 |
|---|---:|---|
| `python` 不在 PATH，planning session-catchup 无法启动 | 1 | 改用 `py -3` 探测后转为直接读取共享计划和工作树恢复 |
| `py -3` 也未发现已安装 Python | 2 | 不再重复解释器探测；使用已读计划、findings、progress 与 `git status` 手工恢复 |
| PowerShell 下用 `rg ... Services/*.cs` 触发 Windows glob 路径错误 | 1 | 改为目录参数配合 `--glob '*.cs'`；不再把通配符直接放路径段 |
| 搜索包含不存在的 `sidecar/internal/httpapi` 路径产生单项错误 | 1 | sidecar route 实际在 `sidecar/internal/app`；后续只用已枚举出的真实路径 |
| PowerShell 执行 `npm` 被 ExecutionPolicy 拒绝加载 npm.ps1 | 1 | 后续统一使用 `npm.cmd` |
| .NET 定向测试在沙箱内被 Windows SDK 元数据 ACL 拒绝 | 1 | 按权限规则在沙箱外运行；获得真实红灯并在实现后转绿 |
| relation/Lookup 兼容回退使用 `??` 合并两个不同具体接口类型，临时 WPF 项目编译失败 | 1 | 显式按共同 `IRelationLookupRpcGateway` 接口选择产品 gateway 或 legacy gateway；定向 37/37 转绿 |
| Desktop 全量仅剩两个旧 `ShouldAutoStartLocalDirectus` 资产探测基线失败 | 1 | WP14 不伪造旧 Directus 资产；确认其余 233/233 通过，这两个测试与实现交由 WP16 物理删除 |

## WP16 物理删除与独立审查（子代理 repo_coupling_audit）
- [ ] Python composition 只保留 PocketBase 产品端口、local worker 插件、任务和本地工作区
- [x] 删除 Python Directus/Flow/协作物理实现、契约与旧测试，收窄剩余 imports
- [x] 插件 manifest/package/CLI 只允许 local worker + mutation/network capability，并拒绝遗留字段
- [ ] 删除 `directus/`、`scripts/local_directus/`、迁移脚本及 Desktop Directus 生产/测试路径
- [x] 更新 handoff 阶段和依赖 hash 为 sidecar/schema/migration/capability
- [ ] 分批运行 Python/.NET/plugin/QA 测试并完成全仓生产路径引用审查
- [ ] 三处用户初始 dirty 文件保持原样；仅从项目编译/测试入口隔离

## WP15 开发、CI、打包、发布（子代理 query_port_review）

- [x] 阅读计划第 13 节、真实集成、性能/故障与发布门槛
- [ ] 盘点 scripts/qa/desktop 发布布局及现有测试 seam
- [ ] 红→绿实现固定版本 sidecar 构建、资产 manifest/hash/build-info/licenses/SBOM
- [ ] 红→绿重写 dev/QA/package-check/handoff，加入真实 sidecar smoke
- [ ] 红→绿实现数据/安装目录分离、升级备份与失败保留策略
- [ ] 清理 plugin esbuild fallback、旧 Directus/local-directus/npm-runtime flags
- [ ] 跑 pytest、脚本 smoke、Go/.NET 定向门槛并汇报 installer 外部验证项

### WP15 约束与并行状态

- 文件所有权仅限 parent 指定的 scripts/qa/publish-layout/LaunchPaths 及对应测试。
- 不触碰 sidecar Go、backend、MainWindow 和三处用户 dirty。
- 当前 4 个并发槽位均占用，无法为 WP15 再派独立代理；整体任务已由根代理使用多代理并行，
  本工作包完成后仍由根代理和 WP16 代理独立复核。

## WP09 Relations / Junction / M2A / Lookup（子代理 query_port_review）

- [ ] 盘点当前 frozen/normalized schema、relation、mutation、lookup 和真实 PB 测试 seam
- [ ] TDD：严格 schema 表达 direct、junction M2M、M2A allowlist，保持 v1 additive 兼容
- [ ] TDD：junction/M2A search 与 delta 经 MutationKernel 完成幂等、冲突、审计和回滚
- [ ] TDD：Lookup 覆盖 junction、M2A 与 reverse dependency
- [ ] 运行 Go contract/unit/integration、vet、格式与全量回归并自审

### WP09 测试 seam

- schema 公共 seam：frozen v1 JSON decode/validate 和 normalized schema catalog。
- relation 公共 seam：Relation service 的 Describe/Search/Delta。
- 写入公共 seam：MutationKernel receipt、idempotency、audit/outbox。
- Lookup 公共 seam：Lookup Query 与 computed reverse dependency。
- 集成 seam：真实 PocketBase app/HTTP API，不 mock 内部协作者。

### WP09 错误记录

| 错误 | 尝试 | 处理 |
|---|---:|---|
| 在仓库根目录执行 Go package 路径，Go 找不到 `go.mod` | 1 | 后续统一以 `sidecar/` 为 cwd，不重复该命令 |
