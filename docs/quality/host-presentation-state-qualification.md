# Host 本机呈现状态完整收敛：设计与资格记录

基线 main `9fa626a13840830037bcb82eaddc0adf8617075b`。本分支处理 PR140 L6 的本机网格呈现状态及真实保存/恢复消费者；与 Go 共享工作日历、Version、Dashboard 等业务 authority 分开。当前仅完成代码盘点，尚未生产切换或测试资格，不创建文档微型PR。

## 已核实际调用

- Web 的 gridState.saveRequested 经 GridRequestController / GridStateCoordinator → LazyProductTableGateway / PocketBaseTableGateway → JsonRpcWorkspaceSupportGateway → 常驻 Python gridState.get/save。该Host调用链存在，但进一步全文核对确认Web只有协议/白名单，尚无真实save发送者；LoadStateAsync也仅测试调用，reconcileState/buildRestorePlan只有测试消费者。必须接通真实列布局保存/恢复，不能只迁薄route。
- Python GridStateService 使用 LocalStateStore 的本机 state SQLite；表布局、排序、过滤、keyword、density、forcedRemote 都是呈现数据，不进入 PocketBase 业务库。get 在首次读取时持久化默认状态；save 实施revision CAS并返回冲突当前值。
- 原 Python DTO 有512列、16sort、64filter、keyword256、width1–4096等边界，C# GridStateContracts 不是这些输入验证的自动替代。切换需显式保留/解释契约，不用类型反序列化代替验证。
- GridStateCoordinator 当前 ExecuteSaveAsync 以CancellationToken.None保存并直接覆盖_confirmedState，LoadStateAsync也未检查await后的workspace/table世代。迁移时必须以延迟完成/切换workspace的真实回归核实，避免把旧请求结果采纳到新表。
- settings.readDevice/saveDevice已有C# DeviceSettingsRequestController/DeviceSettingsStore及workspace epoch lease；存储目前位于当前workspace runtime data/state/device-settings.json，不能重新发明相同Host route并宣布完成。
- 实际主题仍从uiStore的全局vt:theme localStorage读取/写入；useTheme直接消费该值。设备设置route存在不等于主题UI已接线。主题的全局设备语义与网格workspace/table语义须显式分域。

## 待形成的完整实现边界

用小接口隐藏持久化与CAS，让GridStateCoordinator直接调用Host呈现状态服务；JsonRpcWorkspaceSupportGateway仅保留未迁移paste支持，网格不经Python。持久身份使用真实workspace UUID和table ID，不能退回本机目录/展示名称。配置路径必须来自Host既有可信数据根，禁止接受Web任意路径。

在切换前冻结实际旧DTO/service/store语义，且当前新Host回归须覆盖默认读、重开、CAS冲突、输入边界、取消/过期请求和独立workspace/table隔离。不声称旧Python回放就是新Host测试。保留原测试直到迁移方案可审、发现机制和覆盖对应关系明确；当前没有授权绕过任何自动审批拒绝。

主题/其他uiStore偏好是否纳入同一完整呈现状态PR，须先确定现有DeviceSettings DTO与真实消费者范围，避免仅接theme却继续保留第二个主题authority。Shortcuts/command.export.query与Worker依赖另行完整实施，不因此分支宣称L6全部完成。

## 验收

正常hooks、相关C#/Web/Python契约与生成一致性，独立Standards/Spec，完整发布构建；使用真实产品改变网格状态、切表/切工作区/关闭重开，并检查当前状态仍由Host持久恢复。若接主题则同时从真实Settings UI改变并重开验证。fresh CI、严格同步main、squash及postmain CI/CD全部完成后才可写交付通过。

## 恢复顺序的首个已复现缺口

现有buildRestorePlan按schema遍历顺序输出列，忽略ColumnState.order。新增真实reconcile→restore回归同时覆盖已删除列排除、显式顺序、未指定顺序列置后、宽度/冻结/隐藏保留、新列不伪造旧状态以及不修改输入。旧实现1 FAIL/13 PASS（build/qa/host-presentation/column-order-red.log）；稳定排序修复后14 PASS（column-order-green.log），npm run typecheck PASS（typecheck.log）。复用相同lock的已有Node缓存，无依赖升级。

