# VibeTable 架构、功能完成度与质量审计报告

审计日期：2026-08-11  
审计对象：`e730f558713eb702648ca0be7a41a850c9687e1d`，分支 `codex/fix-github-security-alerts`  
审计口径：以代码、可执行配置、测试和本次实测为主；文档只作辅助证据。功能按“实现存在 → 跨栈接线 → 自动化验证 → 产品级闭环”四层判断。

## 1. 执行摘要

VibeTable 已经具备有深度的离线多维表格数据内核，但还不是功能面完整、体验闭环成熟的主流多维表格替代品。

- 架构方向正确：PocketBase 是唯一数据权威；WPF 持有本机能力和进程生命周期；Web、Python BFF、Go sidecar 通过封闭契约、workspace UUID、session epoch、request/operation ID 和 path grant 协作。数据一致性、冲突、恢复、快照和审计设计明显强于普通 MVP。
- 架构成本偏高：同一功能往往跨 Vue/WPF/Python/Go 四栈及镜像契约，多个协调文件达到 1,500～3,686 行，局部可理解性差，变更扩散和回归成本高。
- 功能并未全部产品级闭环：官方能力矩阵仍有 Hidden/Internal 路径；文件列表存在 `size/modifiedAt` 类型和 UI 列但没有数据链；Dashboard、Preset/version、插件升级/回滚等存在“有 producer/组件，但挂载或真实 E2E 不完整”的情况。
- 不能回答“全部功能都有 E2E”：真实产品 E2E 只有 16 个粗粒度场景，没有逐功能覆盖文件搜索、替代视图完整交互、导出、批量粘贴、快捷键、更新、托盘、Dashboard 图表变体等。并且本地没有与当前 HEAD 对应的完整 E2E 报告可复核。
- 测试总量大但不均衡：Python 90.55%；Web 行覆盖 76.75%、分支 62.37%，且没有阈值；.NET 各门禁项目达标，但 PreviewHost 单体只有 14.46%；Go 没有全局覆盖率门禁，包级数值从 2.4% 到 95% 不等。
- 日志不完整：Go 是结构化 JSON，Python 是纯文本，WPF 负责汇集子进程 stderr；缺少统一 schema、跨进程上下文、轮转/保留、脱敏、生产主进程崩溃闭环和一键诊断包。
- 文件管理搜索能力很弱：只支持已经全量加载到前端的 `displayName` 大小写不敏感子串过滤；不支持日期、格式、内容、AND/OR、服务端搜索或分页，且 10,000 文档硬上限。

综合判断：数据与恢复内核约处于“可用且有特色”阶段；面向普通业务用户的多维表格产品层仍处于“部分完成”。排除权限后，与 Airtable、飞书多维表格、Notion、Coda 的最大差距是表单、自动化/工作流、低代码界面、连接器/同步、内容型记录、评论通知、AI、多端和可观测性，而不是基础字段或 Relation/Lookup。

## 2. 架构审查

### 2.1 主链路

```text
Vue 3 / TypeScript WebView UI
        ↓ typed bridge / requestId
.NET 10 WPF host：本机 UI、路径授权、进程管理、日志汇集
        ├─ JSON-RPC/stdin-stdout → Python 3.11 BFF
        └─ HTTP/SSE/RPC         → Go sidecar / PocketBase
                                      ↓
                            workspace 数据与文件历史
```

优点：

1. 权威边界明确。前端没有绕过 PocketBase 自建 SQLite 写路径，workspace UUID 是身份而不是目录名。
2. seam 设计成熟。session epoch、防陈旧响应、稳定错误 envelope、取消、幂等、path grant 和 request/operation ID 均有代码和测试支撑。
3. 故障域划分合理。Web 不直接取得本机任意路径；WPF 负责启动/停止 Python 和 PocketBase；Go 承担数据与恢复内核。
4. 离线、快照、冲突、审计、恢复、文档版本/差异比较构成了区别于纯云表格的产品优势。

主要技术债：

