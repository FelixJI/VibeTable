# PocketBase 实施验收证据

本文件把实施计划第 12～16 节的发布门槛映射到可重复执行的测试。它不是人工勾选清单。第 12.5 节的 12 个场景必须由真实发布包的 WPF Host、其 WebView2 renderer、Python broker 和 PocketBase sidecar 联合执行；语言单元测试和底层集成测试只提供补充证据，不能替代产品 E2E。

## 12 个产品场景

场景清单由 `tests/e2e/pocketbase_product_scenarios.json` 固定，具体交互在 `tests/e2e/webview_product_scenarios.mjs`。所有场景使用同一个 `qa/product_acceptance.py` 编排器，不存在跳过或 `CAPABILITY_MISSING` 占位。

| 场景 | 真实产品交互与强制断言 |
|---|---|
| 1. 干净机离线首次启动 | 每个场景使用独立空数据目录启动发布包；从等待 CDP 之前开始采样 WPF Host 及其全部可观察子进程的 TCP endpoint，同时在附着现有 WebView2 page 后立即监听 renderer 请求；断言 VibeTable 自有进程无非 loopback listener/remote endpoint、renderer 无外部 HTTP(S) 请求。`msedgewebview2.exe` 属于 Microsoft Evergreen Runtime，其后台 endpoint 原样留证并单独归类，但不伪装成产品请求，也不作为“可断网启动”的失败条件。 |
| 2. 全字段家族与约束建表 | 从建表 UI 创建 scalar、JSON、formula、attachment、relation、lookup、system 字段；实际填写并提交 required/nullable/unique/range/length/pattern、带独立 value/displayName 及单/多选上下限的 enum、default、JSON Schema、MIME/thumbnail/protected、relation/lookup，以及普通、唯一和组合索引。等待 `schema.validate`/`schema.apply` 完成后，通过只读 `schema.getTable` 回读 normalized definition，逐项断言约束、策略与索引确已持久化，再断言网格渲染全部字段和精确的本地化零行状态。 |
| 3. 前置与服务端同路径 schema 错误 | UI 先显示 `fields[0].formula.source`；随后通过封闭的 `schema.validate` 产品消息验证服务端返回同一路径。 |
| 4. JSON round-trip | 在结构化编辑器保存对象，走剪贴板粘贴预览、Host 拥有的固定 picker 导入、网格筛选、Host picker 导出；解析实际导出的 CSV，对编辑、粘贴、导入、权威 `query.page` 与导出值执行对象键规范化后的深比较，而不只搜索字符串片段。 |
| 5. 公式生命周期 | 建表 UI 调用权威 `formula.preview`，保存后编辑依赖字段并观察重算，另建循环依赖并断言产品错误面。 |
| 6. relation fanout | UI 选择 relation 候选，观察跨记录公式；修改目标记录后重新进入依赖表，断言公式已重算。 |
| 7. 附件与历史 | renderer 只发意图，由 Host 拥有的固定 picker 选择上传/替换源；执行真实 shell 预览并读取隔离预览目录中的产物，验证长度与 SHA-256 后保存证据副本，再从历史抽屉恢复旧 revision 并核对原始摘要。 |
| 8. stale conflict | 保持一个网格编辑器打开，经同一封闭产品桥提交竞争写入，再提交旧 digest，断言明确的可见冲突。 |
| 9. 原子 import | 通过 UI 启动单个 MutationKernel 上限边界的 1,000 行导入；测试专用 `after_record` 屏障在同一数据库事务内明确通知编排器，编排器只终止当前 WPF 子树中报告该屏障的精确 `vibetable-pb.exe` PID。恢复后除 UI/历史查询外，编排器还以只读 SQLite URI 读取隔离 `data.db`，同时断言业务记录、审计、幂等键和 outbox 全部为零。25k 是分页查询规模门槛，不是单事务导入大小。 |
| 10. SSE 重连 | 创建一条基线记录后精确终止 sidecar；始终保持当前表选中且不重选、不手动刷新。Host 自动恢复后，经同一封闭产品桥提交第二条记录，等待重连 SSE 自动刷新当前网格，并断言两条逻辑记录各出现一次。 |
| 11. 插件 mutation plan | 通过固定 native picker 安装确定性插件；授权 plan 必须确认并落一条记录，未声明字段 plan 必须被拒绝且不能落第二条。 |
| 12. 备份一致性 | UI 建立记录、公式和附件并创建备份；变更后从设置页恢复，断言记录、公式和附件元数据/摘要回到快照状态；对历史页的 collection/scope/changeSets/total/capabilityHash/schemaRevision/hasMore 做规范化深比较。当前设计未规定备份恢复额外写入表审计，因此允许的额外恢复事件集合为空，任何多余 change set 都失败。 |

