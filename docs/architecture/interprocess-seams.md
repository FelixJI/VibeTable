# 跨进程 seam 索引

> 本页只索引现行边界。PocketBase/Go sidecar 是业务数据权威；Web 不取得数据库、对象仓库或本机
> 绝对路径，WPF 持有进程生命周期和 path grant。

| 边界 | 现行入口 | Authority / session | 错误与取消语义 | 现有验证 |
|---|---|---|---|---|
| Web → WPF WebView2 bridge | `desktop/web-grid/src/bridge/hostBridge.ts`、`services/workspaceV2HostAdapter.ts` → `MainWindow.Product.cs` / `Services/WebMessageRouter.cs` | Host 白名单；`workspaceId`、`sessionEpoch`、request/operation id 绑定当前 session。设置族（`appPreferences.get/update`＋`appearance.changed`）由 `contracts/v2/app-settings-contract.json` 单一声明，两端生成投影：TS `contracts/appPreferencesContracts.ts`（含真实 bridge 解码）、C# `VibeTable.Contracts/Generated/AppSettingsContracts.g.cs`（patch 解析/结果/appearance DTO/白名单常量）。 | Host bridge 以稳定 error/reply 结束 request；旧 epoch 响应不得写入新 session。设置回复在 bridge 解码，畸形成功拒为 `AppSettingsProtocolError`（结果未知，不得推断未提交）；未知错误码保留诊断失败。 | `hostBridge.test.ts`、`appPreferencesContracts.test.ts`、`workspaceV2HostAdapter.test.ts`、`WebMessageRouterTests.cs`、`AppSettingsContractConformanceTests.cs`、共享 `contracts/v2/fixtures/app-settings-conformance.json`。 |
| WPF dispatcher → Python BFF | `JsonRpc*Gateway.cs` → `backend/rpc/{server,dispatcher,messages,framing}.py` | WPF 是进程与本机能力 owner；Python 不拥有 SQLite 写路径。 | JSON-RPC error envelope；调用方传递取消，具体 method 保持闭集。 | `JsonRpcProductDataGatewayTests.cs`、`tests/backend/rpc/*`。 |
| WPF → Go sidecar v2 RPC | `WorkspaceV2HttpGateway.cs` → `/api/vibetable/v2/rpc` → `protocolv2/dispatcher.go` → `workspacev2/runtime.go` | sidecar/PocketBase 是业务数据 authority；wire 绑定 `workspaceId`、`sessionEpoch`、fence/operation/sequence，path grant 仅由 Host 注入。 | `workspaceV2ErrorEnvelope` 使用稳定 JSON-RPC envelope；context cancel 传播到 Go handler。 | `WorkspaceV2HttpGatewayTests.cs`、`protocolv2/dispatcher_test.go`、`workspacev2/runtime_test.go`。 |
| Go sidecar capability → Web 可见性 | `Runtime.Capabilities()` → `/api/vibetable/v2/capabilities` → `MainWindow.Product.cs` bootstrap → Web adapter | Producer capability 与 Host allowlist 必须共同满足；Web 只消费 Host 下发的闭集。 | capability 缺失返回稳定 `workspace.capability_unavailable`；不得以 UI 猜测替代。 | `workspacev2/runtime_test.go`、Desktop capability/dispatcher tests，以及能力矩阵所列最终产品场景。 |
| Web Document Diff → WPF → sidecar | `document.diffRequested` / `document.diffCancelRequested` → `WorkspaceDocumentOsAdapter` / coordinator → Host-only `fileHistory.materializeDiffPair` 与 `assertEffectiveRevision` | sidecar 固定 target/effective revision；Host 持有两文件 path grant 和 epoch/sequence lease；Web 只持 entry handle/revision id。 | operationId + entryHandle 精确取消；materialize 前后 CAS stale 映射为稳定 `stale`；epoch 轮换取消在途请求，raw Web materialize 被拒绝。 | Workspace/OpenXml engine tests、Desktop adapter/cancel tests、Web service/store/view tests、产品场景 `14-document-diff`。 |
| WPF test-mode controls → 产品 E2E | runner 写入受控 controls dir → WPF test-mode picker/normal-close watcher | 仅 `--test-mode + --e2e-controls-dir` 可用；production 始终使用 native picker，不向 renderer 暴露 raw path route。 | 缺失/无效 fixture fail closed；normal close 后报告 Host exit、后代进程和端口释放。 | `ProductE2eControlTests`、`WorkspacePathGrantStoreTests`、runner 契约，以及[当前场景声明](../quality/product-e2e-capability-index.md)与对应 `required` 报告。 |