| 技术债 | 证据 | 影响 |
|---|---|---|
| 超大协调模块 | `MainWindow.Product.cs` 3,686 行、`WorkspaceView.vue` 3,205 行、`WorkspaceRequestDispatcher.cs` 2,347 行、`product_rpc.py` 1,719 行、`workspaceV2Bridge.ts` 1,565 行 | 状态、协议转换和业务编排混合；修改难以局部推理 |
| 四栈镜像契约 | Web/.NET/Python/Go 均有对应 DTO、方法名和错误映射 | 新增字段或语义需要多点同步，漂移风险高 |
| 隐藏 producer/未挂载组件 | capability matrix 标记 Workspace relink、Preset/version、插件部分 mutation 为 Hidden/Internal | 代码存在不等于用户可用，增加维护面和误判 |
| 过时审计记录进入仓库 | `sidecar/task_plan.md`、`sidecar/findings.md`、`sidecar/progress.md` 有乱码和过时结论 | 污染事实来源，误导后续维护和自动审计 |
| 产品层与内核成熟度不匹配 | 恢复/冲突内核深入，但 FileWorkspace/Dashboard UI 覆盖低 | 研发成本集中在用户不容易感知的部分 |

建议把跨栈协议保留为稳定接口，同时把大协调文件拆成按能力聚合的深模块：每个模块拥有状态机、映射和错误处理，外部只暴露少量命令/查询；不要再增加平铺的桥接方法。

## 3. 功能完成度

| 功能域 | 实现/接线状态 | 自动化证据 | 结论 |
|---|---|---|---|
| Workspace 创建、打开、重定位、离线启动 | 主链完整；relink 有 Hidden 路径 | 离线首启 E2E；路径/epoch 有分层测试 | 主流程完成，重定位产品入口不完整 |
| 表、字段、记录、全字段类型 | Web/WPF/Python/Go 已接线 | 全字段、schema 错误 E2E | 核心完成 |
| 筛选、排序、分组、汇总、隐藏字段 | 3 层/50 条 AND/OR；2 层分组；3 个汇总 | 单元/集成较强，场景 E2E 部分覆盖 | 核心可用，收口文档仍写 Partially implemented |
| 公式 | token、类型推断、预览、聚合链已实现 | 公式生命周期 E2E + 分层测试 | 较完整 |
| Relation / Lookup | 双向 Relation、最长 8 跳 Lookup、>10k 相关记录分页/流式 | relation fanout E2E + 大量集成测试 | 较完整 |
| 表格编辑、批量粘贴、导入 | 编辑与原子导入接线；CSV/XLSX 有服务 | 大规模原子导入 E2E；粘贴主要是组件/服务测试 | 导入较强，粘贴缺真实产品 E2E |
| 导出 | 存在 CSV/Excel 相关实现 | 没有独立真实产品 E2E | 实现存在，验证不足 |
| 看板/日历/时间线/画廊 | 组件与视图逻辑存在 | 主要是相邻单测，没有完整交互 E2E | 部分完成，产品打磨不足 |
| Dashboard | 查询、采样、布局、生命周期存在 | 仅生命周期 E2E；大部分 UI 组件 0% | 不能视为完整成熟功能 |
| 附件与文件历史 | 附件历史、预览、恢复、diff 有链路 | 附件历史/文档 diff E2E | 版本内核较强，文件管理搜索很弱 |
| Snapshot、备份、恢复、冲突、retention | 内核和防护契约深入 | stale conflict、备份一致性、snapshot 包、protection E2E | 强项；retention E2E 只验证零删除计划 |
| Realtime/SSE | 已接线 | SSE reconnect E2E | 主链完成 |
| 插件 | SDK、worker、mutation 和生命周期基础存在 | plugin mutation E2E；成功升级/回滚/卸载部分 Hidden | 平台基础存在，产品管理闭环不完整 |
| Preset/version | producer、RPC、组件存在 | 无产品挂载点/真实 E2E | 未接线，不应计入已完成功能 |
| 更新、托盘、快捷键、日历集成 | 能找到实现和局部测试 | 无对应完整产品 E2E | 有实现，验证覆盖不足 |

“未发现 TODO/FIXME”不代表没有未完成代码。该仓库的主要未完成形态是：类型和组件存在但没有 producer/consumer 闭环、功能被标记 Hidden/Internal、或只有局部测试而没有真实用户路径。

## 4. 测试与覆盖率

### 4.1 本次实测

