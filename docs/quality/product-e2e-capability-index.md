# 产品 E2E 能力索引

> 本文件由 `tests/e2e/pocketbase_product_scenarios.json` 确定性生成；
> 请运行 `uv run python scripts/generate_product_e2e_capability_index.py --write` 更新，
> 不要手工编辑。这里的 capability 是 E2E selector tag，不等同于 Host/runtime 广告能力；
> 本索引只描述 manifest 声明的覆盖范围，不代表某次运行已通过。

## 当前声明范围

- 场景：32
- 唯一能力：51
- 场景—能力关联：67
- `release.smoke` 场景：4

## 能力到场景

| 能力 | 场景 |
|---|---|
| `attachment.history` | <code>07-attachment-history</code>（附件全生命周期与历史恢复）、<code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `attachment.search` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `audit.ledger` | <code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `calendar.lifecycle` | <code>21-calendar-date-move</code>（Calendar 日期拖动持久化） |
| `content.record` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `contract.diagnostics` | <code>03-schema-errors</code>（前端与服务端 typed diagnostic）、<code>06-relation-fanout</code>（双向关联字段编辑、冻结计划与重开） |
| `dashboard.conflict` | <code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `dashboard.drilldown` | <code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `dashboard.filtering` | <code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `dashboard.lifecycle` | <code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `dashboard.visualization` | <code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `data-import.atomic` | <code>09-atomic-import-scale</code>（粘贴或导入中途失败无半提交） |
| `data-io.round-trip` | <code>04-json-round-trip</code>（JSON 编辑、筛选、粘贴、导入与导出不变） |
| `data.json` | <code>04-json-round-trip</code>（JSON 编辑、筛选、粘贴、导入与导出不变） |
| `file-history.diff` | <code>14-document-diff</code>（真实文件历史版本比较） |
| `file-history.query` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `formula.recalculation` | <code>05-formula-lifecycle</code>（空表转换与非空迁移故障回滚） |
| `gallery.lifecycle` | <code>19-gallery-lifecycle</code>（Gallery 创建、重开与冲突恢复） |
| `grid.state` | <code>33-host-grid-presentation</code>（Host 网格呈现保存与恢复） |
| `history.restore` | <code>07-attachment-history</code>（附件全生命周期与历史恢复）、<code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `interface.lifecycle` | <code>17-interface-lifecycle</code>（Interface 构建、运行、重启与删除） |
| `interface.runtime` | <code>17-interface-lifecycle</code>（Interface 构建、运行、重启与删除） |
| `kanban.lifecycle` | <code>20-kanban-lane-drag</code>（Kanban 单选泳道拖拽持久化） |
| `lookup.definition-read` | <code>26-lookup-definition-read</code>（Lookup 持久定义读取） |
| `lookup.export` | <code>34-relation-lookup-data-io</code>（Relation 导入与 Lookup 文本导出） |
| `lookup.source-pagination` | <code>29-lookup-source-pagination</code>（Lookup 来源分页读取） |
| `mutation.authority` | <code>20-kanban-lane-drag</code>（Kanban 单选泳道拖拽持久化）、<code>21-calendar-date-move</code>（Calendar 日期拖动持久化）、<code>22-timeline-date-move</code>（Timeline 单日期拖动持久化） |
| `mutation.conflict` | <code>08-stale-conflict</code>（两次过期编辑显示明确冲突） |
| `offline.start` | <code>01-offline-first-start</code>（干净数据目录离线首次启动） |
| `plugin.action.lifecycle` | <code>17-interface-lifecycle</code>（Interface 构建、运行、重启与删除） |
| `plugin.mutation` | <code>11-plugin-mutation</code>（插件 mutation plan 与越权拒绝） |
| `preset.conflict` | <code>19-gallery-lifecycle</code>（Gallery 创建、重开与冲突恢复） |
| `realtime.reconnect` | <code>10-sse-reconnect</code>（SSE 断线重连且不重复应用） |
| `record-document-link.lifecycle` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `relation.import` | <code>34-relation-lookup-data-io</code>（Relation 导入与 Lookup 文本导出） |
| `relation.integrity-inspection` | <code>31-relation-pair-inspection</code>（关系完整性只读分页检查） |
| `relation.pair-edit` | <code>06-relation-fanout</code>（双向关联字段编辑、冻结计划与重开） |
| `relation.preview` | <code>28-relation-delta-preview</code>（多值关系预览与取消） |
| `relation.search` | <code>27-relation-target-search</code>（关系目标搜索） |
| `release.smoke` | <code>01-offline-first-start</code>（干净数据目录离线首次启动）、<code>02-all-field-schema</code>（Schema v2 字段家族与稳定身份）、<code>08-stale-conflict</code>（两次过期编辑显示明确冲突）、<code>16-dashboard-lifecycle</code>（Dashboard 可视化、筛选与冲突闭环） |
| `replica.recovery` | <code>23-directory-replica-recovery</code>（目录副本释放、重开与进程恢复） |
| `schema.query` | <code>30-query-snapshot-validation</code>（查询快照只读校验） |
| `schema.v2` | <code>02-all-field-schema</code>（Schema v2 字段家族与稳定身份）、<code>03-schema-errors</code>（前端与服务端 typed diagnostic）、<code>05-formula-lifecycle</code>（空表转换与非空迁移故障回滚）、<code>06-relation-fanout</code>（双向关联字段编辑、冻结计划与重开） |
| `snapshot.package` | <code>15-workspace-snapshot-package</code>（工作区切换与快照包） |
| `snapshot.restore` | <code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `timeline.lifecycle` | <code>22-timeline-date-move</code>（Timeline 单日期拖动持久化） |
| `workspace-search.query` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `workspace-search.rebuild` | <code>12-backup-consistency</code>（工作区快照恢复一致性）、<code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `workspace.calendar` | <code>32-shared-work-calendar</code>（工作区共享工作日历） |
| `workspace.lifecycle` | <code>01-offline-first-start</code>（干净数据目录离线首次启动）、<code>10-sse-reconnect</code>（SSE 断线重连且不重复应用）、<code>15-workspace-snapshot-package</code>（工作区切换与快照包）、<code>23-directory-replica-recovery</code>（目录副本释放、重开与进程恢复） |
| `workspace.protection` | <code>13-protection-policy</code>（工作区保护策略与仓库验证）、<code>23-directory-replica-recovery</code>（目录副本释放、重开与进程恢复） |