桥接消息的补充来源为 `contracts/v2/host-bridge-message-catalog.json`：Product method/topic
以 `reference` 引用原 authority，`host_bridge_catalog.py` 调用现有 Product manifest builder，
从原 catalog/policy/runtime inventory 解析，不重复声明 owner/scope/audience/effect。生成器拒绝
失效引用、public audience 降级及独立声明已有 authority 名。只显式投影当前边界，新增 public
方法不会自动开放；内部 rollback 仍拒为 `CAPABILITY_NOT_PUBLIC`，退役布局保留
`CAPABILITY_RETIRED`。C# `HostBridgeCatalog.g.cs` 与 TS `generated/hostBridgeCatalog.ts`
分别被真实 router/bridge 消费，五个消息族的数组也来自该生成物，freshness 由 `--check` 检查。

`routerWeb/routerHost` 仅描述静态 seed；Host 继续组合原 Product/relation typed registry 并执行
参数、audience、session 等 guard，不改变 dispatch owner。四侧投影有意不对称，不能把它们取并集。
Workspace 内层方法继续由 `RPC_REGISTRY`＋Workspace policy 经原生成器产生 manifest/TS 投影；
外层 `workspace.v2.request` 属于补充表，两者没有同名交集。设置族独立沿用 app-settings 权威。

上述设置纵切覆盖参数/结果/appearance 及关联错误；其它 payload 沿用已有契约，不宣称全仓统一。
设置的合法 `operation.failed` 要求 `message: string`，`code/operation/operationId` 允许缺省、null
或 string（保留真实 Host 的 null 输出），未知 code 保留诊断。畸形成功和畸形错误都拒为
`AppSettingsProtocolError`、结果未知；不得推断写入未提交或自动重放，也不改变既有 timeout/取消语义。

## 设置请求的协议与用例边界

真实路径为 `HostRequestDispatcher` → 同步 `AppPreferencesRequestAdapter` →
`AppPreferencesUseCases` → 既有 `AppPreferencesService`。adapter 使用生成的原始 JSON reader，
持有方法/requestId、wire 错误映射与唯一终态尝试；坏 payload 不进入用例，未知方法不 fallback。
回包器异常只记录异常类型，不补发第二终态。设置操作没有新增后台 Task 或无人观察的异步完成。

用例直接接收 `AppSettingsPreferencesPatch`、返回 `AppSettingsPreferences`，只依赖设置 service、
`IAppPreferencesEffects` 和初始状态；不依赖 Window/WebView2、JSON、reply sink、更新器或语录客户端。
`CurrentPreferences` 由用例单独持有；更新检查只调用 `RefreshCurrent`，不附加原生效果。
get 保持读取、更新 Current、应用原生效果、投影结果的顺序；update 保持托盘准备、持久化、更新
Current、主题投影、原生应用、结果的顺序。legacy adoption 的 compare-unset 留在原 service；
后置原生效果失败可能发生在写入成功之后，不能把 `WRITE_FAILED` 解释为未提交或重放命令。

直接用例测试、真实 adapter 测试与 `AppSettingsContractConformanceTests` 分别验证业务、一次终态
和原始 JSON 边界；打包 S12 验证真实窗口设置、主题和重开，不能仅以纯 mock 代替。

## 宿主 Product 调用与 Go owner

无 Worker 插件页面使用 `CoreWebView2.FrameCreated` / `CoreWebView2Frame.WebMessageReceived`
进入 `PluginSurfaceApi`。可信父页面只分配 iframe name；Host 捕获实际 frame、验证 source/token，
从安装快照与 workspace epoch 形成 principal。公开 Surface API 没有 Product 任意 method 转发。
Go 的私有 `/api/vibetable/v3/plugins/surface` 重用 v2 query.Source、pluginstore、MutationKernel；
Host-only 凭据不进入插件 JSON。MutationKernel 的可信 `Authorize` 回调在事务内、幂等回放之前
核对撤权和安装代际，业务行 guard 与审计保持原权威路径。

