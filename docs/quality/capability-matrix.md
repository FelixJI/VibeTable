# 能力闭环矩阵

> 本页人工判断用户能力纵切是否闭环；当前 manifest 声明的 E2E selector tag 与场景映射见
> [产品 E2E 能力索引](product-e2e-capability-index.md)，selector tag 不等同于 Host/runtime 广告能力。
> 历史实施基线 `GitHub/main@bd06158e` 在 2026-08-09 的打包候选报告
> `build/q/f2/20260809T152446Z/product-e2e-report.json` 为 16/16 passed、0 failed、0 skipped；
> 该次每个场景均无未确认 bridge failure/pending request，且正常退出、子进程清理和端口释放通过。
> `Closed` 仍只用于 producer、Host/allowlist、Web consumer、capability 与产品 E2E 均有证据的范围。

| 能力 | Producer | WPF Host / allowlist | Web consumer | capability 条件 | 产品 E2E | 当前状态与阶段 0 决策 |
|---|---|---|---|---|---|---|
| Workspace create/open/switch | `sidecar/internal/workspacev2/runtime.go` | `MainWindow.Product.cs`、`WorkspaceRequestDispatcher.cs` | `workspaceV2HostAdapter.ts`、session store | `workspace.session.v2` 和 v2 method 闭集 | `01-offline-first-start`、`15-workspace-snapshot-package`，含旧 epoch 拒绝 | **Closed**。 |
| Workspace relink | workspace registry/relink producer | Host v2 route 保留；renderer raw request 返回 `CAPABILITY_NOT_PUBLIC` | `WorkspaceCenter.vue` 由 `publicCapabilityPolicy.workspaceRelink=false` 隐藏 | 不向 renderer 公开 | 无 | **Hidden**；保留生产实现供后续恢复开发，重新公开前必须补打包产品证据。 |
| Snapshot timeline / package / open-as-new | `workspacev2/snapshot_*`、`snapshotpkg/*` | `WorkspaceV2HttpGateway.cs`、snapshot broker | Settings/Workspace Center snapshot UI | `snapshot.timeline.v2`、`snapshot.open-as-new.v2` 与 method 集 | `12-backup-consistency`、`15-workspace-snapshot-package`，含损坏包拒绝 | **Closed**；创建、恢复、export/import、open-as-new 已形成真实纵切。 |
| FileHistory / Conflict | `workspacev2/filehistory_handlers.go`、conflict handlers | dispatcher 与 v2 gateway | File workspace、revision history UI | `fileHistory.tree.v2`、history/conflict methods | `07-attachment-history`、`08-stale-conflict`、`14-document-diff` | **Closed**；history restore、过期编辑拒绝和 revision CAS 均有产品证据。 |
| Retention / Repository | `workspacev2/retention_*`、`repository_*` | `MainWindow.Product.cs` capability 合成/dispatcher | `WorkspaceProtectionSettings.vue` | `retention.policy.v2`、`repository.verify` | `13-protection-policy`：策略更新/旧 revision 拒绝、verify、零删除 plan/apply | **Closed**；Apply 只在 fresh workspace 的计划确认为零删除且无阻塞原因时执行。 |
| Replica synchronize | `workspacev2/replica_*` | `ProductionWorkspaceHooks` 是保留 consumer；renderer raw request 返回 `CAPABILITY_NOT_PUBLIC` | mirrored 手动同步按钮由 public policy 隐藏 | 不向 renderer 公开 | raw route 拒绝测试；无 mirrored 产品运行 | **Internal only**；不把内部协调钩子广告成用户能力。 |
| Plugin install/enable/action | plugin producer | `JsonRpcPluginGateway.cs` | plugin bridge 与 Plugins 页面 | public plugin allowlist | `11-plugin-mutation`：安装、授权/拒绝 mutation、disable/enable、无效升级候选拒绝 | **Closed**。 |
| Plugin upgrade/rollback/uninstall mutation | plugin lifecycle producer/service | 内部 catalog 保留；renderer raw request 返回 `CAPABILITY_NOT_PUBLIC` | 成功提交、回滚和卸载入口由 public policy 隐藏 | 不向 renderer 公开 | raw route 拒绝与组件测试；无成功产品 E2E | **Hidden**；检查候选仍可见，成功 mutation 重新公开前必须补完整产品生命周期。 |
| Alternative view / Gallery | `backend/application/insights_service.py` 的 preset producer 与 `ViewQuery` | `JsonRpcProductDataGateway.cs`、`ProductDataRpcRegistry.cs` 的 public preset RPC | `DataSourceViewBar.vue`、`RecordGalleryView.vue`、`presetViewController.ts` | public `preset.list/save` 与 Tables 导航 | `19-gallery-lifecycle`：真实 UI 创建/配置、两条权威记录与空封面占位、离开后重开、stale revision 冲突与显式 reload | **Closed（Gallery 生命周期与冲突恢复范围）**；Kanban/Calendar/Timeline 及拖拽、日期移动仍未取得对应产品证据。 |
| Preset / version | preset/version producer 与 RPC | Host/plugin method 闭集 | `PresetVersionPanel.vue` 存在，但全仓无产品挂载点 | 无可见导航或 capability 广告 | 无；组件测试不计 E2E | **Hidden**；provider 保留，本轮不新增 UI，不把未挂载组件伪报为用户能力。恢复开发后若决定公开，必须作为新的完整纵切重新进入矩阵。 |
| Dashboard | dashboard producer，经 workspace v2 coordinated business write | `JsonRpcDashboardGateway.cs` | `nav-dashboard`、Dashboard workspace | Dashboard host feature gate，默认开启 | `16-dashboard-lifecycle`：四类面板、键盘布局、全局筛选定义/绑定持久化、FilterBar 筛选/清空、图表联动/钻取、竞争公开写入 CAS 冲突与显式重载 | **Closed（场景声明范围）**；双真实编辑器并发仍未取得独立产品证据。 |
| Document Diff | Host-only materialize/assert RPC、streaming diff/OpenXml engine | strict WPF adapter/coordinator | Web generation/cancel/tree UI | closed bridge；raw materialize 拒绝 | `14-document-diff`：历史两 revision、identical、stale CAS 与 raw route 拒绝 | **Closed**；只承诺受预算约束的可见文本摘要。 |

## 实施后固定边界

- 首版广告范围已按真实产品证据收口；workspace relink、手动 Replica synchronize 与 plugin 成功升级/回滚/卸载同时在 UI 和 renderer 路由隐藏，内部 producer 不算用户广告。
- Dashboard 双编辑器冲突继续明确标注未验证，不因相邻生命周期通过而扩大结论。
- Alternative view 只关闭 Gallery 创建、持久重开与 preset 冲突恢复；其他视图种类和交互仍保持未验收。
- Preset/version 经产品树复核确认从未挂载，保持 Hidden；若未来公开，必须作为新的完整纵切进入矩阵。
- 打包生命周期的 sidecar/BFF 证据边界不同：sidecar 异常由 supervisor 自动恢复；BFF 异常通过
  workspace 关闭/重开的受支持产品路径恢复并轮换 session epoch。场景 10 同时证明旧 epoch 写入稳定
  返回 `workspace.session_stale` 且没有改变策略，新 epoch 写入可见。
- 真实产品通过证据仅指 `tests/e2e/product_e2e_runner.py` 启动打包 WPF host、附着真实
  WebView2 CDP 后生成的对应 `required` 场景报告；[产品 E2E 能力索引](product-e2e-capability-index.md)
  只投影当前声明，unit、integration 与组件测试只作支撑证据。