该函数尚未接真实UI，局部回归通过不是产品能力通过；修复保留在本完整Host网格状态分支，未push/PR。后续必须完成Host持久化及实际网格事件采集/重开恢复。

## Host 持久化深模块（尚未接线）

新增HostGridStateStore，以可信Host状态根+workspace UUID分目录，表身份仅作JSON字典key，不进入路径。保留首次get持久默认revision、完整状态保存与CAS冲突返回当前值；保存前固定调用方可变列表，在本实例队列内读取/比较/原子替换。跨Host独占文件句柄失败时直接报IO失败，不覆盖并发写，不添加重试。旧Python本机数据库尚未删除，也没有被当前未接线模块双写；真实Host消费者切换仍待实施。

保存接受当前lease检查回调，在临时文件写入前后检查；取消/失效不发布新文件，临时文件finally清理。损坏文件失败而非自动覆盖默认值。输入沿旧列数/排序/过滤/宽度/keyword等边界并按Unicode codepoint计长度，重复列名新写拒绝；不复制业务行数据，不新增hash。

首次测试仅因HostGridStateStore类型尚不存在而编译失败（host-store-red.log），不能称为旧生产行为RED。实现后3真实文件测试PASS；扩大到临时写入后lease失效、损坏文件不覆盖、完整排序/过滤与9007199254740993保真后5 PASS/45ms；调用方快照完善后5 PASS/42ms（host-store-snapshot-green.log）。使用dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter FullyQualifiedName~HostGridStateStoreTests；首次locked restore已通过。本阶段未双轴/完整质量/新包/真实UI，不能写Host迁移完成。

下一接线需将local://workspace/<UUID>的现有source绑定与真实workspace epoch核对，防止旧表请求在新workspace下保存。GridStateCoordinator保存/加载的请求结果和pending completion须按各自世代处理，不能让旧任务完成新任务；Web必须从真实列操作采集状态，在schema就绪后恢复且避免恢复事件反写。旧Python路由与仅为旧persist使用的LocalStateStore在完整切换时处理，保持既有测试门禁可审。

## Workspace scope 请求模块（待生产 composition/renderer 接入）

新增GridPresentationRequestController独立处理gridState.get/save，直接消费真实WorkspaceSessionEnvelopeFilter lease和HostGridStateStore。新参数为table与save必填state/revision；workspace由scope取得，renderer不能指定database路径。无scope/旧scope拒绝；严格大小写、未知字段、重复JSON字段和缺revision拒绝；错误消息固定，不输出路径。持有lease直到回复结束，关闭排空等待lease释放；过期请求不再回写或向新workspace交付结果。未添加Python fallback。

三项controller回归使用真实WorkspaceSessionManager/Registry/WorkspaceLayout和epoch drain，仅运行时进程启动是测试适配器：验证两个workspace同名table互不污染、旧scope拒绝、损坏DTO不改revision及回复中close必须排空。与存储五项合跑8 PASS/118ms（build/qa/host-presentation/host-controller-tests.log）；初次controller编译时仅重跑原store5项，不能把该记录算controller测试。尚未修改生产HostRequestDispatcher/MainWindow或renderer白名单/owner，当前不宣称生产请求已通。

后续采用独立Host route模块接入既有HostRequestDispatcher，按workspace范围/wpfHost owner进行router能力校验；不为了复用旧GridStateCoordinator而保留无配置时Python fallback。旧saveRequested/debounce链的清理及现有测试对应关系必须在完整切换前说明并验证。

