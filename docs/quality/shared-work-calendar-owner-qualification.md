# 共享工作日历 Go owner 设计与旧语义资格

本次完整共享工作日历实施的基线为 `9fa626a13840830037bcb82eaddc0adf8617075b`。完整意图是工作区 A 的假日/调休日经 PocketBase 保存，设置页、首页小日历与网格日期编辑器共享同一已确认投影；切换 B 不泄漏 A 的规则。生产代码与相关本地回归已实施；尚未双轴审查、构建、packaged E2E、PR 或 CI 放行。

## 切换前事实与 Module

- `SettingsCommandService.read_shared` 经真实 `PocketBaseInternalMetadataPort` 投影、scope/key 筛选读取 Go `shared_settings`；注册点仍在 Python `backend/__main__.py`。失败会返回空 settings/fresh=false。Web 没有调用者。
- `workCalendarStore.ts` 在全局 localStorage `vt:work-calendar:v1` 存放规则；SettingsView、HomeView 消费 store，`calendarDateEditor.ts` 直接读 storage。该状态没有 workspace 身份，既不复制也不进入 PB。
- Go metadata Module 已提供 `vibetable_shared_settings`、revision、事务、幂等 receipt、audit/outbox。复用其 Implementation 与 Runtime 写门禁；不增加 SQLite、Host 文件或 renderer storage authority。
- Host device settings、theme、locale、网格本机布局、shortcut/command 不是共享规则。本 PR 不修改这些能力的 owner，不将它们写入 PB 日历。

## Interface 与持久语义

新增公开 Go Product 方法 `settings.readWorkCalendar({})` 与 `settings.commitWorkCalendar({overrides, expectedRevision, idempotencyKey})`。两者要求活动 workspace，Host 使用现有 workspace/epoch scope 路由；不接受 renderer 提供物理路径、PB collection 或任意 key。

读结果 `{overrides, revision}`。未保存返回 `overrides: [], revision: ""`，读取不创建记录。固定 namespace=shared_settings，logicalId=`work-calendar`，payload 为 `{scope:"workspace", key:"work-calendar", value:{overrides:[...]}}`；revision 来自真实 metadata Item，不另算内容摘要。旧其他 shared_settings 项保持原样。

提交是完整替换一个聚合，operation 固定为 commit；没有逐日期 patch、第二 delete 路由或隐式 merge。`overrides: []` 清除覆盖规则但保留已有记录及其新 revision，避免删除重建回到空 revision。仅保留记录仍不足以防内容 ABA：现 metadata revision 源自 payload 内容摘要。固定日历 payload 另含私有 generation，由现 newID 在每次真正提交的同一事务内生成，A→B→A 仍有不同 metadata revision；重放不生成，不增加第二 hash 或公开 token 字段。首次创建必须 `expectedRevision: ""`；已有记录必须精确 revision。相同幂等键/相同请求返回原完整结果；异载荷拒绝；结果 receipt 在同一 PB 事务写入，使用现有 TTL、Runtime replay/epoch 处理，不额外补写或额外计算 hash。

提交结果 `{overrides, revision}` 外加既有 receipt trace 必需字段（按当前 metadata Product 约定确定），前端只采用确认结果。CAS 冲突显式重新加载，不能后台重试覆盖他人新值。保留现有旧 setting payload 的必需 metadata 字段；该固定 key 的 malformed payload 返回错误，不能当空日历覆盖。

### DTO 与预算

- overrides 必填 array，最多 3660 项（约十年每日覆盖的有界预算），可为空。完整请求上限使用现有 Product decoder 限制，不抬高全局 body 上限；实现需检查该 payload 在现有上限内，并对超限使用既有 request-size 错误。
- 每项只有 date/kind/name。date 严格 `YYYY-MM-DD`，实际 Gregorian 日期，支持 `0100-01-01` 至 `9999-12-31`，与当前 JS Date 对 0–99 年的拒绝一致；不存在日期拒绝。没有随当前时间变化的未来/过去窗口。
- kind 严格 holiday/workday；name 必填 string，trim 后最多 40 UTF-16 code units，匹配现 UI 40 字符限制与 JS slice 计数，但新写入超长拒绝而非截断。生产实现不得切开 surrogate pair；需测试中文与 emoji 边界。
- date 不得重复，拒绝重复而非旧 localStorage 的 last-wins；输出按日期升序。未知字段、null、错类型、非法枚举拒绝。旧 sanitize 的容错仅是历史证据，不作为新 authority 静默吞值的理由。
- idempotencyKey 使用既有 metadata 192 字符闭合模式；expectedRevision 必填字符串，格式/长度复用 metadata validator。