| 栈 | 命令/结果 | 覆盖率 | 判断 |
|---|---|---|---|
| Python | 1051 passed，1 failed | backend 90.55%，阈值 85% | 覆盖率达标；失败因本地 `runtime/node` 违反可恢复工具链架构断言，不是业务断言 |
| Web/Vitest | 128/128 files，916/916 tests passed | statements 72.40%，branches 62.37%，functions 70.29%，lines 76.75% | 无覆盖率门禁；关键产品 UI 盲区明显 |
| .NET | 572/572 tests passed | Workspace 93.33/86.48；Infrastructure 73.21/64.16；Desktop+PreviewHost 51.91/45.04（line/branch） | 声明门禁达标，但合并门禁掩盖 PreviewHost 14.46% 行覆盖 |
| Go | 大部分包通过，最终 2 个清理失败 | 无全局阈值；包级 2.4%～95% | 两个业务断言完成后在 Windows `t.TempDir` 清理报目录非空；覆盖率保障不完整 |

Web 关键盲区：DashboardCreateModal、Drilldown、FilterBar、Grid、PanelEditor、Settings、Sidebar、Toolbar 和 DashboardWorkspaceView 多数为 0%；FileWorkspaceView 为 0%；WorkspaceView 行覆盖 46.07%；DocumentInspector 45.45%；DocumentList 62.5%。

Go 低包级覆盖示例：metadata 2.4%、schemaapi 2.7%、relation 3.7%、attachments 5.1%、audit 5.3%、jobs 7.9%、realtime 12.5%、mutation 17.5%、lookup 17.2%。默认 `go test -cover ./...` 不会把跨包集成执行完整归因到目标包，因此不能直接断言这些行为完全未测；但没有 `-coverpkg` 总门禁，确实无法量化保障。

### 4.2 E2E 结论

真实产品 E2E 固定为 16 个场景：离线首启、全字段 schema、schema 错误、JSON round-trip、公式生命周期、关系 fanout、附件历史、陈旧冲突、原子导入规模、SSE 重连、插件 mutation、备份一致性、保护策略、文档 diff、workspace snapshot 包和 Dashboard 生命周期。

因此答案是：**不是全部功能都有 E2E**。缺少独立闭环的至少包括：

- 文件名/日期/格式/内容/AND-OR 搜索；
- 看板、日历、时间线、画廊的完整创建、配置、拖拽、保存与重开；
- CSV/Excel 导出和批量粘贴；
- Dashboard 多图表、筛选、钻取、编辑冲突；
- 快捷键、更新、托盘、日历集成；
- Preset/version 和 Hidden 插件生命周期路径。

`.ci/project.json` 的 `ci.e2e` 是空数组，产品 E2E 被折叠进昂贵的 release smoke。仓库文档引用过 2026-08-09 的 16/16 报告，但对应文件当前不存在；本地 `build/qa/report.json` 是 2026-07-28、旧 commit 且失败的陈旧报告。因此本审计不能声称“当前 HEAD 的完整真实 E2E 已通过”。

### 4.3 边缘情况

强项集中在数据和协议边界：非法契约、空操作、过期 session/schema、幂等冲突、事务回滚、取消/超时、重启恢复、损坏数据、大批量限制、Unicode、SSE 重连、恢复冲突均有测试。

薄弱处集中在可见交互和环境边界：多视图拖拽/持久化、Dashboard UI 组合、文件工作区、真实导出/粘贴、生产崩溃日志、Windows 文件占用清理。部分 Go 测试还依赖 Kopia/age/Windows Credential Manager 或 release runner，短模式和缺少固定工具链时会跳过。

## 5. 日志与可观测性

现有链路：

- Python：`backend/__main__.py:142` 配置标准 logging，INFO、纯文本、stderr；主要硬约束是不能污染 JSON-RPC stdout。
- Go：`sidecar/cmd/vibetable-pb/main.go:317` 使用 `slog.JSONHandler(os.Stderr)`，结构化程度最好。
- WPF：`ProductionWorkspaceRuntime.cs:278,306` 将 sidecar/backend stderr 分别写到 workspace 临时目录的 `logs/pocketbase.log`、`logs/backend.log`；supervisor 持续排空 stderr。
- 主进程异常：`App.xaml.cs:69-101` 的 AppDomain/Dispatcher 未处理异常记录仅在 test mode 注册。

缺口：