## 旧 Python 网格契约冻结
固定producer9fa626a13840830037bcb82eaddc0adf8617075b，通过git archive隔离backend，在-I子进程运行实际GridState DTO、GridStateService与LocalStateStore。23项包含首次/重复读、完整列布局与大整数filter保存读取、stale/null revision、workspace/table隔离、数据库关闭重开、11项旧输入边界及3项完整JSON schema；仅服务器随机revision按首次出现顺序归一化，保留同一性关系。contracts/v2/grid_state-python-oracle.json首次由脚本生成，后续只读回放，不根据当前C#生成期望。原21个service/store测试未修改或迁移。
首次回放通过；后续类型修正时一次文本编辑误将GridStateResult加入schema枚举，回放精确拒绝差异，现恢复原三schema且冻结文件无变化。Pyright最终0错误，Ruff通过。此基线只说明旧行为，不替代当前Host测试和真实保存恢复消费者。

## Host composition 与路由接入
MainWindow以可信_productDataRoot/grid-presentation构造状态服务，HostRequestDispatcher将gridState.get/save交给新的scope控制器。WebMessageRouter显式允许两个名字，并要求wpfHost/workspace/rendererPublic capability及真实workspace scope；DeviceSettings的global规则保持原样。新真实router回归旧6 FAIL（UNKNOWN_TYPE），接线后router+controller+store相邻62 PASS/139ms，日志router-red.log/router-green.log。默认生成owner仍为旧Python，尚未切换；显式policy测试证明新路由边界，不能写生产默认renderer已可用。下一步必须同步inventory/policy/catalog、退出Python注册和旧saveRequested链，并接真正Web读写恢复。

## 正式 owner 切换与递归状态（当前未提交整合）
Python 不再注册 gridState.get/save；inventory、capability policy 和脚本生成 catalog 已切为 wpfHost/workspace。新 Host DTO 使用递归 FilterExpression、最多三层 group/50 条叶条件，保留大整数；PresetId/Revision 必须同时为空或同时有效，供 Web 判断本机覆盖是否仍属于当前共享基线。原 Python DTO、21 项服务测试和固定 producer 文件均保留。

当前 MainWindow/dispatcher 已直接接 HostGridStateStore。旧 gridState.saveRequested 已从 renderer 白名单退出：真实回归先 FAIL（请求仍被分发），关闭后拒绝 UNKNOWN_TYPE 且不调用 dispatcher。内部历史 coordinator/gateway 方法及其测试暂留，生产新路由不经过它们；这不代表死代码清理或 L6 全部完成。

本次验证：Python owner/oracle/product contracts 31 PASS；Ruff PASS，Pyright 0 errors。递归状态与 Preset 绑定先真实丢失 RED，修复后相关 Host 测试通过。solution --no-restore 命令中 Host 55 项通过，但整体 EXIT1（未还原的 OpenXml.Tests 缺 project.assets.json），不计完整 solution 通过。精确运行已还原的 Desktop.Tests 项目、筛选 GridPresentation/HostGridStateStore/WebMessageRouter 后 68 PASS，EXIT0（owner-routing-project-green.log）。较早四层拒绝用例误构造为合法三层，已修正测试输入为实际四层而不改变生产预算。

Web 作者正在同一分支接统一 Preset/schema/Host 恢复序列、实际网格事件、串行 CAS 与跨 epoch 废弃；当前 104 项聚焦测试通过，完整 Web/typecheck、独立双轴、新包及真实重开场景尚待完成。不得据此写整个 PR 验收通过。

完整 Python 回归：在 cc187f12 后端状态下执行 uv run --frozen --no-sync python -m pytest -q，1873 PASS / 1 SKIP，131.80s，coverage 91.39% 达到既有85%门禁，EXIT0（build/qa/host-presentation/full-python.log）。此时 Web 仍在修复同workspace快速切表保存队列，Python通过不代表Web/新包资格。

Web 接线最终：178文件1603测试PASS，vue-tsc EXIT0，diff --check通过。真实回归先证明复合filter丢失/拖列未启用，以及快速连续修改后同步切表返回只读到旧状态；修复后CAS队列按workspace epoch+table隔离，同epoch切表保留旧表待写队列，真正epoch变化才废弃；返回原表前等待该队列完成。Preset与Host统一恢复、旧preset revision不覆盖新基线、cozy保真、实际keyword/冻结/密度控件、每次setColumns重施布局已覆盖。尚未独立审查完整Host分支或新包GUI。