`tests/e2e/product_e2e_runner.py` 只启动发布包中的 WPF 可执行文件。Node 客户端只能调用 `chromium.connectOverCDP` 附着到该 WPF 进程创建的 WebView2；代码和测试都禁止 `chromium.launch` 与 `launchPersistentContext`，因此不能用替代浏览器伪造桌面产品验收。

这里的“离线”指产品在没有可用互联网连接时仍能首次启动并完成本地数据工作流，而不是声称宿主能够通过受支持的 WebView2 API 禁止 Microsoft Runtime 自身尝试后台连接。产品页面由 `connect-src 'none'`、WebView 请求监听和 typed HostBridge 共同约束；需要外联的每日一句或插件网络能力只能由显式授权的 Host 路径发起。

每个场景保存：

- 发布包完整性、新鲜度、逐文件 SHA-256 和整体 SHA-256；
- WPF/sidecar/broker/renderer readiness、启动参数与独立数据目录；
- Playwright trace、全页截图、浏览器 console/page error、网络请求；
- Host stdout/stderr、Node runner stdout/stderr；
- 故障请求及被精确终止的 PID/进程名；
- 场景结构化断言和总报告，失败为非零退出，`skipped` 固定为零。

## UI 与可访问性成品门槛

界面验收以发布包中的真实 WebView2 为准，并保持飞书式蓝灰、克制留白和紧凑信息密度：

- 新建后的零行表必须出现本地化的可行动空状态，不能只显示一块空白网格；
- attachment、relation、JSON 等结构化单元格必须可由键盘聚焦，并支持 Enter/Space 打开、Shift+F10 打开上下文菜单；真实 WebView2 场景会读取 `aria-keyshortcuts` 并用键盘实际触发 JSON 模态框和上下文菜单；
- 关系编辑、附件、JSON、备份恢复确认必须是真实模态交互，具备 Esc 关闭、focus trap 与触发点焦点恢复；JSON 场景会按 Esc 后直接断言 `document.activeElement` 回到原单元格；
- 暗色场景必须验证浏览器计算得到的 `color-scheme: dark`，并对表格、单元格、模态框、弹出菜单逐项检查合成背景亮度与文字对比度，普通文字最低为 4.5:1；
- 中英文切换必须即时重建已打开表格的筛选占位符、可操作名称和空状态，不允许保留旧语言；真实 WebView2 会在同一张打开的表上依次切到英文和中文，断言筛选占位符及 JSON 单元格可访问名称均已重建；
- 899px 以下不得因侧栏收起而失去表切换入口；场景 2 会把真实 WebView2
  viewport 收窄到 720px，通过紧凑工具栏切换到另一张表，并保存
  `02-narrow-table-switcher.png`；
- 每个场景继续要求 console warning/error、page error 与外部 HTTP(S) 请求均为零。

场景 2 的最终截图提供空表视觉证据；场景 12 额外保存
`12-dark-table.png`、`12-dark-modal.png`、`12-dark-popover.png`，用于人工检查暗色表面、层级和密度。

## 规模与性能门槛

以下测试使用真实 PocketBase/SQLite 数据目录：

