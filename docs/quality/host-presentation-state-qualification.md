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