独立审查修复：Host出站漏get/save实际2RED，补两精确类型后70HostPASS。Web复现旧Preset等待后污染新表、runtime事件改OR/空eq/nullsLast与大整数舍入；作者补captured table/generation复核、完整查询与表头投影分离、局部lossless codec（能力不足明确拒绝）及筛选编辑器处理，并统一实际布局排序。最后Web178文件1607PASS、sidebar77PASS、typecheck/diff通过。新增S33真实控件/resize/order/filter OR+empty/sort/冻结隐藏/切表/workspace重开/CAS；node语法与索引检查通过，尚未执行新包GUI，完整Host进程重启资格仍缺。

## 同步指定 main（2026-09-10，待后续独立验收）

从干净的 `4de80f10132c0f32609dc1013cb02906a969acb9` 正常合入指定 `f88e856eea3b830c8f910acc3dbc9eae842eb5d1`，未继续追取其他 main。两处文本冲突是 Go 生成 owner 集合与 Python owner 数量：保留 Host 的 gridState.get/save 和 main 的四个 Go Surface 方法，Python owner 数量因此为 72。owner/catalog 均由原生成脚本重生；catalog 方法覆盖测试使用独立列举的 Host 四方法与 Surface 四方法，Go Host 测试也固定四方法闭集，不从生成集合反推期望。

Python gridState 和 Surface 均未恢复生产注册；旧 grid-state producer 文件保持原样，main 的 Surface frozen producer 和具名 closed errors 完整保留。Host 入站/出站两侧 gridState 白名单未改变，MainWindow 同时保留 HostGridStateStore composition 与 main 的 Product Surface gateway。S33 与 main 新 S17 均保留；生成索引检查通过，`docs/e2e-performance.md` 仍如实记录当时的 gap 1（S33）、changed 2（S07/S17）。本次未运行新包 GUI，历史 Web 1607/Host 70 不能替代合并后的独立 Standards/Spec、新包、fresh CI 与完整交付。

## S33 两进程资格编排（待运行）

`run_product_acceptance` 对 S33 现在固定为一个顶层场景、两个真实 Host phase。seed 从真实 UI 保存后必须先通过原有 normal-close、owner lease、端口和进程作用域检查；失败时不会启动 resume。runner 随后只读校验真实 workspace manifest 与 Host 写入的 registry，保存 Node 返回的 workspace UUID、table ID、字段名、完整 state 和 revision；不会直接写 Host 存储或后端。删除旧 readiness 文件后，resume 以相同 `local-data` 和 workspace root 启动另一 Host，从 UUID 绑定的现有卡片进入原工作区并选择原表，不调用 `waitForShell` 创建新工作区。两 phase 各有独立 evidence、controls、CDP 与 lifecycle，持久路径固定在 `build/qa` 下。此编排与聚焦 harness 契约不构成新包 GUI 的通过证据。

本次合并验证（均使用固定隔离 Python 环境、`UV_NO_SYNC=1`、`PYTHONPATH` 指向当前 worktree）：

