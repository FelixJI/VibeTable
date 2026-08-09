# 能力闭环矩阵

> 实施基线：`GitHub/main@bd06158e`。最终打包候选报告
> `build/qa/p21/20260809T125151Z/product-e2e-report.json` 为 16/16 passed、0 failed、0 skipped；
> 每个场景均无未确认 bridge failure/pending request，且正常退出、子进程清理和端口释放通过。
> `Closed` 仍只用于 producer、Host/allowlist、Web consumer、capability 与产品 E2E 均有证据的范围。

| 能力 | Producer | WPF Host / allowlist | Web consumer | capability 条件 | 产品 E2E | 当前状态与阶段 0 决策 |
|---|---|---|---|---|---|---|
| Workspace lifecycle | `sidecar/internal/workspacev2/runtime.go` | `MainWindow.Product.cs`、`WorkspaceRequestDispatcher.cs` | `workspaceV2HostAdapter.ts`、session store | `workspace.session.v2` 和 v2 method 闭集 | `01-offline-first-start`、`15-workspace-snapshot-package`：create/open/switch 与旧 epoch 拒绝 | **Closed（本轮范围）**；relink 是条件入口，仍未纳入 Closed 范围。 |
| Snapshot timeline / package / open-as-new | `workspacev2/snapshot_*`、`snapshotpkg/*` | `WorkspaceV2HttpGateway.cs`、snapshot broker | Settings/Workspace Center snapshot UI | `snapshot.timeline.v2`、`snapshot.open-as-new.v2` 与 method 集 | `12-backup-consistency`、`15-workspace-snapshot-package`，含损坏包拒绝 | **Closed**；创建、恢复、export/import、open-as-new 已形成真实纵切。 |
| FileHistory / Conflict | `workspacev2/filehistory_handlers.go`、conflict handlers | dispatcher 与 v2 gateway | File workspace、revision history UI | `fileHistory.tree.v2`、history/conflict methods | `07-attachment-history`、`08-stale-conflict`、`14-document-diff` | **Closed**；history restore、过期编辑拒绝和 revision CAS 均有产品证据。 |
| Retention / Repository | `workspacev2/retention_*`、`repository_*` | `MainWindow.Product.cs` capability 合成/dispatcher | `WorkspaceProtectionSettings.vue` | `retention.policy.v2`、`repository.verify` | `13-protection-policy` | **Closed（当前广告范围）**；策略更新、旧 revision 拒绝和 repository verify 已验证。 |
| Replica synchronize | `workspacev2/replica_*` | capability/dispatcher | mirrored-only protection UI | `replica.synchronize` | direct workspace 场景证明入口不被误广告；无 mirrored workspace 产品运行 | **Implemented, unverified**；不把 direct 模式证据伪报为同步闭环。 |
| Plugin lifecycle | plugin producer | `JsonRpcPluginGateway.cs` | plugin bridge 与 Plugins 页面 | plugin allowlist | `11-plugin-mutation`：安装、授权/拒绝 mutation、disable/enable、无效升级失败且旧版本保留 | **Closed（本轮范围）**；真实第二版本升级成功/回滚不在现有 fixture 范围。 |
| Preset / version | preset/version producer 与 RPC | Host/plugin method 闭集 | `PresetVersionPanel.vue` 存在，但全仓无产品挂载点 | 无可见导航或 capability 广告 | 无；组件测试不计 E2E | **Hidden**；provider 保留，本轮不新增 UI，不把未挂载组件伪报为用户能力。恢复开发后若决定公开，必须作为新的完整纵切重新进入矩阵。 |
| Dashboard | dashboard producer，经 workspace v2 coordinated business write | `JsonRpcDashboardGateway.cs` | `nav-dashboard`、Dashboard workspace | Dashboard host feature gate，默认开启 | `16-dashboard-lifecycle`：create/save、离开后重进、选择与 refresh | **Closed（生命周期范围）**；双编辑器 revision conflict 仍只有相邻集成/组件证据。 |
| Document Diff | Host-only materialize/assert RPC、streaming diff/OpenXml engine | strict WPF adapter/coordinator | Web generation/cancel/tree UI | closed bridge；raw materialize 拒绝 | `14-document-diff`：历史两 revision、identical、stale CAS 与 raw route 拒绝 | **Closed**；只承诺受预算约束的可见文本摘要。 |

## 实施后固定边界

- 首版广告范围已按真实产品证据收口；`Replica synchronize`、workspace relink、Dashboard 双编辑器冲突继续明确标注未验证，不因相邻能力通过而扩大结论。
- Preset/version 经产品树复核确认从未挂载，保持 Hidden；若未来公开，必须作为新的完整纵切进入矩阵。
- 真实产品证据仅指 `tests/e2e/product_e2e_runner.py` 启动打包 WPF host、附着真实 WebView2 CDP 后生成的场景报告；unit、integration 与组件测试只作支撑证据。