`HostPluginTaskRegistry` 只拥有原生 surface 的保存任务、确认消费及公开终态。
插件代码仅在隔离 WebView2 frame 内运行，不经过 Python 或 Node 插件执行器。
Python 保留固定的平台包检查/安装和 CSV/XLSX 等用途。确认由可信 Host UI 解析与批准，
插件仅得到 opaque planId；回执未知不补发，真实成功不被迟到取消覆盖。

#482 起 v2 action 可声明 `presentation: application/headless`（entry 在 action 上，与
`ui.customViews`/旧表单 schema 互斥）。Host catalog 投影为这类动作在 action 级派生 src/token
（仅投影字段，不落盘）；application 占据全业务视口（单 entry SPA hash 路由，仅同文档
fragment 变化被允许，导航栏/返回平台/管理入口始终在插件 frame 之外），headless 由保留路径
`__vibetable_host__/headless.html` 的 Host 内置受控文档加载包内 JS 模块（包内同路径不可伪造，
CSP/独立 origin/初始 source 语义与页面 surface 一致）；Go surfaceInstallation 同时接受
customViews 与 presentation-entry 两种绑定。旧 v1/API1–2 Worker 包只读保留原件；检查、启用和升级返回迁移诊断，不再执行。

`JsonRpcProductDataGateway(HostProductRpcInvoker)` 保持 typed method 与严格 Schema v2 解析；
invoker 按生成 policy/Workspace catalog 选择 Go、Host 或 Python owner。Go HTTP 握手独立持有
workspace epoch lease 至实际请求完成，单 caller 取消仅结束自身等待。

`ProductionWorkspaceRuntimeFactory.CaptureHostProductRpcBinding` 捕获固定 runtime、workspace
UUID/epoch 和 canonical Sidecar snapshot。Go binding identity 不含 Python client：普通打开、
查询、编辑、workspace 恢复及 Sidecar 重启均只恢复 Go/Host 准入，不启动或等待 Python。
首次 CSV/XLSX preview/template/import/export，以及固定的包检查/安装，经同一 runtime 的按需入口
启动现有 Python supervisor。并发共享一个启动 Task；共享启动拥有独立 Host epoch lease、
runtime ingress 取消与既有 BackendTimeout。单 caller 不取消其他等待者；失败后仅新请求重试。
SessionManager 先 request drain 后 runtime drain，因此启动失败/取消必须等待 supervisor 真正
teardown 才释放共享 lease；StopIngress/Stop/Dispose 在 lifecycle 锁外 cancel/join。

共享启动成功返回前，runtime 已为精确 client 安装文件 handler、DataIO report 与 Terminated
订阅。DataIO 报告由 runtime 的 HostDataIoTaskRegistry 消费，不依赖某个 gateway 构造或存活。
任务查询和既有任务取消不启动 Python；旧执行退出后保留 unknown/aborted 及已确认终态，
不自动重放。乱序 Backend.StateChanged 仅触发重新观察绑定，不能无条件退休当前 client。
Python 调用在启动及返回时验证精确 client；Sidecar、UUID 或 epoch 换代拒绝旧请求。

HostSessionFileBroker 属于 runtime 的当前 Go binding，可在 Python 尚未启动时签发 grant。
第一次 null→client 附着保留既有 grant；真实 client、Sidecar 或 epoch 退休使旧 grant 失效，
不能迁移到下一 client。短生命周期 gateway Dispose 不退休共享 broker。导出清理始终使用
发起时捕获的 client 和 broker，不会启动或调用替换代际。

`ProductTableComposition` 构造并拥有 Lazy table gateway、TableWorkspaceService、
ProductWorkspaceController、GridStateCoordinator、WorkspaceRequestDispatcher 和 sidecar lifecycle，
直接连接通知回调，无 nullable 回填。runtime、sessions/filter、authority、database-open registry
由外层借入；组合入口不创建第二 supervisor，也不拥有这些外层资源。runtime 注册是构造最后一步，
失败时仅回收本次取得的对象与订阅，原注册 owner 不受影响。

