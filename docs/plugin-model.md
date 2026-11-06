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

绑定使用 canonical table/field ID；用户修改显示名称不会失联。绑定核心表、字段的删除、退役、转换及不兼容定义修改在原生 schema authority 拒绝，用户扩展字段仍可正常编辑。禁用、卸载与清理插件私有存储不会移除已建立绑定或核心保护。业务余额和跨表写入由单独确认的 `permissions.transactions` 规则保护。每份声明最多 192 条规则、65 个十进制字段，工作区最多 64 条规则登记。新版本完整包含旧规则、十进制格式且复用其绑定时，写入只执行覆盖旧版本的规则集。其它旧登记继续约束写入。验证的前后镜像合计受 10,000 行和 1 MiB 限制。登记前按原生插入或删除的源表及其反向关系字段计算同一写入会触发的有效规则集合；超出上述预算会拒绝登记，并保留旧规则。

`sameTarget` 比较一行业务记录的两条关系路径，例如 `left:[{field:"version",target:"versions"},{field:"series",target:"series"}]` 与 `right:[{field:"item",target:"items"},{field:"series",target:"series"}]`。每条路径包含 1–4 个单值关系，两端必须抵达同一实体的同一条实际记录。`sameTarget` 的 `right:[]` 表示本行身份，左路径必须抵达本行所属实体。可选 `whenPresent` 只能等于左路径首字段：该单值指针尚为空时跳过；一旦赋值，后续每一跳均须存在。这样可在系列行上验证 `current → version.series` 恰好回到本系列，即使版本还没有明细。多值跳转、悬空引用、跨系列记录都被拒绝。每条来源分配可使用独立规则核对多个来源。

例如，批次例外可选指定义务。逻辑行使用 `whenPresent:"exception"` 和 `skipEmptyLeftEnd:true` 比较 `exception → obligation` 与本行义务。省略例外可照常建行。已有例外的末端指针为空时，右路径仍须有效，双方不作相等比较。末端非空时必须指向同一义务。字段缺失、中间断链或非空悬空引用均阻断写入。`atMostOne` 的 `fields` 必须是本行两个不同的已绑定单值关系。两者都空和仅填其一均可提交。两者都有值时整个写入被拒绝。

已登记的十进制字段可选 `sign:"nonnegative"`（允许零）或 `sign:"positive"`（严格大于零）。按精确系数校验，历史版本的已映射数值也遵循本字段的符号规则。省略 `sign` 时继续接受有符号值，适用于已有红冲记录。

十进制字段还可声明 `nullable:true`（默认 false，显式 false 等价）。仅显式 JSON `null` 合法；新行缺键、空串或错误类型均拒绝，不会把 null 当零；更新省略该字段时保留原值。这些语义同时适用于插件事务和原生单表写入，原始空白单元格不作为显式 null。nullable 不得用于 `conservation`、`balance` 的金额/限额字段，也不得用于任何 `reservationBalance` 的数量字段；适用于价格、税率等未知可为空的单据金额。

`parentFreeze` 声明 `{entity, parentPath, parentStateField, frozenStates, fields}`。`parentPath` 由 1–4 个已声明单值关系组成，指向持有阶段字段的父实体；父行在事务前镜像中已处于 `frozenStates` 时，子行不得删除、不得修改 `fields` 列出的核心字段、不得迁出，其他子行也不得迁入或新建到该父行之下。同事务新建并确认的父行及其最终子事实允许；未列入 `fields` 的扩展字段仍可编辑。路径逐跳解析子行旧/新两套祖先，改中间祖先不能绕过；路径断裂（父删除或悬空）fail closed 报 `transaction.parent_frozen` 或 `transaction.reference_violation`，不会静默跳过。父行自身的阶段转移仍由既有 `state` 规则保护。