1. Python 与 Go 没有统一日志 schema、时间字段、错误字段和级别政策。
2. requestId/operationId/workspaceId/sessionEpoch 已存在于协议，却没有自动贯穿全部日志。
3. 普通文件追加没有大小/时间轮转、保留期、压缩、配额；长期 workspace 有无界增长风险。
4. 没有统一脱敏过滤器。Go 清理 recovery key 是局部措施，不等于全链路治理。
5. 生产 WPF 主进程缺统一崩溃落盘/Windows Event Log/崩溃报告。
6. `diagnostics.get` 只返回路径、版本、OS、内存和 data service 状态；没有日志导出、最近错误、跨进程 trace 或一键支持包。
7. 关闭流程中存在 best-effort/吞异常分支，虽然避免退出失败，但会削弱现场诊断。

结论：日志“能看见子进程 stderr”，但距离完整生产可观测性仍有明显差距。

## 6. 文件管理与搜索专项

| 能力 | 是否支持 | 实际行为/证据 |
|---|---|---|
| 文件名搜索 | 部分支持 | `documentWorkspaceStore.ts:86-92` 仅对 `displayName` 做 `trim + toLocaleLowerCase + includes` |
| 完整路径搜索 | 否 | 搜索不读取路径字段 |
| 日期筛选 | 否 | 无日期条件 UI/API；sidecar 文档模型无可查询修改时间 |
| 格式/MIME 筛选 | 否 | UI/API 没有格式条件；即使渲染项有 MIME 也不参与查询 |
| 文件大小筛选 | 否 | `size?` 只在 TS 类型/UI 存在，`toStoreEntry()` 没有赋值 |
| 文件内容搜索 | 否 | 无内容提取、tokenizer、FTS/倒排索引或内容查询契约 |
| AND/OR 搜索 | 否 | 单字符串子串匹配，不解析 token 或布尔表达式 |
| 服务端搜索/排序 | 否 | `fileHistory.listDocuments` 只有 `includeDeleted` |
| 分页 | 否 | sidecar 一次性 `history.List()`；超过 10,000 返回 `file_history.document_limit` |

`DocumentEntry` 声明了 `size?` 和 `modifiedAt?`，列表也绘制“修改时间/大小”列，但 `documentWorkspaceService.toStoreEntry()` 没有赋值，Go `FileDocument` 也没有对应元数据。这是本次最明确的“UI/类型存在但数据链未接线”实例。

推荐的新接口不是继续扩展一个 `query: string`，而是建立分页查询 DTO：元数据条件（名称、扩展名/MIME、大小、修改/导入日期、状态）+ AND/OR 条件树 + 排序 + cursor；内容搜索应单独设计可选提取器和全文索引，明确 PDF/Office/文本/OCR 的支持范围、索引状态和失败语义。

## 7. 与主流多维表格的差距（排除权限）

官方基线：Airtable 有 grid/form/calendar/gallery/kanban/timeline/list/gantt、Interface Designer、按钮、自动化、Sync 和 Extensions；飞书有表格/看板/日历/甘特/画册/表单、仪表盘、自动化/工作流、文件夹、完整 `.base` 迁移和 AI；Notion 有 table/board/timeline/calendar/list/gallery/chart/dashboard/form/feed/map、内容型页面、按钮和自动化；Coda 有连接视图、表单、图表、按钮、自动化和 Packs。

参考资料：