MainWindow 保留真实 native adapter、插件/authority transition 与独立 realtime owner，并按原顺序调用
组合入口的三个幂等释放阶段：realtime 后释放 sidecar lifecycle；authority/registry 后释放请求；
插件/文档后退订 workspace/table 通知、停止 coordinator 查询并释放 workspace/table gateway。
组合入口的 Dispose 补齐全部阶段；窗口后续初始化失败复用同一 OnClosed 回收顺序，允许尚未创建的
对象为空。session request drain→runtime drain、captured binding/epoch 与目录发布门保持原 owner。
`ProductTableCompositionTests` 在没有 Window/WebView2 的情况下构造真实子图并验证通知、失败回收
与阶段释放；窗口产品路径仍由同一候选的完整资格检查覆盖。

MainWindow 在 workspace activation 的 Verify 内完成 Go/Product/Table/Document gateway 安装。
首次 Python 附着不重建这些 gateway、不推进 authority transition、不 resetGrid；插件执行
单独等待精确 client gateway 安装。LazyProductTableGateway 按稳定 Go binding 复用网关；
update health reader 只读 schema.list，不启动 Python。内部 construction seam 继续复用真实
supervisors 与既有 process/health/HTTP adapter。

真实 S10 先通过完整、身份已验证的 Host Job 成员证明普通操作和 Go 恢复为零 Python，再
显式调用既有 schema_mismatch probe。仅 TestMode + controls dir 可消费一次
`python-start-fail-once.request`，在 supervisor spawn 前抛出启动失败；S10 核对首请求失败、
Go/UUID/epoch 保持可用且仍零 Python，第二次显式请求才启动成功。该证据不代表已创建子进程
的清理覆盖；后者由 supervisor、epoch teardown 回归和 S36 承担。S18/preset 共用的恢复 helper
保持纯 Go；原精确 Python kill 与 Go paste token 契约保留。

文件预览通过既有 Host Data IO owner 登记 `data.previewImport` 任务；Web 与导入/导出共用任务 ID、
会话退役防护及取消入口，工具栏显示真实读取字节、解析行数或编码/写出阶段。未知总量为 0，
不显示虚构百分比。直接 `data.previewImport` RPC 保留为封闭兼容调用。
CSV/XLSX 读取、逐页编码和 ZIP 保存离开事件循环；取消检查覆盖逐块、逐行和 ZIP I/O。
线程被取消后必须完成 join，才可关闭其流或结算 grant。XLSX ZIP 与临时 worksheet 由同一执行收尾。
每次操作重新读取 Go schema，并为此次执行构造专属 profile；Worker 不再共享可变 schema 缓存。

Python 普通 RPC 仍经上限 64 的 FIFO 串行队列；Host 文件回复由读泵直接路由。
只有 `task.cancelExecution`、`task.settleExport` 与既有插件取消/交互/文件回复控制入口
使用独立上限 16 的串行控制队列，任一队列过载均关闭通道，不把业务 RPC 全面并行化。
Host 导出命令在 registry 的同一锁内获取终态 Task，替代 100ms 自轮询；等待者取消不取消公开记录。
导出命令在既有 10 秒清理预算内撤销 captured grant、等待 captured worker 结算；
已确认提交成功优先于迟到取消和清理错误，未确认提交保持 unknown，不宣称已撤销或自动重放。

