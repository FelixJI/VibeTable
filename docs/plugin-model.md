# 插件逻辑模型 v1

模型声明位于原生 API 3.x manifest 的 `permissions.model`，合同为 `vibetable.plugin-model.v1`。声明包含稳定 `modelId`、递增整数 `version` 和最多 40 个实体；每个实体最多 64 个字段。实体与字段使用稳定逻辑 `key`、用户可读 `label`。同一模型版本不能改变定义。

支持 `text`、`number`、`bool`、`date`、`dateTime`、`email`、`url`、`relation`。数字显式选择 `integer` 或 `float64`，沿用原生数字存储语义，不承诺任意精度整数或精确十进制。关系两端均须声明目标 `entity`、反向 `field` 与自身 `cardinality`；创建时使用原生双向关系、`restrict` 删除策略。每个实体须有至少一个标量字段，优先使用首个文本字段作为关系显示字段，否则使用首个标量字段。未知合同、物理字段、SQL 和脚本声明被拒绝。

`client.model.configure({mode:"create"})` 请求原生精确预览；`mode:"map"` 先由原生窗口选择现有表与字段。插件不能获取未授权工作区目录，也不能提交映射或批准参数。预览显示具体表、字段、操作、数字精度和关系。名称冲突、类型、精度损失或关系不兼容以字段路径诊断，拒绝写入；映射不会转换原值。

已有绑定可用 `configure({mode:"repair"})` 或 `mode:"map"` 重新选择。原生窗口优先显示已知 canonical ID，并为唯一同名或唯一类型兼容的字段提供建议，无法判定时留空。可改选并取消。人工选择和确认期间保留会话取消信号，每次目录和预览读取限时 15 秒，提交限时 120 秒。若显式取消后 20 秒仍无配置结果，SDK 返回 `plugin_commit_unknown`，调用方按 `read/status` 核实。精确预览有效期为 30 分钟，过期须重新预览。

已完成绑定的 `read/status` 按现行表、字段、访问性、类型、精度及双向关系核验；异常返回 `status:"stale"` 及带逻辑路径的 `diagnostics`，旧绑定仅供定位。原生修复预览逐项显示旧、新 canonical ID，原表或目标表存在记录、写入历史、关系依赖、已登记规则依赖或无法证明历史时拒绝。确认后在同一事务复核前序请求、结构、数据和登记规则，并追加新绑定。原记录、历史及规则登记保持各自的 canonical 身份。跨安装修订仍须按当前声明重新确认。取消可从当前权威绑定重新开始。

首次初始化通过统一 schema domain 创建表、添加字段与原生关系。每步业务事务同时保存不可变的审计进度和写协调器 receipt。`completed/total` 表示已提交步骤数，反向关系的一次事务算一步。`model.read()` 和 `model.status({requestId})` 从工作区持久状态读回 `pending`、`partial`、`failed` 或 `completed`，未创建的计划表不出现在公开进度绑定中。失败不自动重放，重新配置未失效计划会复用原计划身份与已完成范围。

从已完成的旧模型追加升级时，宿主在一次写协调和同一个 PocketBase 事务中复核原绑定、创建新增表和字段、登记完整的新绑定。提交前失败或取消会回滚该次升级的目录、字段、关系和绑定变更。原预览仍有效时可按同一请求显式重试。若旧宿主已接受升级并提交部分步骤，新宿主保留这些既有对象，将同一请求剩余的步骤和最终绑定放入一次事务。

只有当前启用安装、当前安装 revision、完整完成且与当前声明一致的绑定能派生数据权限。字段逐个列举并包含 `id`；用户扩展字段和升级新增字段不会自动授权。安装、模型或工作区变化使旧预览失效；升级后必须再次原生确认。已确认计划遇到外部 schema 更改会阻断，并保留准确已完成范围。

部分初始化后禁用再启用，新的确认计划只继承已提交表与字段的 canonical ID；尚未创建的表重新进入精确计划，确认之前不会补建。已提交对象丢失会报缺失诊断，不能用空表替代。并发请求的失败状态在写事务内读取最新进度，不能覆盖另一个请求已完成的结果。映射接受与完成均检查原生字段迁移写锁；迁移切换权威字段时也重新检查模型核心保护。

绑定使用 canonical table/field ID；用户修改显示名称不会失联。绑定核心表、字段的删除、退役、转换及不兼容定义修改在原生 schema authority 拒绝，用户扩展字段仍可正常编辑。禁用、卸载与清理插件私有存储不会移除已建立绑定或核心保护。业务余额和跨表写入由单独确认的 `permissions.transactions` 规则保护。每份声明最多 192 条规则、64 个十进制字段，工作区最多 64 条规则登记。新版本完整包含旧规则、十进制格式且复用其绑定时，写入只执行覆盖旧版本的规则集。其它旧登记继续约束写入。验证的前后镜像合计受 10,000 行和 1 MiB 限制。登记前按原生插入或删除的源表及其反向关系字段计算同一写入会触发的有效规则集合；超出上述预算会拒绝登记，并保留旧规则。

`sameTarget` 比较一行业务记录的两条关系路径，例如 `left:[{field:"version",target:"versions"},{field:"series",target:"series"}]` 与 `right:[{field:"item",target:"items"},{field:"series",target:"series"}]`。每条路径包含 1–4 个单值关系，两端必须抵达同一实体的同一条实际记录。`sameTarget` 的 `right:[]` 表示本行身份，左路径必须抵达本行所属实体。可选 `whenPresent` 只能等于左路径首字段：该单值指针尚为空时跳过；一旦赋值，后续每一跳均须存在。这样可在系列行上验证 `current → version.series` 恰好回到本系列，即使版本还没有明细。多值跳转、悬空引用、跨系列记录都被拒绝。每条来源分配可使用独立规则核对多个来源。

