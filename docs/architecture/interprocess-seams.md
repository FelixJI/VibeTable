# 跨进程 seam 索引

> 本页只索引现行边界。PocketBase/Go sidecar 是业务数据权威；Web 不取得数据库、对象仓库或本机
> 绝对路径，WPF 持有进程生命周期和 path grant。

| 边界 | 现行入口 | Authority / session | 错误与取消语义 | 现有验证 |
|---|---|---|---|---|
| Web → WPF WebView2 bridge | `desktop/web-grid/src/bridge/hostBridge.ts`、`services/workspaceV2HostAdapter.ts` → `MainWindow.Product.cs` / `Services/WebMessageRouter.cs` | Host 白名单；`workspaceId`、`sessionEpoch`、request/operation id 绑定当前 session。 | Host bridge 以稳定 error/reply 结束 request；旧 epoch 响应不得写入新 session。 | `hostBridge.test.ts`、`workspaceV2HostAdapter.test.ts`、`WebMessageRouterTests.cs`。 |
| WPF dispatcher → Python BFF | `JsonRpc*Gateway.cs` → `backend/rpc/{server,dispatcher,messages,framing}.py` | WPF 是进程与本机能力 owner；Python 不拥有 SQLite 写路径。 | JSON-RPC error envelope；调用方传递取消，具体 method 保持闭集。 | `JsonRpcProductDataGatewayTests.cs`、`tests/backend/rpc/*`。 |
| WPF → Go sidecar v2 RPC | `WorkspaceV2HttpGateway.cs` → `/api/vibetable/v2/rpc` → `protocolv2/dispatcher.go` → `workspacev2/runtime.go` | sidecar/PocketBase 是业务数据 authority；wire 绑定 `workspaceId`、`sessionEpoch`、fence/operation/sequence，path grant 仅由 Host 注入。 | `workspaceV2ErrorEnvelope` 使用稳定 JSON-RPC envelope；context cancel 传播到 Go handler。 | `WorkspaceV2HttpGatewayTests.cs`、`protocolv2/dispatcher_test.go`、`workspacev2/runtime_test.go`。 |
| Go sidecar capability → Web 可见性 | `Runtime.Capabilities()` → `/api/vibetable/v2/capabilities` → `MainWindow.Product.cs` bootstrap → Web adapter | Producer capability 与 Host allowlist 必须共同满足；Web 只消费 Host 下发的闭集。 | capability 缺失返回稳定 `workspace.capability_unavailable`；不得以 UI 猜测替代。 | `workspacev2/runtime_test.go`、Desktop capability/dispatcher tests，以及能力矩阵所列最终产品场景。 |
| Web Document Diff → WPF → sidecar | `document.diffRequested` / `document.diffCancelRequested` → `WorkspaceDocumentOsAdapter` / coordinator → Host-only `fileHistory.materializeDiffPair` 与 `assertEffectiveRevision` | sidecar 固定 target/effective revision；Host 持有两文件 path grant 和 epoch/sequence lease；Web 只持 entry handle/revision id。 | operationId + entryHandle 精确取消；materialize 前后 CAS stale 映射为稳定 `stale`；epoch 轮换取消在途请求，raw Web materialize 被拒绝。 | Workspace/OpenXml engine tests、Desktop adapter/cancel tests、Web service/store/view tests、产品场景 `14-document-diff`。 |
| WPF test-mode controls → 产品 E2E | runner 写入受控 controls dir → WPF test-mode picker/normal-close watcher | 仅 `--test-mode + --e2e-controls-dir` 可用；production 始终使用 native picker，不向 renderer 暴露 raw path route。 | 缺失/无效 fixture fail closed；normal close 后报告 Host exit、后代进程和端口释放。 | `ProductE2eControlTests`、`WorkspacePathGrantStoreTests`、runner 契约，以及[当前场景声明](../quality/product-e2e-capability-index.md)与对应 `required` 报告。 |

## 宿主 Product 调用与 Go owner

`JsonRpcProductDataGateway(HostProductRpcInvoker)` 保持 typed method 与严格 Schema v2 解析；
invoker 按生成 policy/Workspace catalog 选择 Go、Host 或 Python owner。Go HTTP 握手独立持有
workspace epoch lease 至实际请求完成，单 caller 取消仅结束自身等待。