现行 Product owner 以[生成能力清单](../../contracts/v2/product-rpc-capability-manifest.json)和[ownership inventory](../../contracts/v2/product-runtime-ownership-inventory.json)为准。`query.page`、`query.readRows`、`query.cursorOpen`、`query.cursorFetch`、`query.selectionOpen` 与 `query.view` 按该 policy 直达 Go，Python 不再注册这些方法。selection 产生的 cursor 继续由同一 Go authority 续读。
`query.view` 以 `queryViewRegistration` 直达既有 `query.Port.ExecuteViewQuery`，保持原 Python
参数边界、分组投影与公开错误；默认 Host composition 验证 Go epoch、远端错误及关闭取消均不 fallback，S02 通过现有分组／汇总控件覆盖产品链路。
`lookup.valuePage` 经同一policy直达既有Go relation服务，以目录revision和稳定fieldId绑定来源分页；
Python专属分页转译已删除；lookup.query也已迁移，供冻结独立输入和其他路径使用的revision helper保留。完整原件与资格状态见[分页资格](../quality/lookup-value-page.md)。
`file.token` 按生成 policy 归 Go sidecar；本地 import/export task 与文件 grant 由 Host 持有，Python 仅保留执行上下文，见下文。
`HostProductRpcInvokerTests` 在 typed gateway seam 使用实际 HTTP/JSON-RPC adapter 和 session drain
验证此契约；进程和网络由测试 peer 提供。
`HostProductRpcCompositionTests` 通过真实 factory/runtime、Python supervisor 和 session close，验证
错误 workspace/session 期望拒绝、Python 停止后 Go paste 继续工作而 Python route 拒绝、
Sidecar 换代拒绝旧发送/迟到响应，以及默认
生成 policy 按方法选择当前唯一 owner，并验证 `query.page` 与 `query.readRows` 通过 Product HTTP gateway 保持返回值。游标组合测试验证 Go open/fetch 的 cursor 传递，selection 组合测试验证默认 Go owner 的代际租约、取消及公开失败零 fallback。
Go Product 与 REST 的 `events.reconcile` 共用 revision authority，`file.list` 共用 attachment manager，
`schema.getTable` 共用 `schemaexecution.Describe` 投影与 field 错误分类，`schema.list` 共用
Catalog 投影。Python 保留共享参数模型，但不再注册或转发已迁移的方法。独立 Workspace catalog 的六个
既有方法名单由参数 contract 与 golden generator 共享，不作为未知方法的默认 Python fallback。
同一真实 composition fixture 还覆盖 Lazy 同 Client 新 snapshot 轮换而不提前结束旧在途请求，
以及 health reader 的期望 epoch lease、严格 schema.list、远端错误和 close 取消。

## Host 设备偏好（L6）

`settings.readDevice` / `settings.saveDevice` 经 Router 闭集 manifest 校验后直达
`DeviceSettingsRequestController`；owner 为 `wpfHost`，Python 不再注册这两个方法。
`global` 仅是 transport scope：文件仍位于当前 runtime root 的
`.vibetable/data/state/device-settings.json`，旧 snake JSON 与 camel wire 均保留。
Host 捕获当前 workspace/epoch lease，在写入、同目录原子替换及回包前核对当前绑定；
请求携带 workspace scope 时复用该 scope 的 epoch/sequence 准入，不重新绑定到当前 session。
关闭/切换通过现有 drain 等待请求退出，无 Python Ready 前提，也不改变业务只读 authority。
`DeviceSettingsRequestControllerTests` 以真实 session/临时 JSON 验证；尚无可见 UI 消费者，
不把该测试当作 packaged UI/E2E 通过证据。

## 插件任务/交互公开 owner（L7）

`plugin.task.get` / `plugin.task.cancel` / `plugin.interaction.resolve` 由 WPF
`HostPluginTaskRegistry` 唯一拥有。Host 生成 taskId/runId，绑定安装身份、surface 与工作区代际；
终态粘滞，迟到取消不能改写已记录的成功。确认登记核对 run/project/plugin/action/interactionId
和期限，在 Host 原子消费；安装退役和工作区切换会终止待确认请求。
Python 的 describeAction/startAction/cancelTask/resolveInteraction/resolveFile 仅保留固定
`plugin_worker_retired` 拒绝，不持有执行、交互或文件请求状态。原生文件能力由
`PluginSurfaceApi.Files` 与 `HostSessionFileBroker` 管理；任务终态撤销对应授权。
未知提交保持 unknown，不自动重放；Web 不能用迟到任务回包复活已结束任务。
安装计划仍由 `HostInstallPlanLeaseRegistry` 持有，共享插件状态仍由 Go catalog 持久化。

以下 #369 报告是旧实现的历史证据，不代表当前无 Worker 链已验收；当前资格以对应 PR
及完整产品场景报告为准。