`reservationBalance` 是固定结构的原生数量检查，声明字段名与关系映射，不是公式或脚本：`{obligation:{entity,quantity}, scope, quantity, purpose:{path,field}, normalPurpose, phase:{path,field,六个互斥阶段值}, instructionSource?, transferredFrom?, planItem?, plans?{entity,quantity,scope,logicalItem,share,currentWhen}}`。阶段与用途只沿 `owner_line → document` 路径取权威值，子行自报字段不参与判定。X 是指令执行量：对每份指令 h，Xh=所有已确认、明确消费 h 且用途与 h 完全相同（沿权威路径比较）的实际来源量；0≤Xh≤Qh；open 时 Rh=Qh−Xh，closed/transferred 时 Rh=0。E 是正常履约量：只计 normal 实际；正常容量只计 normal 实际与 normal 开放指令的 R，gift/afterSales 等用途有自己的 Q/X/R，不占正常义务额度。消费与 transferred_from 都要求完整 purpose 相等：normal→gift、gift→normal、gift→afterSales 均拒绝；已确认消费的用途不可更改或删除恢复 X；salesReturn 退回依 original_line 记录新事实，不得用 instruction_source 混作对旧指令的消费。稳定计划桶 U=normal E+normal 开放 R：当前计划存在时 U≤P 且桶占用 P，否则占用 U；全部桶与无计划直连合计不得超过义务数量，禁止 P+E 重复计数。换版不得丢失或缩小已执行份额（E>0 的 stableKey 必须仍在当前版本内）。转移必须同事务成对且同用途：余量>0 的转出指令有唯一转入指令，转入数量等于转出时 Q−X，范围/稳定份额一致，重复、自引用、循环、错数量、零余量造行均拒绝；仅新转入必须进入 open，历史承接允许随后 closed 或再次转移。已记录的 X 单调不降：退款或改用途不得释放既有执行。超发真实行不受限制，仅内部来源分配受预算约束。数量字段必须是非空、非负且同一精确格式的十进制声明（`sign` 为 `nonnegative` 或 `positive` 均可，其中 `positive` 的零值仍由十进制层拒绝）；金额字段不参与预算。相关拒绝码：`transaction.reservation_invalid`、`reservation_exhausted`、`reservation_exceeded`、`reservation_released`、`reservation_frozen`、`reservation_transfer_invalid`、`reservation_plan_required`，以及 `transaction.parent_frozen`。

父冻结比较路径每一跳的行身份：即使最终仍指向同一单据，换所属行或重挂中间祖先也拒绝，不依赖这些关系是否列在 `fields` 中。

`reversalBalance` 为退回业务行增加独立的原行与数量检查，不要求该行具有来源分配：

```json
{"id":"delivery_return_capacity","kind":"reversalBalance","entity":"delivery_lines","reversal":{"original":"original_line","quantity":"quantity","purpose":"purpose","reversalPurpose":"salesReturn","originalPurpose":"normal","ownerPath":[{"field":"document","target":"delivery_documents"}],"ownerStateField":"phase","requiredStates":["instruction_open","instruction_closed","instruction_transferred","delivery_confirmed"],"confirmedState":"delivery_confirmed","sameFields":["material","project","package","unit","currency"],"sameOwnerFields":["agreement"]}}
```

`ownerPath` 为 1–4 跳单值关系，`original` 必须是指向同行实体的单值关系；`quantity` 使用既有非 nullable 十进制，`positive` 或 `nonnegative` 声明均可，保留其零值政策。`requiredStates` 为 1–32 个互异非空值，必须包含 `confirmedState`；两个快照字段列表必须显式给出，各最多 64 项且不得重复。所有字段、原行关系、父路径、父阶段与父快照都参与持久依赖及共同读取预算。

在最终事务图中，退回处于 `requiredStates` 时必须关联不同于自身的 `originalPurpose` 原行，且原行的父阶段为 `confirmedState`；`sameFields` 与 `sameOwnerFields` 比较存储的历史快照，不沿当前包件推导。双方 null 相等，null 与非 null 不相等。每条下达/确认退回量不得超过原量，仅处于 `confirmedState` 的退回按原行累计不得超过原量；原单与退回同事务确认合法。未进入 `requiredStates` 的草稿可以缺原行、暂指向未确认原行或保留超量输入，仍受原有 FK、类型和十进制校验；确认失败不截断输入或改用途。

确认历史须结合 `parentFreeze`/`state` 冻结原行关系、数量、用途、身份和父归属，未冻结的扩展字段仍可编辑。该规则不限制 normal 真实超发、不检查金额、不改变 reservation 的 X 或释放正常来源。仅修改父单 phase 的普通原生写入同样执行检查；登记规则时已有坏确认事实会拒绝登记，不自动清洗。拒绝码分别为 `transaction.reversal_invalid`（原行、自引、用途或阶段不合法）、`transaction.reversal_mismatch`（行或父快照不相等）、`transaction.reversal_exceeded`（单行或确认累计超量），都明确表示 not-committed。

计划来源的 `scope` 必须与关联计划项的义务一致，消费与转移同样适用。已下达指令或已确认实际引用的历史计划项，其 `scope/logicalItem/share` 稳定身份不可改写；新版本可以建立同稳定身份的新计划项并正常切换当前版本。

规则按 `kind` 校验字段归属。其他类型的字段即使显式给出 `null`、空对象、空列表或默认空字符串，也会在 Go 登记和 Python manifest/package 校验中拒绝。