- `TestQueryPortPagesFiltersAndSortsTwentyFiveThousandRows`：25,000 行数据、12,500 行命中、500 行分页，逐页验证筛选及降序连续性。
- `TestFormulaBackfillTenThousandRowsCancelsResumesWithoutDuplicateAudit`：10,000 行 backfill，在首批提交后取消并恢复，最终严格验证 10,000 条审计、100 个幂等批次。
- `TestMutationKernelOneThousandOperationsCommitOrFullyRollback`：1,000 个操作在 `before_commit` 故障下业务记录/审计/幂等/outbox 全部为零，随后同规模完整提交。
- `TestSingleRecordFormulaPreviewRecordsP50AndP95`：包含 compile + evaluate 的单记录预览，记录 p50/p95，并以 p95 100ms 为本地门槛。

2026-07-24 本机基线（Windows，非独占 CI 主机）：

- 25k 分页/筛选/排序：约 491ms；
- 10k backfill 取消/恢复：约 9.55s；
- 1k 回滚后完整提交：约 0.76s；
- 公式预览：p50 约 0.10ms，p95 约 0.58ms。

时间只用于回归观察；正确性断言（数量、顺序、游标、审计与幂等性）是强制门槛。

## 故障与恢复门槛

- Mutation fault points：`after_record`、`after_audit`、`after_outbox`、`before_commit` 全部验证事务回滚。
- schema/migration：不完整 internal collection、错误字段类型、损坏 revision metadata 均 fail closed。
- backup：marker 严格 JSON、SHA-256、归档 `data.db`、部分目录移动回滚、恢复前安全副本。
- attachments：上传失败回滚、MIME/大小限制、hash mismatch、missing metadata、orphan 仅报告隔离。
- host：异常退出自动重启、重启上限、health/identity 不一致、非 loopback readiness、显式停止取消恢复。
- launch：只绑定内核分配的 IPv4 loopback 端口；非法/未分配端口拒绝。

## 一键验收

```powershell
.venv\Scripts\python.exe qa\next.py --ci --json-report build\qa\next-report.json
.venv\Scripts\python.exe scripts\build_next.py --release
.venv\Scripts\python.exe qa\package_check.py dist\VibeTable.Next
.venv\Scripts\python.exe qa\product_acceptance.py `
  --package-root dist\VibeTable.Next `
  --evidence-root build\qa\product-e2e
```

`qa/next.py` 已把 `qa/product_acceptance.py` 纳入产品验收门禁。编排器在启动任何场景前再次执行发布包检查，并验证桌面、Web、Python backend 和 sidecar 构件都不早于对应生产源码；陈旧发布包会让所选场景全部严格失败而不是静默使用旧二进制。

发布包检查同时验证 sidecar、migration manifest/hash、contract version、SHA-256、Go build info、SBOM 和完整第三方许可证文本；任何未解析许可证都会失败。

## 2026-07-25 本机最终证据

- 完整门禁：`build/qa/final-next-summary-6.json`，19/19 阶段通过，`ok=true`、`releaseEligible=true`，源码摘要为 `1b9f8d6cde8da3ea4b95b557e17504b20d91d64897135ff67d5487b4ac156b0a`。
- 完整门禁内的真实产品 E2E：`build/qa/product-e2e/20260725T112948Z/product-e2e-report.json`，12/12 通过，失败与跳过均为零。
- 门禁后针对开发启动隔离、稳定 WebView2 profile、插件调度与终态通知丢失兜底做了小范围修正；对应 Python 插件运行时/Worker 测试为 13/13，通过场景 11 的独立证据位于 `build/qa/focused-plugin-terminal-reconcile-final/20260725T115720Z/product-e2e-report.json`。
- 对最终当前发布包重新执行了全部 12 个真实 WPF/WebView2 产品场景：`build/qa/post-gate-product-e2e-final/20260725T115752Z/product-e2e-report.json`，12/12 通过，失败与跳过均为零；发布包摘要为 `b2b168355f150c3b28452322846e26a5800e65d3ffd47c8a35c8863eba90df0e`。

完整门禁摘要对应门禁执行时的源码；门禁后的修正没有伪装成同一源码摘要，而是由上述定向测试、重新打包的新鲜度检查及最终 12/12 产品回归覆盖。