#369 的本地真实候选 S11（`build/qa/task369-plugin-owner/20260924T045540Z/product-e2e-report.json`）通过：
原生授权文件读写、明确确认后的单条提交、字段越权拒绝，以及待确认时杀掉已归属的 Python 子进程后公开 task.get 的
aborted/unknown、旧 resolve 的 expired、既有成功不变和 Go query 可用。S17 同候选通过（`build/qa/task369-interface/20260924T045641Z/product-e2e-report.json`）。
这两份报告先于终态提示文案修正；最终产品代码 aa389cd2 重建后 S11 再通过（31.915 秒，
`build/qa/task369-final/20260924T050407Z/product-e2e-report.json`），包含终态不再等待的界面断言。
[终态截图](../assets/screenshots/vibetable-plugin-task-aborted.png)来自该真实 WPF/WebView2 运行。
最终 PR CI、独立审阅及合并后门禁另由 Issue/PR 记录，不据此宣称 L7/L9 全部完成。


## 插件共享 catalog 与本机包

`plugin.listCatalog` / `plugin.listAudit` / `plugin.listPendingCleanup` / `plugin.setEnabled`
由 Host Product binding 直达 Go，不要求 Python gateway 就绪。PocketBase 的
`vibetable_plugin_records` 保存 installation、revision、audit、setting 四类记录，projectKey
固定为当前 workspace UUID 的 `local:<32 位小写十六进制>`；路径不是身份。Python 固定包检查/安装仅通过 session 保护的固定 `/api/vibetable/v1/plugins/store` 闭集操作读写共享状态。
写入复用业务 write coordinator/fence 和同事务持久收据；`commit_install` 一次提交安装 snapshot、
当前包 revision、安装 audit 和 catalog outbox，不跨 HTTP 进行删除补偿。

启动公开读取前，Go 从当前 runtime data root 的 `state/plugins.db` 只读承接旧四类记录。
首次缺源按新 workspace 完成；无完成标记而目标已有记录时拒绝覆盖，任何读取或校验失败均不留完成标记。
迁移版本与 workspace UUID 绑定的完成标记、四类共享记录均随整库 snapshot 恢复，后续启动不再打开旧源。
共享记录参加 SettingsItem 冲突投影；完成标记属于内部迁移簿记。

Go outbox 发布 `plugin.catalog.changed`，Host 在当前 epoch、gateway generation 和 renderer 门禁内
复用 `PluginRequestDispatcher.ProjectSnapshot` 投影，Web 断线恢复后重新读取完整 catalog。
包的 source/localPath 字段不授予执行或资源能力：Host 只按既有 packageHash/Base32 规则定位当前
runtime data root 的 `state/plugin-packages/*.vtplugin`，缺本机保留包时不生成资源链接；重新定位后
使用新 runtime root。Python 包安装前复用既有包检查校验预期 packageHash。Host 保留确认、文件 grant
与 surface token 能力；隐藏升级/回滚/卸载未开放为公开 RPC。

## 插件安装计划与固定平台进程（L9）

`HostInstallPlanLeaseRegistry` 保存完整安装计划与下载包 lease。Host 签发 planId、核对 inspect 回显，
并将计划绑定到 workspace/session、Go authority epoch 和具体 Python gateway 代际。公开 renderer
commit/upgrade DTO 保持不变；Host 原子消费 lease 后才构造带完整计划的私有执行参数。
Python 不再维护 `_plans` 或接受远端 cancelInstall；它在执行时校验请求身份、重新检查源包与 manifest，
沿既有 packageHash/retain 契约保留本机包，再调用 Go 原子安装事务。

取消安装只在 Host 消费 lease、释放下载包，不启动 Python。未知、重复或旧代计划无法提交；
Python transport 终止立即退休该 gateway 的计划绑定，已消费操作在开始和投影结果时再次检查绑定。
这不会退休仍可用的 Go epoch，也不会因 Python 重启恢复计划或自动重放未知提交。