- [Airtable views](https://support.airtable.com/docs/en/getting-started-with-airtable-views)、[automations](https://support.airtable.com/getting-started-with-airtable-automations)、[Interface Designer](https://support.airtable.com/docs/interface-designer-overview-articles)、[Sync](https://support.airtable.com/docs/airtable-sync-integrations-overview)
- [飞书多维表格视图](https://www.feishu.cn/hc/zh-CN/articles/472603853615-%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E7%9A%84%E6%95%B0%E6%8D%AE%E8%A1%A8%E5%92%8C%E8%A7%86%E5%9B%BE)、[自动化](https://www.feishu.cn/hc/zh-CN/articles/665088655709-%E4%BD%BF%E7%94%A8%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC%E8%87%AA%E5%8A%A8%E5%8C%96%E6%B5%81%E7%A8%8B)、[导入导出](https://www.feishu.cn/hc/zh-CN/articles/360049067854-%E5%AF%BC%E5%87%BA%E6%88%96%E5%AF%BC%E5%85%A5%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC)、[AI](https://www.feishu.cn/hc/zh-CN/articles/519714421437-%E5%A4%9A%E7%BB%B4%E8%A1%A8%E6%A0%BC-ai-%E5%8A%9F%E8%83%BD%E4%BB%8B%E7%BB%8D)
- [Notion database views](https://www.notion.com/help/views-filters-and-sorts)、[database automations](https://www.notion.com/en-gb/help/database-automations)、[forms](https://www.notion.com/help/guides/use-forms-to-collect-organize-and-act-on-responses-in-notion)
- [Coda tables/views](https://help.coda.io/hc/articles/39555768266893)、[automations](https://help.coda.io/hc/en-us/articles/39555778179853-Automations-in-Coda)、[Packs](https://help.coda.io/en/collections/1400310-integrating-with-packs)

| 能力层 | VibeTable 相对位置 | 主要差距 |
|---|---|---|
| 结构化字段、筛选、Relation/Lookup、公式 | 中等偏强 | 视图级体验、公式生态和大规模实证仍不足 |
| 离线、恢复、快照、文件版本、审计 | 差异优势 | 需要把内核能力转化为可理解的 UI 和可观测性 |
| 视图 | 中等偏弱 | 缺表单、成熟 Gantt/list/map/feed；现有替代视图 E2E 不足 |
| Dashboard/界面搭建 | 弱 | Dashboard UI 覆盖低；没有成熟 Interface Designer/页面布局产品 |
| 自动化/工作流/按钮 | 很弱 | 没有面向用户的触发器—条件—动作编排和运行历史 |
| 外部同步/连接器 | 很弱 | 插件底座不等于 Airtable Sync/Coda Packs 的普通用户产品 |
| 内容型记录与搜索 | 很弱 | 缺正文页面、全文索引、文件内容搜索和统一搜索体验 |
| AI | 基本缺失 | 缺 AI 建表、录入、字段生成、筛选、工作流和分析 |
| 多端与协作体验 | 弱 | Windows x64 单端；排除权限后仍缺评论、通知、表单分发、移动/Web |

不建议用一个伪精确百分比描述“还差多少”。按能力面判断：数据内核差距较小，产品工作流与生态差距大；如果目标是替代主流产品的 80% 日常用法，当前优先补的不是更多底层恢复机制，而是表单、自动化、文件/全局搜索、视图闭环、Dashboard、导入导出实证和可观测性。

## 8. 优先级路线图

### P0：产品可信度与现场可诊断性

1. 补齐或移除文件列表的 `size/modifiedAt` 假接线；新增分页元数据查询和 filename/date/format 条件。
2. 设计全文索引与 AND/OR 查询 AST，先支持文本/PDF/Office 的明确子集，并增加索引状态和失败提示。
3. 统一跨进程日志 schema 和 correlation context；加入轮转、保留、脱敏、生产崩溃日志和一键诊断包。
4. 为 Web 关键产品视图设覆盖率门禁；为 Go 使用 `-coverpkg` 建立可解释的核心包门禁；拆开 Desktop 与 PreviewHost 阈值。
5. 根治 Windows TempDir delete-pending 测试清理问题和本地 `runtime/node` 工具链污染，不用无依据重试掩盖。

### P1：功能闭环

1. 新增文件搜索、替代视图、Dashboard 多图表/钻取/冲突、导出、粘贴、更新、托盘的真实 packaged E2E。
2. 把 Hidden/Internal producer 要么挂成产品入口并完成 E2E，要么删除/隔离为明确实验模块。
3. 拆分超大协调模块，按能力建立深接口；用生成器或单一 schema 减少四栈 DTO 镜像。
4. 清理仓库内乱码/过时 planning 文档，恢复事实来源可靠性。

### P2：缩小主流产品差距

1. 表单/外部分发数据采集。
2. 用户可配置的按钮、自动化和工作流，包含运行历史、失败重放和取消。
3. 低代码界面/Dashboard builder 与更多视图。
4. 外部数据同步连接器与统一搜索。
5. AI 建表、录入、筛选、公式和分析；在离线产品中明确本地模型/可选云服务边界。

## 9. 审计限制

- 本次没有修改产品代码，也没有删除本地 `runtime/node` 或改写失败测试现场。
- 当前环境没有可核对的、与 HEAD 对应的完整 packaged E2E 产物；历史 16/16 只能作为历史证据。
- Go 默认包级覆盖率不等于跨包行为覆盖率，报告已避免把低包覆盖直接解释成“完全未测试”。
- 竞品能力取自 2026-08-11 可访问的官方文档；套餐、限额和具体可用性会变化，比较只用于能力方向，不比较权限和价格。
