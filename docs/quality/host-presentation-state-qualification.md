# Host 本机呈现状态完整收敛：设计与资格记录

基线 main `9fa626a13840830037bcb82eaddc0adf8617075b`。本分支处理 PR140 L6 的本机网格呈现状态；与 Go 共享工作日历、Version、Dashboard 等业务 authority 分开。当前仅完成代码盘点，尚未生产切换或测试资格，不创建文档微型PR。

## 已核实际调用

- Web 的 gridState.saveRequested 经 GridRequestController / GridStateCoordinator → LazyProductTableGateway / PocketBaseTableGateway → JsonRpcWorkspaceSupportGateway → 常驻 Python gridState.get/save。真实消费者存在，并非仅注册表残留。
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