错误：根 DTO 沿用 Product invalid params；领域错误闭合为 `settings.calendar.invalid`（field 指向具体项）、`settings.calendar.revision_conflict`、`settings.calendar.corrupt`。存储不可用、幂等冲突、workspace 只读/epoch 取消复用现有 metadata/Runtime 错误映射；不给 raw PB 错误或路径。新前端不将失败伪装为 fresh 空列表。

## Python 退出与必要元数据读

同一切换 PR 删除 `settings.readShared` Python 注册，将旧公开 readShared 从 capability/catalog 退役；没有生产 UI 依赖它，不维持无消费者兼容层。固定历史回放的 DTO 与方法由固定 producer 提供。当前源码暂保留未注册、无生产调用者的 read_shared 方法与旧 DTO，原服务单测仍运行；公共生产注册已经退出；此残留仅供历史服务测试，尚未满足最终死代码退出项，后续统一收束。其他 command/shortcut 方法不在此 PR 删除。

现有内部 metadata GET shared_settings 保留，供 PB authority 的 snapshot/冲突/只读诊断等必要消费者使用。generic HTTP upsert/delete 对整个 shared_settings namespace 封闭，只允许 typed Product Module 写；不能只挡 work-calendar key 仍允许旁路修改它。内部 Go metadata Method 不删除，由 typed Module 复用。实现前搜索所有内部写消费者，发现真实另一个写能力则纳入显式 typed 设计或先交 root 裁决，不能暗中破坏或保留无限制 public 写。

## 三消费者与 workspace/epoch 状态

新的 workCalendar service/store 是 Go 已确认状态的投影，不是另一个 authority。状态至少区分 unavailable/loading/ready/saving/error/conflict；revision 与 overrides 总是一起替换。绑定 workspace UUID 与 generation；切换/关闭立即清空旧投影并取消在途请求，旧 generation 的成功/失败均不得应用。

SettingsView 编辑 draft，通过明确保存提交完整 overrides；忙时禁止重入写，失败保留 draft 与错误，冲突允许重新加载并由用户重作，不静默覆盖。HomeView 与 calendarDateEditor 只消费同一 confirmed projection；日期编辑器依赖注入当前规则读取函数，不自行解析 localStorage。加载失败必须能区分“共享规则暂不可用”与“没有自定义规则”，不能把带未知共享规则的日期标记成确认工作日；基本日期选取能力可以保留。

日历默认周末/工作日是纯显示计算，不写数据库。旧全局 localStorage 无 workspace 身份，不自动导入任何 workspace，也不在失败时回退使用；可保留磁盘旧 key 不主动删除，但生产停止读写。用户已允许开发期破坏性更新。

## 固定旧 producer 与局限

`contracts/v2/replay_shared_work_calendar_legacy.py` 每次从上述固定 Git commit 取得 backend 与 workCalendar.ts，在 `build/shared-calendar-oracle/<producer>` 隔离回放。Python 使用原注册、RpcDispatcher、DTO、SettingsCommandService、PocketBaseInternalMetadataPort，只有最底层 internal metadata transport 使用脚本响应，时钟固定。Node 24 原生 TypeScript stripping 执行旧纯规则；没有调用新实现或从预期结果反推输入。

`shared-work-calendar-legacy-oracle.json` 保留 11 个 Python 样本：默认空、scope/排序、key 筛选、另一 scope、未知 key、离线、损坏 authority、额外字段、空 collection、key 预算与重复 key；包括动态大整数。12 个旧日历样本覆盖非数组、非法闰日/合法闰日、重复 last-wins、排序/trim、非法 kind、名称截断、未知字段与四种日期展示。回放默认只检查，不重写；`--write` 仅允许原件不存在时创建。

