# 产品 E2E 能力索引

> 本文件由 `tests/e2e/pocketbase_product_scenarios.json` 确定性生成；
> 请运行 `uv run python scripts/generate_product_e2e_capability_index.py --write` 更新，
> 不要手工编辑。这里的 capability 是 E2E selector tag，不等同于 Host/runtime 广告能力；
> 本索引只描述 manifest 声明的覆盖范围，不代表某次运行已通过。

## 当前声明范围

- 场景：19
- 唯一能力：32
- 场景—能力关联：41
- `release.smoke` 场景：4

## 能力到场景

| 能力 | 场景 |
|---|---|
| `attachment.history` | <code>07-attachment-history</code>（附件全生命周期与历史恢复）、<code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `attachment.search` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `audit.ledger` | <code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `content.record` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `contract.diagnostics` | <code>03-schema-errors</code>（前端与服务端 typed diagnostic） |
| `dashboard.lifecycle` | <code>16-dashboard-lifecycle</code>（Dashboard 创建、保存与刷新） |
| `data-import.atomic` | <code>09-atomic-import-scale</code>（粘贴或导入中途失败无半提交） |
| `data-io.round-trip` | <code>04-json-round-trip</code>（JSON 编辑、筛选、粘贴、导入与导出不变） |
| `data.json` | <code>04-json-round-trip</code>（JSON 编辑、筛选、粘贴、导入与导出不变） |
| `file-history.diff` | <code>14-document-diff</code>（真实文件历史版本比较） |
| `file-history.query` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `formula.recalculation` | <code>05-formula-lifecycle</code>（空表转换与非空迁移故障回滚） |
| `gallery.lifecycle` | <code>19-gallery-lifecycle</code>（Gallery 创建、重开与冲突恢复） |
| `history.restore` | <code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `interface.lifecycle` | <code>17-interface-lifecycle</code>（Interface 构建、运行与重开） |
| `interface.runtime` | <code>17-interface-lifecycle</code>（Interface 构建、运行与重开） |
| `mutation.conflict` | <code>08-stale-conflict</code>（两次过期编辑显示明确冲突） |
| `offline.start` | <code>01-offline-first-start</code>（干净数据目录离线首次启动） |
| `plugin.action.lifecycle` | <code>17-interface-lifecycle</code>（Interface 构建、运行与重开） |
| `plugin.mutation` | <code>11-plugin-mutation</code>（插件 mutation plan 与越权拒绝） |
| `preset.conflict` | <code>19-gallery-lifecycle</code>（Gallery 创建、重开与冲突恢复） |
| `realtime.reconnect` | <code>10-sse-reconnect</code>（SSE 断线重连且不重复应用） |
| `record-document-link.lifecycle` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `relation.fanout` | <code>06-relation-fanout</code>（关系 cascade 方向与影响预览） |
| `release.smoke` | <code>01-offline-first-start</code>（干净数据目录离线首次启动）、<code>02-all-field-schema</code>（Schema v2 字段家族与稳定身份）、<code>08-stale-conflict</code>（两次过期编辑显示明确冲突）、<code>16-dashboard-lifecycle</code>（Dashboard 创建、保存与刷新） |
| `schema.v2` | <code>02-all-field-schema</code>（Schema v2 字段家族与稳定身份）、<code>03-schema-errors</code>（前端与服务端 typed diagnostic）、<code>05-formula-lifecycle</code>（空表转换与非空迁移故障回滚） |
| `snapshot.package` | <code>15-workspace-snapshot-package</code>（工作区切换与快照包） |
| `snapshot.restore` | <code>12-backup-consistency</code>（工作区快照恢复一致性） |
| `workspace-search.query` | <code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `workspace-search.rebuild` | <code>12-backup-consistency</code>（工作区快照恢复一致性）、<code>18-workspace-search</code>（内容、文件关联与统一搜索闭环） |
| `workspace.lifecycle` | <code>01-offline-first-start</code>（干净数据目录离线首次启动）、<code>10-sse-reconnect</code>（SSE 断线重连且不重复应用）、<code>15-workspace-snapshot-package</code>（工作区切换与快照包） |
| `workspace.protection` | <code>13-protection-policy</code>（工作区保护策略与仓库验证） |

## 场景到能力