例如，批次例外可选指定义务。逻辑行使用 `whenPresent:"exception"` 和 `skipEmptyLeftEnd:true` 比较 `exception → obligation` 与本行义务。省略例外可照常建行。已有例外的末端指针为空时，右路径仍须有效，双方不作相等比较。末端非空时必须指向同一义务。字段缺失、中间断链或非空悬空引用均阻断写入。`atMostOne` 的 `fields` 必须是本行两个不同的已绑定单值关系。两者都空和仅填其一均可提交。两者都有值时整个写入被拒绝。

已登记的十进制字段可选 `sign:"nonnegative"`（允许零）或 `sign:"positive"`（严格大于零）。按精确系数校验，历史版本的已映射数值也遵循本字段的符号规则。省略 `sign` 时继续接受有符号值，适用于已有红冲记录。

十进制字段还可声明 `nullable:true`（默认 false，显式 false 等价）。仅显式 JSON `null` 合法；空串、缺省值或错误类型均拒绝，不会把 null 当零。nullable 不得用于 `conservation`、`balance` 的金额/限额字段，也不得用于任何 `reservationBalance` 的数量字段；适用于价格、税率等未知可为空的单据金额。

`parentFreeze` 声明 `{entity, parentPath, parentStateField, frozenStates, fields}`。`parentPath` 由 1–4 个已声明单值关系组成，指向持有阶段字段的父实体；父行在事务前镜像中已处于 `frozenStates` 时，子行不得删除、不得修改 `fields` 列出的核心字段、不得迁出，其他子行也不得迁入或新建到该父行之下。同事务新建并确认的父行及其最终子事实允许；未列入 `fields` 的扩展字段仍可编辑。路径逐跳解析子行旧/新两套祖先，改中间祖先不能绕过；路径断裂（父删除或悬空）fail closed 报 `transaction.parent_frozen` 或 `transaction.reference_violation`，不会静默跳过。父行自身的阶段转移仍由既有 `state` 规则保护。

`reservationBalance` 是固定结构的原生数量检查，声明字段名与关系映射，不是公式或脚本：`{obligation:{entity,quantity}, scope, quantity, purpose:{path,field}, normalPurpose, phase:{path,field,六个互斥阶段值}, instructionSource?, transferredFrom?, planItem?, plans?{entity,quantity,scope,logicalItem,share,currentWhen}}`。阶段与用途只沿 `owner_line → document` 路径取权威值，子行自报字段不参与判定。X 是指令执行量：对每份指令 h，Xh=所有已确认、明确消费 h 且用途与 h 完全相同（沿权威路径比较）的实际来源量；0≤Xh≤Qh；open 时 Rh=Qh−Xh，closed/transferred 时 Rh=0。E 是正常履约量：只计 normal 实际；正常容量只计 normal 实际与 normal 开放指令的 R，gift/afterSales 等用途有自己的 Q/X/R，不占正常义务额度。消费与 transferred_from 都要求完整 purpose 相等：normal→gift、gift→normal、gift→afterSales 均拒绝；已确认消费的用途不可更改或删除恢复 X；salesReturn 退回依 original_line 记录新事实，不得用 instruction_source 混作对旧指令的消费。稳定计划桶 U=normal E+normal 开放 R：当前计划存在时 U≤P 且桶占用 P，否则占用 U；全部桶与无计划直连合计不得超过义务数量，禁止 P+E 重复计数。换版不得丢失或缩小已执行份额（E>0 的 stableKey 必须仍在当前版本内）。转移必须同事务成对且同用途：余量>0 的转出指令有唯一转入指令，转入数量等于转出时 Q−X，范围/稳定份额一致，重复、自引用、循环、错数量、零余量造行均拒绝；仅新转入必须进入 open，历史承接允许随后 closed 或再次转移。已记录的 X 单调不降：退款或改用途不得释放既有执行。超发真实行不受限制，仅内部来源分配受预算约束。数量字段必须是非空、非负且同一精确格式的十进制声明（`sign` 为 `nonnegative` 或 `positive` 均可，零值仍由十进制层拒绝）；金额字段不参与预算。相关拒绝码：`transaction.reservation_invalid`、`reservation_exhausted`、`reservation_exceeded`、`reservation_released`、`reservation_frozen`、`reservation_transfer_invalid`、`reservation_plan_required`，以及 `transaction.parent_frozen`。

`balance` 可选 `currentWhen:{left:[...],right:[...]}`，两端相同才将该行金额计入目标余额。左路径通常指向行所属版本，右路径指向系列当前版本；仅允许右路径的最后一个指针为空，表示尚无当前版本。悬空引用仍拒绝。切换当前指针与多行分配均在原生事务的最终图上检查。历史行仍受十进制和关系校验。

`examples/plugins/model-conformance` 使用真实生产 SDK；离线测试宿主明确返回 `plugin_api_unsupported`。真实 WPF/WebView2 场景 `54-plugin-models` 通过正常打包、安装、原生 SendInput 确认覆盖空工作区取消后重新初始化、三表创建、异名手动映射、修复取消、空表字段重绑、历史数据阻断、原生重命名与合成用户字段值保留、版本升级。随后正常退出首个 Host，以第二个真实进程打开同一 workspace，核对新 session epoch、持久绑定与原值。逐步提交中断、安装 revision 变化和并发恢复另有真实 PocketBase 回归。原生输入仅允许受控 GitHub CI 的当前 Job-owned Host，执行前再次核对身份与前景窗口。