旧样本证明旧行为，不能替代新 typed CAS、workspace 隔离、transaction/replay、真实 UI 或 PB 测试。旧 UI sanitize 容错与新写入严格校验的差异在本设计明确批准前不生产实施。未删除或迁移任何旧测试。

## 生产实施后的资格范围

- 真实 PB/Product HTTP：空读不写、提交/清除、日期/重复/预算、CAS、同键与异载荷、重启 replay、其他 setting 不变、generic 写封闭、只读/epoch 取消与持久 receipt。成功时只产生一次 mutation/outbox，失败事务不留修改。
- Web 真组件与日期编辑器：三个消费者一致、保存失败保留 draft、冲突重新加载、快速切换 workspace 丢弃旧回包、首次加载与不可用状态。
- 新包：A 设置公司假日，首页与实际日期编辑器显示；重开仍存在；B 不出现 A 规则；返回 A 修改/清除。S24 副本场景补真实 calendar 显示证据，避免仅检查 shared_settings 原始数据。
- 最小相关检查后运行项目质量入口，独立 Standards/Spec、fresh strict CI、squash 与 postmain CI/CD；此文不预先声明通过。

## 本次仅设计阶段的本地结果

- 固定 producer 首次捕获后独立只读回放：11 Python + 12 calendar 样本全部相等；node 使用现有锁定 24.19.0 缓存，Python 使用已有 uv 3.13 环境。
- `uv run --frozen --no-sync python -m pytest tests/backend/application/test_settings_command_service.py tests/contract/test_d2_settings_contracts.py -q --no-cov`：21 passed，0.64 秒。
- `uv run --frozen --no-sync ruff format --check contracts/v2/replay_shared_work_calendar_legacy.py` 与 `ruff check`：通过；首轮 I001 导入排序失败已修正，未忽略规则。
- 此阶段没有安装新环境、运行全矩阵或 GUI；无生产改动，因此没有新包资格主张。提交正常执行既有 hooks。

## 生产实施的本地结果与剩余资格

- Go typed owner、同事务 CAS/receipt、固定记录的私有 generation、Host 两层同一领域错误 allowlist、三处真实 UI 接线及 generic shared_settings 写封闭已经落地。内部 metadata GET 保留。已盘点真实写消费者，没有另一个生产 shared_settings 写能力需要兼容。
- 真实 PB 的 A→B→A 测试先以固定 generation 触发旧 revision 被接受的 RED，再恢复每次提交生成后 GREEN；另覆盖清空再填、同键重放不改变 generation/revision/mutation。证据位于本地 build/qa/shared-work-calendar/aba-red.log 与 calendar-green.log。
- Web store 等待 workspace.phase=opened，并在 applySession 的 UUID/epoch 批次完成后读取；新增回归验证 opening 不请求、新 workspace 只以新 epoch 请求一次。最终 vue-tsc --noEmit 通过；Vitest 相关 8 文件 140 passed。
- Host 相关 5 类测试 63 passed；曾有 6 个真实控制器投影测试因缺少 ProductDataRpcRegistry 注册而 UNKNOWN_TYPE，补齐注册后通过，原失败 TRX 留存。领域错误走真实 gateway→controller→reply 测试。
- Python 契约、capability policy、原 SettingsCommandService 与 E2E runner 回归组合 145 passed。固定 producer 再独立只读回放 11 Python + 12 日历样本通过，原件未重捕。
- Go 工作日历 metadata、真实 PB 集成、Product HTTP 与生成注册定向测试通过。完整 internal/app 包运行仍有三项 TempDir cleanup 失败：TestHistoryReadProductHTTPReturnsFreshAuditedPage、TestMutationProductHTTPRejectsStaleRevisionEpochAndRetiredGate、TestWorkspaceMutationReplayCancellationDuringReceiptRead；没有将其归因杀软或修改清理规则，不宣称完整 Go 矩阵通过。
- 新 S32 源码覆盖实际 A 保存、三个消费者、B 隔离、返回 A 重开与清空。尚未构建或运行 packaged GUI，S32 不能标为 passed。S24 场景来自另一未合分支，其日历副本消费追加须在主干合入后正常同步实施。
- 尚未运行完整项目质量/覆盖率门禁、新包、独立双轴审查与远端 fresh CI。已有局部测试不替代这些资格。