- `uv run --frozen --no-sync python -m pytest tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_product_contracts.py tests/contract/test_grid_state_python_oracle.py tests/contract/test_surface_python_oracle.py tests/backend/rpc/test_error_registry.py tests/backend/application/test_grid_state_service.py --no-cov -q`：53 PASS，3.67s；日志 `build/qa/host-presentation/merge-main-python.log`。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~GridPresentation|FullyQualifiedName~HostGridStateStore|FullyQualifiedName~WebMessageRouter|FullyQualifiedName~ProductRpcCapabilityManifest|FullyQualifiedName~HostProductRpcInvoker|FullyQualifiedName~JsonRpcProductSurfaceGateway|FullyQualifiedName~SurfaceError'`：104 PASS，0 SKIP，218ms；日志 `build/qa/host-presentation/merge-main-host.log`。
- 固定 Go 工具链执行 `go -C sidecar test ./internal/contracts/productcapabilities ./internal/productrpc`：两个包 PASS；日志 `build/qa/host-presentation/merge-main-go.log`。修改的 Go 测试已 gofmt。
- `uv run --frozen --no-sync python contracts/v2/product_rpc_capability_policy.py --check`、`contracts/v2/generate_product_rpc_catalog.py --check`、`contracts/v2/product_runtime_inventory.py --check` 与 `scripts/generate_product_e2e_capability_index.py --check`：EXIT0。固定 Node 执行 `node --check tests/e2e/webview_product_scenarios.mjs`：EXIT0。
- `uv run --frozen --no-sync python -m pyright backend`：0 errors（工具提示本 worktree 无本地 .venv，执行环境为指定隔离环境）；日志 `build/qa/host-presentation/merge-main-pyright-backend.log`。相关四文件 Ruff check 通过；独立 Host 方法预期触发一次 Ruff format 检查失败后，已按 formatter 修正。
- 额外将 Pyright 扩至 `contracts/v2/generate_product_rpc_catalog.py` 时出现四项诊断：412 行可空 model name、489/575 行字典不变性、816 行 event_type Literal。指定 Python 解释器后诊断相同；对应实现与两侧提交均未因本次同步修改。该文件不在 adapter 的 `pyright backend` 入口范围，本次记录额外检查失败，不用 ignore 或扩大业务修改掩盖它。

## S33 恢复验收断言修复（2026-09-10，尚未运行新包）

恢复导航提取为 `activateHostPresentationWorkspace`，使用原 UUID 卡片和目标会话等待。一次打开尝试返回 false 后重新读取会话：已有其他 UUID 立即拒绝，目标自动打开已完成或仍在进行则等待同一个目标条件；不重复点击，不 sleep，不创建替代工作区。可控 page 行为测试复现“卡片先 ready、读会话为 null、自动打开完成、按钮回调返回 false”及按钮禁用期间仍在打开两种交错，旧逻辑 2 FAIL / 3 PASS，修复后 5 PASS（`build/qa/host-presentation/restart-activation-red.log`、`restart-activation-green.log`）。

seed 增加 First/Second 两列，真实拖动 Second 到 First 前，并保留两列可见且非冻结；Title 冻结与 Status 隐藏另行验证。resume 在呈现控件和网格结束 busy 后，读取实际 header DOM 的宽度、`tabulator-frozen`、可见列顺序、隐藏状态、`aria-sort` 及网格密度 class，再读取筛选编辑器每条条件的字段、操作符、连接词和值。保留完整 Host state/revision 比较，恢复断言前不重新应用 Host state。Tabulator 将数值宽度写为 CSS px，DOM 几何允许最多 1 CSS 像素的舍入；测试接受 0.5px 差异、拒绝 2px 差异。

锁定 JSDOM 提供 DOM 语义，测试替身只补充其不实现的布局几何。在 Host state 完全正确时故意破坏 DOM，旧断言错误接受 width/frozen/order/sort/density/filter operator/field，得到 7 FAIL / 8 PASS（`restart-dom-red.log`）；修改后首次 15 PASS（`restart-dom-green.log`）。随后补充像素容差、冻结的排序列和空等式值回归，19 项 Node 行为测试经相关 Python 入口通过。此替身测试验证验收断言，不构成真实 WebView2 渲染证据。

使用指定隔离 uv 环境、`UV_NO_SYNC=1`、当前 worktree 的 `PYTHONPATH` 与固定 Node 执行：`uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py tests/e2e/test_retention_natural_aging.py --no-cov -q`，130 PASS / 9.35s（`restart-focused-python.log`，包含新增 Node 行为契约）；三个相关 Python 文件的 Ruff format/check、两个修改 Node 文件的 `node --check`、`uv run --frozen --no-sync python scripts/generate_product_e2e_capability_index.py --check` 通过。本阶段未 commit/push、未运行完整包或 GUI；独立复审、固定 source 的新包 S33 和完整发布门禁仍 pending。

## 完整数值保真与独立复审