`ProductionWorkspaceRuntimeFactory.CaptureHostProductRpcBinding` 捕获固定 runtime、workspace
UUID/epoch 和 canonical Sidecar snapshot。Go binding identity 不含 Python client：普通打开、
查询、编辑、workspace 恢复及 Sidecar 重启均只恢复 Go/Host 准入，不启动或等待 Python。
首次 CSV/XLSX preview/template/import/export，以及必要插件执行，经同一 runtime 的按需入口
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
`HostPluginTaskRegistry` 唯一拥有：Host 生成 taskId/runId 并在调用执行器前登记，任务以 project/session/fence
绑定到启动它的 gateway 代际，终态粘滞，迟到取消不能改写已记录的成功；执行报告早于 start 回包时不得降级。Python 仅保留执行上下文、取消句柄与等待
Host 回复的 future；`plugin.getTask` 已从 Python 退役，`plugin.cancelTask`/`plugin.resolveInteraction`/
`plugin.resolveFile` 只是封闭 host-only 执行入口。Python client 失效（transport 终止、重绑或项目上下文切换）时，
非终态任务立即结算为 `aborted`，错误码 `plugin_task_aborted` 并明确 `commitOutcome: unknown`，不宣称零写入也不自动重放；
待确认交互与原生文件选择晚返回按代际拒绝，Host 文件 grant 撤销仍由 `HostSessionFileBroker` 的 Retire/DrainCompletion 观察。
安装计划继续复用 `HostInstallPlanLeaseRegistry`（旧 plan 仍要求重新 inspect）；共享插件状态由下述 Go catalog 持久化。
确认登记同时核对 run/project/plugin/action/interactionId 和期限，并在 Host 原子消费；文件选择回包核对完整请求及 run 取消令牌。
任务终态撤销该 run 的文件授权，transport dispose 等待已退休 broker 的 DrainCompletion，首个终止观察者异常不阻断其他 owner。
Web 终态不可被迟到交互或任务回包复活，终态面板不再显示仍在等待的提示。

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
固定为当前 workspace UUID 的 `local:<32 位小写十六进制>`；路径不是身份。Python 包检查与 Node
执行器仅通过 session 保护的固定 `/api/vibetable/v1/plugins/store` 闭集操作读写共享状态。
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
使用新 runtime root。Python 执行前复用既有包检查校验预期 packageHash。Host 保留确认、文件 grant
与 surface token 能力；隐藏升级/回滚/卸载未开放为公开 RPC。

## 插件安装计划与执行进程（L9）

`HostInstallPlanLeaseRegistry` 保存完整安装计划与下载包 lease。Host 签发 planId、核对 inspect 回显，
并将计划绑定到 workspace/session、Go authority epoch 和具体 Python gateway 代际。公开 renderer
commit/upgrade DTO 保持不变；Host 原子消费 lease 后才构造带完整计划的私有执行参数。
Python 不再维护 `_plans` 或接受远端 cancelInstall；它在执行时校验请求身份、重新检查源包与 manifest，
沿既有 packageHash/retain 契约保留本机包，再调用 Go 原子安装事务。

取消安装只在 Host 消费 lease、释放下载包，不启动 Python。未知、重复或旧代计划无法提交；
Python transport 终止立即退休该 gateway 的计划绑定，已消费操作在开始和投影结果时再次检查绑定。
这不会退休仍可用的 Go epoch，也不会因 Python 重启恢复计划或自动重放未知提交。

Python supervisor 复用已有 lifecycle 锁清理意外退出的精确 ProcessGeneration：关闭该代 Windows Job，
使正在计算且不读取 stdin 的 Node 也退出。若排队补启已替换旧代，旧清理直接结束，不影响新客户端。
Job assignment 失败沿已有 teardown 回收已启动进程；`ERROR_ACCESS_DENIED` 不能证明进程已被当前 Job 接管。
相邻 supervisor 回归在任何显式 Stop/Dispose 前检查真实繁忙 Node 退出，并验证排队新代仍可调用。
真实包的 S11 同时覆盖确认、文件能力、崩溃结算、Host 重启后的 catalog 和缺本机包缓存诊断；
主干固定样本（main@0bdbc1b）已将上述 L8/L9 范围连同 L10 六个普通场景（S01/02/03/05/06/08）
真实业务完成后的零 Worker Job 成员观测（Host/Go 各 1、Python/Node 各 0）一并验收，见
[当前产品 E2E 证据](../e2e-performance.md#当前产品-e2e-证据)。

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