`balance` 可选 `currentWhen:{left:[...],right:[...]}`，两端相同才将该行金额计入目标余额。左路径通常指向行所属版本，右路径指向系列当前版本；仅允许右路径的最后一个指针为空，表示尚无当前版本。悬空引用仍拒绝。切换当前指针与多行分配均在原生事务的最终图上检查。历史行仍受十进制和关系校验。

`examples/plugins/model-conformance` 使用真实生产 SDK；离线测试宿主明确返回 `plugin_api_unsupported`。真实 WPF/WebView2 场景 `54-plugin-models` 通过正常打包、安装、原生 SendInput 确认覆盖空工作区取消后重新初始化、三表创建、异名手动映射、修复取消、空表字段重绑、历史数据阻断、原生重命名与合成用户字段值保留、版本升级。随后正常退出首个 Host，以第二个真实进程打开同一 workspace，核对新 session epoch、持久绑定与原值。逐步提交中断、安装 revision 变化和并发恢复另有真实 PocketBase 回归。原生输入仅允许受控 GitHub CI 的当前 Job-owned Host，执行前再次核对身份与前景窗口。


### 开票份额、结算与更正依据

`allocationBalance`、`settlementBalance`、`changeEvidence` 是闭集声明；完整字段见
`contracts/plugin-allocation-rule.json`、`plugin-settlement-rule.json`、`plugin-evidence-rule.json`
和 SDK 类型。所有路径最多四跳单值关系；`AuthorityRef` 的空 `path` 表示本行字段。
安装和原生登记均校验逻辑字段及关系映射，原生写入按事务最终图重新检查。

`allocationBalance` 的 `owner` 指向票面行，父子共同使用 quantity/net/tax/gross 四字段。
净额加税额必须等于总额，每个份额与父效果同向，单份及累计幅值均不得超过父值；允许部分归属。
只有声明的草稿阶段可保持未知，null 不当零。父 `direction` 指定真实头的方向及载体约定：
`signed` 保留票面符号，`magnitude` 仅在红方向规范一次；内部归属始终保存 signed 效果。
`zeroQuantityPurposes`（如 priceDiscount）要求内部数量为已知零，票面商品数量继续原样保留。
蓝行显式 null 用途合法；字段缺失、类型错误、断裂路径不会变成 null。
负数量蓝归属及正金额红归属拒绝；signed 蓝负金额可保留同向份额，但不能充当红预算的非负原蓝容量。
原始票头/票面尚未归属、缺原蓝或含异常时可以单独登记，归属失败不截断原始事实。

`settlementBalance` 的 `capacity/request/actual` 共享一至四个精确 measure。普通蓝桶用真实义务数量，
红桶用原蓝 allocation 的同一组四个 signed 字段，不另建蓝容量或可写效果副本。
先按权威 direction 选角色，再检查用途、原蓝资格、容量与 application source；正式红份额不得缺原蓝，
原蓝必须真实 blue、达到 actual.settledStates、非自身且容量非负；同事务确认后可引用，已有红占用时不得降回草稿。不同红原因共用原蓝桶。原始未归属票面不参与预算。
可选 `currency`、`unit`、`material` 都必须在三方同时声明为 AuthorityRef；币种三方明确一致，
quantity 路径单位明确一致，已知物料 FK 三方不得冲突。物料显式 null 表示未映射，用户选定具体容量/来源
构成该份额的匹配依据；缺字段、错误类型和断裂路径仍拒绝。amount 路径跳过物料/单位数量匹配。
合同等真实 FK 通过既有 `sameTarget` 组合校验，不复制票面或引入拼接键。

quantity 来源 Q>0，已执行 E 与明确关闭 C 满足 E+C<=Q；剩余净额/税额分别按
`原额-roundHalfAway(原额*(E+C)/Q)` 计算，再由两者之和得到剩余总额。
数量全部兑现时，即使实际金额有价差也不保留申请金额预留，实际原始差异仍保留。
若同时声明 request.purpose、request.basis 与 actual.zeroQuantityPurposes，正式申请按同一零数量用途集合核对 basis：priceDiscount 必须 amount，数量用途必须 quantity；草稿可暂不完整。
amount 来源数量必须明确为零，只通过 `fulfillKinds` 的明确金额事实或关闭事实释放预留。
`adjustment` 先按 kind 选择结算事件；关闭可无 actual，兑现必须关联同 source、同 capacity 的真实 allocation，
累计兑现不得超过该 allocation 的实际效果。关闭和兑现复用 adjustments 的同一组四范围字段。
`closeRemaining` 必须捕获本次动作前剩余的固定四范围；之前仅数量关闭已经按比例消费预留，不能将其缺省金额误读为零释放或改写历史。
不能只存 bool 动态重算；关闭后不允许新增或增加同源消费，
历史执行保留。归属与兑现可在一个事务中创建，按最终图验算，不要求先经过非法中间图。
原先开放或本事务从草稿确认的 source 才能承接新增消费。