此前仅保护大整数的 codec 不能保留高精度小数、舍入为整数的小数、上溢和下溢。实际 bridge → reactive → clone → persistence → outbound 链先取得 13 FAIL / 16 PASS；现按源 token 和 Number 序列化值的精确十进制语义比较，仅对有损数字保留 raw JSON，普通 width/order 仍为 Number。缺少源 token 或必要 rawJSON 能力时明确拒绝并阻断覆盖保存。筛选标量、范围和列表的数值输入使用保留文本的控件，通过同一 codec 解析。

固定 Node 24.19.0 执行 `desktop/web-grid/node_modules/vitest/vitest.mjs run --config desktop/web-grid/vite.config.ts src/services/gridPresentationService.test.ts src/components/grid/FilterTreeEditor.test.ts src/contracts/gridStateJson.test.ts src/bridge/hostBridge.test.ts`：71 PASS；`desktop/web-grid/node_modules/vue-tsc/bin/vue-tsc.js --noEmit --project desktop/web-grid/tsconfig.json` EXIT0。独立 Spec 复审另运行三文件36测试及12种数字边界，均通过，原数字P2已修复。

S33 最后一轮独立 Standards 与 Spec 均0项剩余确定发现；Spec独立用固定Node执行 `--test tests/e2e/host_presentation_restart.test.mjs`：19 PASS / 0 FAIL / 0 SKIP。此前两轮复审失败不视为通过，最终修复已用实际异步交错和DOM破坏回归证明。完整release build和真实双Host S33仍待当前固定提交执行。

## 首次 S33 新包结果与列宽定位修正

固定源码 `1ac9a1464a60affc0dc7a268812b1e2a62ac2350` 的 `uv run --frozen --no-sync python scripts/build_next.py --release` 完整 EXIT0，日志 `build/qa/host-presentation/build-release-final.log`。同包首个 S33 报告 `build/qa/host-presentation/product-e2e-final/20260910T091349Z/product-e2e-report.json` 为 EXIT1：seed 前8条断言通过，后在列宽手柄 `boundingBox` 等待30秒超时，resume未启动。四组件freshness通过，bridge failures/pending/acknowledged与pageErrors均0；唯一console条目是 autofocus 的 info，不是error。失败阶段Host仍正常退出0，进程/端口/owner lease及final cleanup均通过。

锁定 Tabulator 的 ResizeColumns 实现使用 `element.after(handle)`，手柄为列头的紧邻兄弟节点。场景原来查找列头的后代，无法命中真实节点；现改为目标字段列头后紧邻的 `.tabulator-col-resize-handle`，仍执行真实鼠标拖动并验证Host回执中的宽度改变。未修改产品实现、等待时限或断言。既有完整包的运行时源码未变，后续只针对该确定定位根因复用同包验证，不把首次失败改记通过。

## 第二次 S33 新包结果与真实拖动边界修正

定位修正提交 `91e37cc1f9ceb04bc93caede5386f31a40666bf4` 复用上述完整包执行 S33，报告 `build/qa/host-presentation/product-e2e-resize-selector/20260910T092125Z/product-e2e-report.json` 仍为失败：seed 前9条断言通过，真实列宽由160变为224；随后列移动 `dragTo` 超时，resume未启动。trace 的 `call@230` 明确显示目标列内 `x: 2, y: 10` 被 `.tabulator-col-resize-handle` 拦截。pageErrors与bridge failures/pending/acknowledged均0，唯一console仍为autofocus info。首次列宽定位失败和本次列移动失败均保留，不能作为双Host恢复通过证据。

首个trace还确定了截图横向错位的来源：header与cell字段顺序和160px宽度相同；`call@200` 点击Title时，正在关闭的筛选popover多次拦截指针，Playwright重试中的自动滚动先把header contents单独滚至115.42857 CSS px，而body仍为0，之后才真正点击排序。锁定Tabulator只通过wheel及横向滚动总线同步表头，不监听表头原生scroll。场景现等待筛选面板hidden后继续；列移动需要横向空间时，使用真实body鼠标滚轮并等待目标可见、header/body滚动一致，不依靠列头locator的自动滚动。

