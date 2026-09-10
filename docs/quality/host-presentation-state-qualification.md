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