`plans` 仅支持 quantity-only 声明，不允许同时声明金额 measure 后静默跳过金额检查。
普通桶的 `plans` 直接复用 ReservationPlans 的 currentWhen、版本、logicalItem、share、义务及真实 planItem，
占用为当前计划容量加直接申请剩余和直接执行；计划内申请/执行共同受该计划桶约束。
义务10、当前计划8、直接申请6会因合占14拒绝，不复制计划余额。

`changeEvidence` 的 `fields` 保护核心事实，`ownerPath:[]` 可保护票头自身；既存归属的修改、删除、重绑均属于更正。`allowInsert` 省略时仍要求既有 owner 下新增子行有新依据；
自然 invoice_allocations 可声明 `allowInsert:true`，允许原始票先登记后正常新增归属，无须伪造更正事件。
该选项只豁免新增子行分支，旧行修改/删除/owner路径迁移不豁免，其他守卫照常执行；票面行新增仍使用默认严格策略。
显式 false、null 或非 bool 都拒绝。更正须同事务新建有效证据并绑定 owner 的 latestEvidence，证据反向 owner 必须一致，
`types` 只列更正可用 kind，说明须非空。旧证据不能复用，已记录声明事实不可改删；非声明扩展字段仍可编辑。
请求关闭等事件 owner 可为 null，不能充当实际更正依据，但其说明及声明范围仍被冻结。
保护字段与证据字段属于不同实体，可以同名且分别去重；冻结阶段守卫继续独立生效。
同事务 before/after、changeSetId、原生行历史和历史恢复均复用既有 audit；不新增 SDK history API。

拒绝使用 `transaction.allocation_invalid/mismatch/exceeded`、`transaction.settlement_invalid/exceeded`、
`transaction.evidence_invalid/frozen/required/reused`，均表示 not-committed；十进制格式错误保留既有 decimal 码。


`allocationBalance.requireCompleteWhenAllocated:true` 用于申请来源：没有任何 child 时允许父原始/草稿事实独立保存；
一旦有 child，quantity/net/tax/gross 必须完整覆盖父四维，null/未知父或子不能借草稿分支绕过完整性。
actual allocation 省略该选项，继续允许部分归属。该 enabled option 只接受显式 true，false/null/非 bool 拒绝。

`sameTarget.skipEmptyRightEnd:true` 与 skipEmptyLeftEnd 对称，仅允许对应路径末端的明确 null/空关系；
双方均已知时仍须同一目标。中间路径缺失、悬空、错类型和缺少末端字段均拒绝，最多四跳不变。
省略维持原行为，显式 false/null/非 bool 拒绝，其他 rule kind 不接受该字段。

quantity 原蓝 capacity 除真实票面 material 外，还保留其已有 application_source 所指 Request.Material 的已知事实；
raw material=null 不能抹除来源中的已知物料。已知 raw、继承来源、red source 与 actual 之间不得冲突；
明确无申请来源且 raw 未映射仍可显式匹配；amount 不套用数量物料规则。此过程不复制/回填 raw 字段。
更正依据按 Types 判角色：属于更正 Types 必须关联真实非空 owner，非更正类型只允许 owner 空的请求事件；
不能把 ghost correction 的空 owner 当普通关闭/兑现事件。


state 可选 relationCases，键必须与 transitions 完全一致（最多32），每case明确 required/optional RelationStep列表，合计最多64；同case不得重复/重叠，跨case同field须同target。所有case构成受控关系集合：required须存在非空真实单值FK；optional须存在，可明确空；当前case未列的受控字段须明确空。缺字段、错类型、坏FK均拒，完整最终图原子修正可成功；原迁移/冻结仍生效。全部case关系/目标参与读取、绑定和保护，不只读取当前case。省略保持旧行为；显式null、未知字段、非法case键或列表均拒。

groupSameTarget 用 group（1–4跳）分组，对 targets（1–64个 selector）收集同一目标实体的已知FK；末端明确空为unknown，中间缺失/错型/坏FK拒。selector.whenPresent若有须为path首字段，仅跳明确空。related可选且只指同entity；只展开所引用anchor的同一targets一次，不递归，也不读取该anchor的兄弟。每组已知目标最多一个。reference可选，group终点须同外层，每行只将自己的targets与已初始化G比较；不写回G、不在不同reference行之间传播。reference不存在的G拒绝。全部关系/实体/presence受原生Read、绑定与最终图守卫约束；冲突复用transaction.same_target_violation。完整wire见contracts/plugin-group-same-target-rule.json。它与同票守卫、单anchor继承、申请对原蓝整票的单向比较共同闭合合同，而非递归连通分量合并。