锁定 `MoveColumns` 以250ms按住期区分点击和拖动，只有进入移动状态后才注册目标mousemove处理。场景改为真实鼠标按下后等待 `.tabulator-moving` 可见，再将First拖至Second右半部的内部位置并释放；水平滚动后的这个向右交换同时避开边缘手柄和该版本对左半部落点额外叠加scrollLeft的计算。最终仍严格要求Host保存的 `secondOrder < firstOrder`，且可见DOM同时包含两列、Second位于First之前；未使用force、固定sleep、额外重试或放宽断言。鼠标释放位于finally，失败也会结束按住状态。

本次固定Node24.19.0执行 `node --check tests/e2e/webview_product_scenarios.mjs` EXIT0、`node --test tests/e2e/host_presentation_restart.test.mjs` 19 PASS / 0 FAIL / 0 SKIP，`git diff --check`通过。现有19项行为测试覆盖恢复验收断言，不能替代新鼠标交互的真实WebView2验证；本次未重跑GUI、build、commit或push，修改后的S33及独立复审仍pending。

## 第三次 S33 新包结果与移动释放修正

复用同一完整包的第三次运行仍为失败，证据位于 `build/qa/host-presentation/product-e2e-real-column-move/20260910T093307Z/`。seed 已通过9项真实 UI/Host 断言，之后 `captured bridge response timed out`，resume 未启动。顶层 `product-e2e-report.json` 的 `scenarios[0].phases.seed.lifecycle` 完整通过：normal exit、Host exit 0、ports released、owner lease cleanup 和 final cleanup 均为通过；这次失败不是资源清理失败。bridge diagnostics 的 failures、acknowledged failures 和 pending 均为零，唯一 console 项仍为 autofocus info，不能把它当作错误。

完整 trace 表明最后已完成的 `gridState.save` 属于列宽 resize，而不是列移动的回执。MoveColumns 出现后，header/body 的同步横向位置从 179.42857 变回0，Second 的目标点 `x=1150.57` 超出 1011px viewport；原来的 mouseup 落在视口外，30秒后 First 仍带 `tabulator-moving`，placeholder 仍存在，因而没有新的保存请求。MoveColumns 只监听 `document.body.mouseup`，这构成确定根因，不归因于 bridge capture。

场景现在以一个小的可见目标 helper 在按下前和 `.tabulator-moving` 出现后各检查一次：需要时仅对真实 body 执行水平 wheel，重新读取目标边界，要求 header/body scroll 同步、目标完整位于 viewport 内，并用 `elementFromPoint` 验证落点命中真实 Second header。鼠标在该有效落点释放；finally 在失败路径也在有效 body 内释放，但保留错误。释放后先等待 moving marker 和 placeholder 消失，再使用原30秒等待保存回执。未使用 force、直接赋 scroll、扩大窗口、固定 sleep、盲重试或放宽 Host/DOM 顺序断言。修改后的真实 GUI、独立复审和完整资格仍 pending。

## 真实 Tabulator 排序输入回归

第四轮S33（固定d7eb7044，build/qa/host-presentation/product-e2e-drop-completion/20260910T094720Z/product-e2e-report.json）seed18 PASS/1 FAIL，实际列移动及First5/Second4顺序、DOM检查已通过，resume未启动。失败来自非范围行号冻结列与range组合告警、以及Sort field undefined告警；bridge failures/pending/acknowledged为0，Host exit0与端口/lease/finalCleanup全部通过。原失败保留，不以局部通过冒充双Host资格。

排序问题是把Tabulator getSorters输出的field误当setSort输入；锁定6.5.2实际读取column。新增真实TabulatorFull实例经现有createTabulatorDataSourceViewAdapter、apply/capture公开seam的回归，旧代码1 FAIL/6 PASS：相同undefined告警且读取sorts为空（sort-contract-red.log）。输入类型和映射改用column，输出继续保留field。相关dataSourceViewState、presetViewController、gridPresentationController三组20 PASS（sort-contract-green.log），vue-tsc通过，无mock替代实际Sort实现。冻结与范围选择兼容性另有真实库几何调查，尚未解决，不能过滤告警后放行；完整新包和双HostS33仍pending。