## 场景到能力

| 场景 | 标题 | 需求 | 能力 |
|---|---|---|---|
| <code>01-offline-first-start</code> | 干净数据目录离线首次启动 | 真实发布包在独立数据目录启动，WPF、Python、PocketBase、WebView2 与 renderer 全部就绪；renderer 不直接外联，VibeTable 自有进程仅使用 loopback。 | `workspace.lifecycle`、`offline.start`、`release.smoke` |
| <code>02-all-field-schema</code> | Schema v2 字段家族与稳定身份 | 宿主创建空表并分配不透明表身份；通过统一 Field Settings v2 能力、计划和应用创建全部普通字段家族，同类型显示变更不改变字段身份；select label/color 修改不改记录内 optionId；Ctrl+Z 只撤销数据编辑、不撤销字段；停用与恢复经过回收站，renderer 无法调用旧通用 schema 写入口。 | `schema.v2`、`release.smoke` |
| <code>03-schema-errors</code> | 前端与服务端 typed diagnostic | 统一字段抽屉在本地阻止无效草稿；服务端拒绝无效 v2 字段意图并返回稳定错误码；旧 schema.validate 路由在 renderer 边界不可达。 | `schema.v2`、`contract.diagnostics` |
| <code>04-json-round-trip</code> | JSON 编辑、筛选、粘贴、导入与导出不变 | JSON 值经结构化编辑、剪贴板粘贴、host picker 导入和导出后，与权威查询做规范化深比较并保持完全一致。 | `data.json`、`data-io.round-trip` |
| <code>05-formula-lifecycle</code> | 空表转换与非空迁移故障回滚 | 空表字段按冻结计划直接完成类型转换，不启动 shadow migration，并保持 fieldId 与公开 physicalName 不变；非空表启动真实 shadow migration，copying 阶段故障后回滚并保留旧字段身份、类型和值。 | `schema.v2`、`formula.recalculation` |
| <code>06-relation-fanout</code> | 双向关联字段编辑、冻结计划与重开 | 从真实字段设置修改两端名称、基数、显示字段与共享 setNull/restrict 策略，冻结计划展示两端并原子应用；重开后定义和链接身份保持。many→one 多链接冲突及公共 cascade 输入明确拒绝且不改变两端权威状态；内部迁移 cascade 能力保留。 | `schema.v2`、`relation.pair-edit`、`contract.diagnostics` |
| <code>07-attachment-history</code> | 附件全生命周期与历史恢复 | 通过 host picker 上传和替换附件，验证实际预览产物字节长度与 SHA-256；保留历史抽屉的 Workspace 恢复，并经公开 Product 桥接预览和应用另一修订，核对预览不改当前附件、五字段返回及恢复后的附件身份和长度。 | `attachment.history`、`history.restore` |
| <code>08-stale-conflict</code> | 两次过期编辑显示明确冲突 | 两个基于同一旧版本的编辑中，后提交者看到可操作的显式冲突，且不会静默覆盖。 | `mutation.conflict`、`release.smoke` |
| <code>09-atomic-import-scale</code> | 粘贴或导入中途失败无半提交 | 1,000 行单事务导入在中途故障后，业务记录、审计、幂等键和 outbox 均严格为零。 | `data-import.atomic` |
| <code>10-sse-reconnect</code> | SSE 断线重连且不重复应用 | 真实 sidecar 断开后 UI 自动追赶且事件只应用一次；精确终止打包 BFF 后通过 workspace 关闭/重开恢复，轮换 session epoch，并拒绝旧 epoch 写入。 | `realtime.reconnect`、`workspace.lifecycle` |
| <code>11-plugin-mutation</code> | 插件 mutation plan 与越权拒绝 | 插件操作先显示 mutation plan；授权变更成功，未授权字段或能力被拒绝并写入审计。 | `plugin.mutation` |
| <code>12-backup-consistency</code> | 工作区快照恢复一致性 | 从当前版本界面创建并恢复工作区快照；恢复后业务数据、附件和行历史精确回到快照权威状态，同时保留快照之后产生的不可回滚审计记录与外部 ledger 链；派生搜索 generation 必须失效并重建后重新命中恢复附件。 | `snapshot.restore`、`attachment.history`、`history.restore`、`workspace-search.rebuild`、`audit.ledger` |
| <code>13-protection-policy</code> | 工作区保护策略与仓库验证 | 通过真实 Settings UI 执行 repository.verify、读取并更新 retention policy、预览 cleanup，并仅在计划确认为零删除时一次性执行 retention.apply；过期 policy revision 必须稳定拒绝，direct workspace 不伪造 replica，Apply 必须返回零删除数与零回收字节数。 | `workspace.protection` |
| <code>14-document-diff</code> | 真实文件历史版本比较 | 通过 host-only picker 导入真实 TXT 历史版本，以真实 restore revision 建立当前版本后，从 FileRevisionTree 的“与当前版本比较”执行 closed document.diffRequested；验证本地化 identical 结果、两阶段 effective CAS 的 stale 失败，以及 renderer 原始 fileHistory.materializeDiffPair 请求被拒绝。 | `file-history.diff` |
| <code>15-workspace-snapshot-package</code> | 工作区切换与快照包 | 通过真实 Workspace Center 创建并打开第二个工作区，再由 switcher 完成工作区切换并拒绝旧 session epoch；通过真实 Snapshot UI 创建、open-as-new、导出与导入快照包，损坏快照包必须稳定失败。 | `workspace.lifecycle`、`snapshot.package` |
| <code>16-dashboard-lifecycle</code> | Dashboard 可视化、筛选与冲突闭环 | 通过真实 Dashboard UI 创建并保存四类面板，验证键盘布局；配置仅绑定记录面板的枚举全局筛选，重开后由公开读取契约确认定义与字段绑定持久化，再经真实 FilterBar 筛选和清空；图表选择继续驱动联动筛选与钻取，竞争公开写入产生可见 CAS 冲突并显式重载权威 revision。 | `dashboard.lifecycle`、`dashboard.visualization`、`dashboard.filtering`、`dashboard.drilldown`、`dashboard.conflict`、`release.smoke` |
| <code>17-interface-lifecycle</code> | Interface 构建、运行、重启与删除 | 通过真实 Interface UI 创建空白界面、添加元素、修改内容、保存、切换页面后重开并进入运行模式，并验证插件动作的确认、拒绝与取消，精确重启 sidecar 后以 fresh list/load 验证完整定义和 revision 持久，最后真实 UI 删除并验证 list 缺失与 load not_found；证明构建器和运行时消费同一原子定义及既有插件任务生命周期。 | `interface.lifecycle`、`interface.runtime`、`plugin.action.lifecycle` |
| <code>18-workspace-search</code> | 内容、文件关联与统一搜索闭环 | 通过真实内容 UI 配置并编辑 ContentProfile 记录，经 host picker 导入 Markdown/JSON 文件并验证 FileDocument 元数据 AND/OR；建立显式 RecordDocumentLink，unlink 后显示 broken 并修复到另一文档，精确重启 sidecar 后重开仍一致；统一搜索重建后由键盘查询 records/files/attachments、metadata/content/current/history，并对 stale open 显式重解析。 | `workspace-search.query`、`workspace-search.rebuild`、`content.record`、`file-history.query`、`record-document-link.lifecycle`、`attachment.search` |
| <code>19-gallery-lifecycle</code> | Gallery 创建、重开与冲突恢复 | 通过真实 Tables UI 创建并配置 Gallery，展示两条权威记录与空封面占位；离开后重新进入并选择持久视图；竞争保存造成 preset CAS 冲突，显式重载后采用权威获胜 revision 且仍保持 Gallery。 | `gallery.lifecycle`、`preset.conflict` |
| <code>20-kanban-lane-drag</code> | Kanban 单选泳道拖拽持久化 | 通过真实 Tables UI 创建并配置以单选字段分组的 Kanban；泳道显示 label 但拖拽只提交稳定 optionId，host 权威提交后移动卡片；刷新以及离开 Tables 后重开仍保持移动结果。 | `kanban.lifecycle`、`mutation.authority` |
| <code>21-calendar-date-move</code> | Calendar 日期拖动持久化 | 通过真实 Tables UI 创建 date 字段并配置 Calendar；把权威记录拖到目标日期后只经既有 mutation authority 提交，刷新以及离开 Tables 后重开仍保持目标日期。 | `calendar.lifecycle`、`mutation.authority` |
| <code>22-timeline-date-move</code> | Timeline 单日期拖动持久化 | 通过真实 Tables UI 创建 date 字段并配置无结束字段的 Timeline；把权威 point 记录拖到目标日期后只经既有 mutation authority 提交，刷新以及离开 Tables 后重开仍保持目标日期。 | `timeline.lifecycle`、`mutation.authority` |
| <code>23-directory-replica-recovery</code> | 目录副本释放、重开与进程恢复 | 通过真实 Workspace Center 创建目录镜像工作区，并在真实 Tables UI 写入表与记录；公开释放活动缓存后以同一 workspace UUID 重开，等待 database.opened，再以单次 query.page 与 replica.status 验证目录副本；精确终止 sidecar 并等待同 session 的 replacement database.opened，最后以单次查询和状态读取证明数据与副本状态保持一致。 | `workspace.lifecycle`、`workspace.protection`、`replica.recovery` |
| <code>26-lookup-definition-read</code> | Lookup 持久定义读取 | 通过真实字段规划创建关联与 Lookup，再经打包 Product 桥接读取持久定义，核对目标字段、关系路径和输出类型，并与 schema.describe 的 Lookup revision 保持一致。 | `lookup.definition-read` |
| <code>27-relation-target-search</code> | 关系目标搜索 | 真实关系编辑器验证目标搜索的50/51分页、Unicode、空结果与清空恢复；不提交关联写入。 | `relation.search` |
| <code>28-relation-delta-preview</code> | 多值关系预览与取消 | 真实多值关系编辑器经预览加载权威已关联目标；增加本地草稿选择后取消，源与目标表记录及schema/data revision保持不变。 | `relation.preview` |
| <code>29-lookup-source-pagination</code> | Lookup 来源分页读取 | 通过真实字段规划与既有 mutation 建立101条关联来源，打开Lookup来源面板核对首100条，真实点击加载更多后核对101条唯一Unicode来源与分页耗尽，并比较两表权威记录及schema/data revision保持不变。 | `lookup.source-pagination` |
| <code>30-query-snapshot-validation</code> | 查询快照只读校验 | 真实 Product bridge 校验 query.page 生成的快照，覆盖省略与传入当前查询的有效结果、query_changed、实际 mutation 后的 application_write 和字段变更后的 schema_changed；逐次比较权威记录与 revision，校验过程保持零写入。 | `schema.query` |
| <code>31-relation-pair-inspection</code> | 关系完整性只读分页检查 | 通过真实字段设置检查101条来源与一个反向目标，跨两页累计端点进度且不把覆盖完整误报为健康；检查前后权威记录与revision零写入保持，页间实际mutation后续页拒绝并提示重新检查，重新检查可完成。 | `relation.integrity-inspection` |
| <code>32-shared-work-calendar</code> | 工作区共享工作日历 | 通过真实设置页保存工作区假日，首页和实际网格日期编辑器显示相同已确认规则；创建并打开B证明隔离，再正常重开A证明PB持久化，清除规则仍推进revision。S24目录副本消费证据待独立实现合入后追加。 | `workspace.calendar` |
| <code>33-host-grid-presentation</code> | Host 网格呈现保存与恢复 | 第一真实 Host 通过关键词、密度、列宽拖动、排序、冻结、隐藏和完整 OR/空值过滤控件保存，经正常退出后第二真实 Host 使用相同 local-data 与 workspace UUID 从既有工作区卡片选择原表；两阶段分别保留 CDP、control、readiness 和 lifecycle 证据，并由 Host get 与真实 UI 核对完整呈现状态恢复。 | `grid.state` |
| <code>34-relation-lookup-data-io</code> | Relation 导入与 Lookup 文本导出 | 复用固定 corpus，通过真实 Host 文件选择授权与公开 Product bridge 按唯一 Code 导入稳定关系 ID，拒绝无匹配和非唯一匹配；CSV 导出 Lookup 的中文、公式样文本与空关系，排除计算列导入并拒绝直接写入，核对拒绝及导出前后两端权威记录和 revision 不变。当前 UI 尚无匹配配置和 Lookup 导出选列，本场景验证打包协议。 | `relation.import`、`lookup.export` |