Python supervisor 的固定平台进程清理和 DocumentDiff.Worker 继续保留。
插件 Node adapter/runner 与 Python 插件业务执行编排已删除；不以改名后的执行器回退。
当前 S11 验证新执行链、确认、文件能力、Host 重启与旧包保全；隔离插件卡死、异常和未知
提交还由 S49/S50/S51 覆盖。历史 L8/L9 运行报告见
[产品 E2E 历史证据](../e2e-performance.md#当前产品-e2e-证据)。

## 旧插件原件保全

插件中心通过 Host-only `pluginPreservation.snapshot` 分页读取当前工作区的四类原始记录。
分页协议 v2 首次返回 snapshotRevision，后续分片及终页必须携带并匹配同一代际。
Go 在同一短事务内读取代际、重验安装身份及读取原 JSON；数据库触发器让设置、包修订、
审计、删除和旧库导入的每次提交都推进代际，期间发生写入就中止导出，避免混合或漏项。
Host 只从可信 workspace cache 按已有 packageHash 读取原包，不信任记录中的 localPath；
按安装时的规范化 ZIP 成员摘要校验原包，并持有同一禁止写入/替换的句柄完成复制。
Host 从同一读句柄向生产 backend 的固定 `--verify-retained-plugin-package` 单次模式传递
有界 ZIP 字节（最多 64MiB，不传路径），复用安装权威的 ZIP 读取与成员 identity；
不套用当前 manifest/Worker 准入规则，中央目录方法及 CP437/UTF-8 行为与 Python 一致。
该固定平台文件能力不建 BFF/RPC server、不执行插件代码；Job 绑定和 ready 握手完成后才发送 ZIP，
沿用单次文档解析的 1GiB 内存、30 秒执行和 5 秒退出宽限预算。stdout/stderr 有界，失败、
取消或超时均结束并确认进程退出；成功退出后才从原读句柄复制，不遗留 Python/Node 插件执行器。
内容损坏会中止保全。ZIP 包含 records.json、原 vtplugin 与
preservation.json；缺包明确报告，不生成替代空包。取消、工作区退役或失败不会提交半成品，
该通道不修改安装、私有设置、审计或业务数据。

## Shell readiness 与按需 Worker

测试模式的 `hostReady` 表示现有 Host StartupState.Ready 与 renderer router 准入；
`webViewReady`、`rendererReady` 继续独立校验。Shell 可以尚未打开 workspace，
因此不以 Python 进程存在作为就绪条件；更新激活仍由 `workspaceProbe` 额外证明工作区健康。
## 维护规则

- 新跨进程 operation 同时更新本页、capability 矩阵、producer/Host/Web 的闭集测试和至少一条产品证据。
- session/epoch 轮换、取消与错误 envelope 属于 wire 行为；行为保持型重构不得顺手改变。
- Document Diff 继续复用 sidecar authority、Host path grant 和 epoch seam；不得建立 Web 到 repository 的旁路，
  也不得把本机绝对路径放进 bridge payload。


`query.selectionOpen` 的 Product owner 为 Go；它直接调用既有 SelectionPort 的原子
schema/cursor 投影并保留请求 tableId 与三项 revision 配对。Python 专属 handler/client
已删除，宿主仅按生成 policy 路由，无 Python fallback。游标签发与续读的数据权威仍为
同一 PocketBase QueryPort；`cursorFetch` 的 Product 路由 owner 不构成新的游标 authority。

`lookup.query` 经 `lookupQueryRegistration` 直达既有 relation catalog/query 端口；专属 Python handler 和 grouped-view client 已退役，共享 relation export 的 lookup client 保留。八字段 Product 与 workspace/epoch 准入不变，Go 失败不回落 Python。原 Python 冻结回放、typed 表达边界及 S29 的当前验收状态见 [lookup.query 资格](../quality/lookup-query.md)。

## Go 权威实时恢复（L4）

`ProductRealtimeSession` 通过认证 Go v2 SSE 接收活动公式任务与有限终态通知，在 renderer 业务订阅就绪后交付；WPF 管理连接代际、epoch 和投递生命周期，不建立任务权威缓存。Python SSE supervisor、latest revision cache 与 data.changed 二次包装删除。Data IO 的 `task.create/status/cancel` 由 Host 工作区对象持有，Python 仅执行并上报；旧执行通道退出后 Host 保留 `aborted` 与业务结果待核实的公开快照，不自动重放。导出 grant 仍由 HostSessionFileBroker 和发起调用的作用域绑定、撤销与结算。导入提交收到明确冲突或验证拒绝仍沿用 `failedRows`；回执 `pending` 或提交结论不明时上报失败且不返回零写入结果。恢复失败不得推进 bookmark，旧 epoch 不得交付。细节和完整验收边界见 [ADR 0012](../adr/0012-go-owned-realtime-recovery.md)。