| 场景 | 标题 | 需求 | 能力 |
|---|---|---|---|
| <code>01-offline-first-start</code> | 干净数据目录离线首次启动 | 真实发布包在独立数据目录启动，WPF、Python、PocketBase、WebView2 与 renderer 全部就绪；renderer 不直接外联，VibeTable 自有进程仅使用 loopback。 | `workspace.lifecycle`、`offline.start`、`release.smoke` |
| <code>02-all-field-schema</code> | Schema v2 字段家族与稳定身份 | 宿主创建空表并分配不透明表身份；通过统一 Field Settings v2 能力、计划和应用创建全部普通字段家族，同类型显示变更不改变字段身份；select label/color 修改不改记录内 optionId；Ctrl+Z 只撤销数据编辑、不撤销字段；停用与恢复经过回收站，renderer 无法调用旧通用 schema 写入口。 | `schema.v2`、`release.smoke` |
| <code>03-schema-errors</code> | 前端与服务端 typed diagnostic | 统一字段抽屉在本地阻止无效草稿；服务端拒绝无效 v2 字段意图并返回稳定错误码；旧 schema.validate 路由在 renderer 边界不可达。 | `schema.v2`、`contract.diagnostics` |
| <code>04-json-round-trip</code> | JSON 编辑、筛选、粘贴、导入与导出不变 | JSON 值经结构化编辑、剪贴板粘贴、host picker 导入和导出后，与权威查询做规范化深比较并保持完全一致。 | `data.json`、`data-io.round-trip` |
| <code>05-formula-lifecycle</code> | 空表转换与非空迁移故障回滚 | 空表字段按冻结计划直接完成类型转换，不启动 shadow migration，并保持 fieldId 与公开 physicalName 不变；非空表启动真实 shadow migration，copying 阶段故障后回滚并保留旧字段身份、类型和值。 | `schema.v2`、`formula.recalculation` |
| <code>06-relation-fanout</code> | 关系 cascade 方向与影响预览 | 关系字段切换 cascade 前，冻结计划明确标记危险级别，并返回受影响记录与依赖信息。 | `relation.fanout` |
| <code>07-attachment-history</code> | 附件全生命周期与历史恢复 | 通过 host picker 上传和替换附件，验证实际预览产物字节长度与 SHA-256，并从历史恢复旧版本。 | `attachment.history` |
| <code>08-stale-conflict</code> | 两次过期编辑显示明确冲突 | 两个基于同一旧版本的编辑中，后提交者看到可操作的显式冲突，且不会静默覆盖。 | `mutation.conflict`、`release.smoke` |
| <code>09-atomic-import-scale</code> | 粘贴或导入中途失败无半提交 | 1,000 行单事务导入在中途故障后，业务记录、审计、幂等键和 outbox 均严格为零。 | `data-import.atomic` |
| <code>10-sse-reconnect</code> | SSE 断线重连且不重复应用 | 真实 sidecar 断开后 UI 自动追赶且事件只应用一次；精确终止打包 BFF 后通过 workspace 关闭/重开恢复，轮换 session epoch，并拒绝旧 epoch 写入。 | `realtime.reconnect`、`workspace.lifecycle` |
| <code>11-plugin-mutation</code> | 插件 mutation plan 与越权拒绝 | 插件操作先显示 mutation plan；授权变更成功，未授权字段或能力被拒绝并写入审计。 | `plugin.mutation` |
| <code>12-backup-consistency</code> | 工作区快照恢复一致性 | 从当前版本界面创建并恢复工作区快照；恢复后业务数据、附件和行历史精确回到快照权威状态，同时保留快照之后产生的不可回滚审计记录与外部 ledger 链；派生搜索 generation 必须失效并重建后重新命中恢复附件。 | `snapshot.restore`、`attachment.history`、`history.restore`、`workspace-search.rebuild`、`audit.ledger` |
| <code>13-protection-policy</code> | 工作区保护策略与仓库验证 | 通过真实 Settings UI 执行 repository.verify、读取并更新 retention policy、预览 cleanup，并仅在计划确认为零删除时一次性执行 retention.apply；过期 policy revision 必须稳定拒绝，direct workspace 不伪造 replica，Apply 必须返回零删除数与零回收字节数。 | `workspace.protection` |
| <code>14-document-diff</code> | 真实文件历史版本比较 | 通过 host-only picker 导入真实 TXT 历史版本，以真实 restore revision 建立当前版本后，从 FileRevisionTree 的“与当前版本比较”执行 closed document.diffRequested；验证本地化 identical 结果、两阶段 effective CAS 的 stale 失败，以及 renderer 原始 fileHistory.materializeDiffPair 请求被拒绝。 | `file-history.diff` |
| <code>15-workspace-snapshot-package</code> | 工作区切换与快照包 | 通过真实 Workspace Center 创建并打开第二个工作区，再由 switcher 完成工作区切换并拒绝旧 session epoch；通过真实 Snapshot UI 创建、open-as-new、导出与导入快照包，损坏快照包必须稳定失败。 | `workspace.lifecycle`、`snapshot.package` |
| <code>16-dashboard-lifecycle</code> | Dashboard 创建、保存与刷新 | 通过真实 Dashboard UI 创建空白 Dashboard、保存、刷新列表并重新选择，验证持久化后的 Dashboard 仍可打开。 | `dashboard.lifecycle`、`release.smoke` |
| <code>17-interface-lifecycle</code> | Interface 构建、运行与重开 | 通过真实 Interface UI 创建空白界面、添加元素、修改内容、保存、切换页面后重开并进入运行模式，并验证插件动作的确认、拒绝与取消，证明构建器和运行时消费同一原子定义及既有插件任务生命周期。 | `interface.lifecycle`、`interface.runtime`、`plugin.action.lifecycle` |
| <code>18-workspace-search</code> | 内容、文件关联与统一搜索闭环 | 通过真实内容 UI 配置并编辑 ContentProfile 记录，经 host picker 导入 Markdown/JSON 文件并验证 FileDocument 元数据 AND/OR；建立显式 RecordDocumentLink，unlink 后显示 broken 并修复到另一文档，精确重启 sidecar 后重开仍一致；统一搜索重建后由键盘查询 records/files/attachments、metadata/content/current/history，并对 stale open 显式重解析。 | `workspace-search.query`、`workspace-search.rebuild`、`content.record`、`file-history.query`、`record-document-link.lifecycle`、`attachment.search` |
| <code>19-gallery-lifecycle</code> | Gallery 创建、重开与冲突恢复 | 通过真实 Tables UI 创建并配置 Gallery，展示两条权威记录与空封面占位；离开后重新进入并选择持久视图；竞争保存造成 preset CAS 冲突，显式重载后采用权威获胜 revision 且仍保持 Gallery。 | `gallery.lifecycle`、`preset.conflict` |
