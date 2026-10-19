# VibeTable 插件开发与 GitHub 发布

VibeTable 插件是离线优先的 `.vtplugin` 包。新代码页面通过隔离 WebView2 frame 的闭集 SDK 读取数据、
保留草稿并请求 MutationPlan 预览/保存；Host 展示精确计划并一次确认，Go 完成事务。
旧 1.x/2.x Worker 执行链已退役（#486）：旧包只保留只读解析、迁移诊断与保全，不再构建、安装或执行。
两条链均不能直接访问 PocketBase、SQLite、本机路径或任意网络。宿主在安装前展示 manifest
中的权限、兼容性和动作风险，用户批准后才会提交安装事务。

## 1. 权威工具与版本边界

- Manifest schema：新页面 `vibetable.plugin-manifest.v2`；旧 `vibetable.plugin-manifest.v1`
  （1.x/2.x Worker 包）已退役，仅保留只读解析供诊断与保全
- Plugin result：`vibetable.plugin-result.v1`（平台任务/审计仍使用；插件 SDK 不再暴露旧 Worker 返回类型）
- TypeScript SDK：`sdk/plugin/`（统一 surface API；旧 Worker capability 入口已删除）
- 开发与打包 CLI：`scripts/vibetable_plugin.py`（旧 v1 包 `build`/`pack` 固定拒绝 `legacy_worker_package`，
  `validate`/`inspect-permissions` 返回 `legacy` 迁移诊断）
- 包解析与完整性契约：`backend/infrastructure/plugin_package.py`
- 可运行示例：`examples/plugins/surface-data-editor/`（无 Worker surface 视图）、
  `examples/plugins/data-overview/`（surface 视图：describe/query 分页汇总）、
  `examples/plugins/normalize-text/`（headless 动作：rowGuards 批量规范化）、
  `examples/plugins/record-manager/`（无 Worker 完整应用）、`examples/plugins/team-tasks-app/`
  （无 npm 依赖的应用+headless）；`examples/plugins/weread-notes-dashboard/` 是冻结的
  旧 v1 Worker 历史包 fixture，只用于旧包诊断/保全测试，不参与构建

当前 SDK 只支持 Plugin API 3.x（surface）；包标记为 `private`，CLI 也属于 VibeTable 主仓，尚未发布到 npm 或独立工具仓。
外部插件仓库不得声明一个并不存在的 npm 版本；应在 CI 中 checkout 明确的 VibeTable commit，
并用该 commit 的 SDK/CLI 完成最终 validate/build/pack。固定 commit 需要由插件维护者显式更新，
这样一次 Release 的包契约不会随 `main` 漂移。旧 Worker 包（1.x/2.x）不能再用新 CLI 构建或安装；
固定在 #486 之前 commit 的旧插件源必须按下文迁移指南改写。

API 3.x 的已合并历史基线是 `ada6290921ffadf17a75b5018fbce144841e8594`（#483）。
它不包含本节 #484 的新初始化模板、显示辅助或离线 surface 工具；这些功能必须固定到包含 #484 的实际实现提交。
本节新 SDK、CLI、记录管理示例和 `data.releaseCursor` 固定到实现提交
[`d90238b21ba337a06b6795099007031245e90726`](https://github.com/FelixJI/VibeTable/tree/d90238b21ba337a06b6795099007031245e90726)。
#486 退役旧 Worker 链后，SDK 删除了旧 Worker 入口且 CLI 拒绝旧包；固定到 #486 之前
commit 的插件源码必须按迁移指南改写，新固定应选择包含 #486 的实现提交。
在 VibeTable checkout 中执行 `git checkout --detach <固定提交>`，
再按下文恢复锁定环境、构建和校验；Host 也须使用包含该提交能力的构建，不代表本轮已发布新版本。
开发时可在该已检出的固定源码里运行 `git rev-parse HEAD` 记录版本；不要把可变的 `main` 当作插件供应链 pin。

Host 产品版本取唯一版本源 `backend/_version.py`；SDK 的 `1.0.0` 和 manifest 的
`pluginApi: "1.x"` / `"2.x"` / `"3.x"` 是不同的版本边界。旧示例 `minHostVersion: "0.5.1"`，新示例为 `0.5.3`，不要求不存在的
Host 1.0。该最低版本号不承诺已发布的 0.5.1 包实现本分支新增合同；新增 rowGuards /
expectedDigest / data.releaseCursor 必须使用同一固定 commit 的 SDK、CLI 和正式构建的 Host 配套验证。本 Task
不升级产品版本、不发版。使用仓库锁定的 Python、Node/npm；Python 环境通过 `uv sync --frozen --group dev
--group build` 恢复，插件不会启动额外的数据库权威。

## 2. 推荐目录

### 2.1 无 Worker 的隔离页面 API

`manifest.v2` 必须声明 `compatibility.pluginApi: "3.x"`，动作不允许 `workerEntry`。
仅 `presentation: "surface"` 动作通过 `ui.customViews` 绑定 `actionId` 和 `entry` 页面，可另声明 `script`；
`application` 与 `headless` 动作分别使用自身的 HTML/JS `entry`，不声明 customView。
application 的 HTML 标准 `<script type="module" src="../dist/app.js">` 引用直接声明构建输出，
CLI 将同名 `src/app.ts` 打包，不需要额外的 surface/panel 动作。headless 的 `entry` 和
customView 的 `script` 同样由 CLI 将 `src/<script-stem>.ts`
编译到声明的 `script`，打包仍走既有完整性契约；`build` 在任何编译前先按包路径规则校验每个声明输出
（绝对路径、`..` 穿越与 reparse point 逃逸直接拒绝，且输出必须是 Host 实际执行的 `.js`/`.mjs` 成员、
不得覆盖 `manifest.json`、entry/html/style 或 schema 等插件输入），声明输出（含 `dist/` 之外的路径）执行与 `dist/` 产物
一致的自包含检查。`ui.customViews` 的 `entry/html/script/style` 引用必须是 Host canonical 形式
（拒绝 `./` 前缀、反斜杠与未规范化的 Unicode，不做静默改写）。旧 Host 拒绝 v2，未知 API/合同版本拒绝；
旧 SDK `data.mutate` 不改变拒绝语义。

```typescript
import { createSurfaceClient } from "@vibetable/plugin-sdk";
const api = createSurfaceClient(window.name);
const context = await api.context.read();
const metadata = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"] });
```

公开合同是 `contracts/plugin-surface-api.v1.json`。方法闭集为 `context.read`、`data.describe/query`、
`data.subscribe/unsubscribe`、`storage.get/set/delete`、`mutation.preview/submit`、`output.print` 和请求取消。SDK 没有通用 RPC、SQL、进程或路径代理。
查询复用 v2 原始值、computed freshness 和 canonical 字段；必须跟随 `nextCursor` 直到为空，
中途失败不能报告完整结果。新增 `rowGuards` 是既有 Go opaque expectedDigest，不暴露隐藏字段。
只有获准读取 `id` 时才返回 rowGuards，按 ID 更新或删除也要求该授权。`context.read` 保留绑定的用户上下文；
选择 ID 受同一授权控制，查询快照只返回获准字段的 canonical 条件及 schema/data revision，
隐藏条件或未授权关联目标使整份快照返回 null，不公开内部 cursor/snapshot/database 标识。

`api.output.print(signal?)` 仅允许当前可见、激活的 `application` 页面请求原生打印预览；
不另授读权限，输出是该页面已经获准读取并自行渲染的内容。Host 向 Go 重核当前安装、包、动作和
工作区身份，可信父页只保留该 iframe 的打印路径；平台表格、导航、任务与确认页脚、消息、
lookup/附件/字段/历史/记录抽屉及其它 portal 均排除。panel、headless、后台或旧 surface 不可打印。
参数固定为空对象，不能提交 HTML、URL、路径、选择器、目标打印机或使用 dashboard 的输出桥。
准备期间切换、退役或取消会拒绝开窗；预览期间离开应用会清空父页打印路径，直至浏览器结束打印态。
回执严格为 `{ opened: true }`，只表示宿主已调用原生预览；没有打印完成、成功或取消回执，
也不自动重试、回滚或直接发送到默认打印机。并发预览准备拒绝为 `plugin_print_denied`；
原生开窗异常为 `plugin_print_failed`，此时预览状态未知，保留隔离打印态直到浏览器结束打印或页面重载。Host 固定探测当前 frame 的布局高度（最多 100,000 CSS px），
超限返回 `plugin_print_limit`，不静默截断；插件自己的打印 CSS 及虚拟列表需自行提供适合打印的内容。跨页布局和预览快照时点以受控 WebView2 产品 QA 为准。
受控 QA 48 必须观察真实 browser print preview 的 PDF 页数与首末行，原生取消后验证真实
`afterprint` 清理并再次打印，再在真实预览打开期间导航，确认预览保持授权快照或为空。
证据记录于 `48-plugin-print-preview.json` 与实际预览截图；运行时若不暴露可观测的预览目标，
保存目标诊断并使场景失败，不能用 `emulateMedia` 或普通产品页截图替代。

写入计划使用 `vibetable.surface-mutation-plan.v1`，包含 `collection`、`schemaRevision` 和
`create/update/delete` operations；更新、删除必须带 `primaryKey` 和查询返回的 opaque `expectedDigest`，
删除不接受 `values`（包括 null），且必须显式声明 `permissions.data.operations` 的 `delete`。
每次计划仅覆盖一表的 1–1000 个操作，整批原子提交；任一非法操作、row guard 冲突或 restrict 引用
均零业务写入，不支持跨表原子事务。关系字段编辑沿用 canonical 校验并要求目标表的读取 grant。
`mutation.preview` 返回不可转移的
`planId`，`mutation.submit(planId, signal?)` 只请求宿主确认，不能传入 `approved`。
Host 展示真实操作样本和数量、绑定精确计划并单次消费；surface 的 actionId 和风险固定为实际
customView 对应的声明动作。同 entry 的不同动作持有独立 surface token 和调用上下文；
Go 独立核对当前 manifest 的 action/entry 绑定。read 页面不能请求写计划，
write/destructive 声明风险进入任务和持久审计；任何含 delete 的计划都升级为 destructive 原生确认，
write 动作也不能降低删除确认风险，renderer 不能批准。`requires.selection` 的 one-or-more/exactly-one
在 Host describe 时校验，不满足时不改变已绑定上下文。Interface 的动作按钮不进入 3.x 页面动作选择器，
已有绑定执行时明确提示需要独立插件页面。提交前及 MutationKernel 事务内重核安装
revision/status、权限、包修订。拒绝/提交前取消零写入，冲突返回权威错误；丢失或不完整回执为
`plugin_commit_unknown`，不得自动重放，迟到取消或工作区退役保留已读取并完整校验的 applied/replayed 回执成功；旧任务通知不投递到新会话。

`data.subscribe({ collection }, listener, signal?)` 订阅一个 canonical 表，返回 `{subscriptionId}`；
`data.unsubscribe(subscriptionId)` 立即移除本地回调并退订，AbortSignal 或 client.dispose 也会释放。
每个 surface 最多 8 个，整个 Host API 最多 64 个；超限为 `plugin_subscription_limit`。表 ID 最长
128 UTF-8 字节。必须有该表当前的 read/query grant；Go 在同一只读事务核对 installationId、revision、
status、包修订及表存在，订阅时与每次通知前都重新授权，不能转移 handle。

通知沿唯一 ProductRealtimeSession SSE 接线，仅发送获准表的失效信号；SDK 回调为
`{type:"invalidated", collection, sequence}`，不包含记录 ID、字段、原始事件或隐藏表信息。
sequence 是当前订阅的本地通知顺序，不是数据 revision；先完成 subscribe 再做初始 query，
此后收到信号重新 query，不把信号当数据。
繁忙时同一订阅只保留一个待发失效，Host 单工作者依次处理有界集合，不阻塞平台 SSE。
恢复连接会使当前获准表失效以便重查。权限变化/流终止/授权检查失败会结束订阅，回调
`{type:"closed", code}`（如 plugin_read_denied、plugin_cursor_stale、plugin_subscription_failed）；
frame 投递失败则退役该 surface，不自动重建或重试订阅。重新订阅应由仍有效的当前页面发起。
当前 SSE 已终止时，新的订阅会以 `plugin_subscription_failed` 拒绝；须关闭并重新打开工作区，
或等待真实的新 renderer/generation 绑定建立实时流后再申请，普通迟到事件不会重新开启准入。
最后的 frame post 在实际 UI 回调内核对 renderer、SSE session/generation、当前 surface 与订阅存活；
关闭、切换工作区、禁用/升级、退订后的迟到通知不能投递，SDK 丢弃重复/乱序通知并阻止已结束 handle 复活。

Host 在真实 frame 创建时捕获可信 iframe name/token，验证 WebMessage 的实际 source 文档，
并绑定安装、workspace UUID/sessionEpoch 和授权上下文。插件 payload 的 `pluginId/projectKey/scope`
不受信任且作为额外字段拒绝。私设由 Go pluginstore 按安装私有目录、CAS 和当前授权管理。
私设 key 必须符合 `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`，JSON 值最多 65536 UTF-8 字节。

每次真实保存 run 在确认前持久记录 `action.started`；记录失败不进入确认或提交。
成功的 `action` 审计和数据在同一 Go 事务提交，其他终态由原 run 身份记录，禁用不删除该历史。
回执丢失时 Host 的 `action.observed/unknown` 不覆盖 Go 已确定的成功审计。异常退出只留下
`action.started/unknown` 表示没有可确认的终态；宿主退役有界收束，无法访问原 generation 时不向新工作区补写。

每条消息及 Go请求上限 1 MiB，分页最多 200 行，每 surface 最多 8 个在途请求/8 个预览计划、
64 个游标句柄；普通请求 15 秒，Host 确认/提交请求 120 秒，句柄一次消费。长驻页面没有旧
Worker 的 15 秒/64 次总寿命。

`data.releaseCursor({cursor})` 是 Host 闭集方法；SDK 调用 `await api.data.releaseCursor(cursor)`。
消费下一页会替换当前句柄；放弃分页、回退重查、刷新字段或仅查询计数后，必须显式释放未消费的
`nextCursor`。Host 先核当前 surface/身份，只删除该 surface 自有句柄，不访问 Go，也不新增数据权限；
未知、外国和重复句柄都返回相同 `{released:true}`，不透露其是否存在。SDK 对自己未持有的句柄直接完成。
普通 8 个请求槽满时仍可释放；SDK 的游标释放与订阅释放共用另 8 个有界回执槽，重复在途释放共享回执。
失败或回执未知时拒绝并保留 SDK 所有权，不自动重试；调用方可显式重试释放或关闭客户端。
客户端关闭会发送仍持有句柄的释放请求；页面/surface 退役由 Host 清理其所有句柄。
查询取消通过原 requestId 同时回收迟到创建的句柄，客户端已收到但尚未交付的句柄也会释放。

完整示例和同包 `47-plugin-surface-api` 会在禁用旧 Node 执行器时
完成 550 条查询和拒绝/取消/批准保存；测试环境开关仅设置在该合成 Host 子进程。

### 2.2 应用与无界面动作形态（#482）

在 v2 / pluginApi 3.x 的 action 闭集内新增 `presentation`（`surface` | `application` | `headless`，
缺省 `surface`）与 `entry`：

- `presentation: "application"`：插件贡献**完整业务视口应用**。`entry` 必填，指向包内 HTML
  文档；同一个入口内部用 SPA hash 路由组织多个页面（Host 仅允许同文档 fragment 变化，路径/
  查询/新导航仍被拒绝并退役 surface）。应用不声明 `ui.customViews`、`workerEntry` 或旧
  `formSchema/inputSchema/outputSchema`（互斥，禁止静默忽略）。导航入口由当前工作区已安装、
  已启用且 surface 可用的 catalog 派生，身份是 `pluginId + actionId`，不建 Interface 实体；
  禁用/卸载/升级/缺包会移除入口，宿主导航与“返回平台/管理插件”始终可达。
- `presentation: "headless"`：无界面动作。`entry` 必填，指向包内 JS 模块；由 Host 内置受控
  文档（保留路径 `__vibetable_host__/headless.html`，包内同路径不可伪造）在同插件 origin 的
  隐藏受限 frame 内加载，同一 surface API/确认/预算；模块完成/失败时向可信宿主页发送
  close 事件（SDK `finishHeadlessRun`），宿主显示可见状态并释放 token/frame；用户可随时取消。
- `presentation: "surface"`（缺省）：行为与旧版完全一致（`ui.customViews` 绑定，entry 不允许
  出现在 action 上）。v1 清单拒绝新字段。

旧栈（未升级的 Python/Go）安装 application/headless 包会明确失败；新栈保持旧 v1/v2 包语义与
序列化字节不变（新字段为默认值时不写入清单）。完整示例见
`examples/plugins/team-tasks-app`（application 三页 hash 路由 + headless 动作，同一包、同一 API）与
`examples/plugins/record-manager`（类型化 SDK 构建的完整记录管理应用，见下节）。

```typescript
import { createSurfaceClient, finishHeadlessRun } from "@vibetable/plugin-sdk";
const api = createSurfaceClient(window.name);
const context = await api.context.read();
finishHeadlessRun(window.name, "succeeded", "已完成");
```

### 纯代码记录管理应用（#484）

`examples/plugins/record-manager` 是最小完整的纯代码业务应用：一个 `application` 入口内含
概览 / 列表 / 详情表单三个 hash 页面，加一个不依赖任何界面组件的 `headless` 动作；`npm run build`
经同一 CLI 将 `src/app.ts`、`src/audit.ts` 用仓库固定 commit 的 SDK 源码打包为自包含脚本，不引用未发布
npm 包，也不需要 Worker 文件或入口占位。

- 概览页只展示授权表/字段/关系与宿主权威 `totalRows`（一次 `pageSize:1` 探针），不把分页累计或截断结果当作全量。
- 列表页每次前进消费现有游标，仅查询一页；回退释放当前游标并重查至目标页，途中消费每个句柄，只保留一个继续页句柄。概览与无界面计数探针在 `finally` 释放继续页句柄。`data.query(request, signal?)` 可传 AbortSignal 取消在途查询（复用既有 `request.cancel`，取消错误为 `plugin_cancel_requested`，不自动重放）。原值与显示分离：显示用 SDK `describeCellValue`/`formatCellValue`/`isMissingCell` 纯辅助，`0`、`false`、空串与 `null`/缺失区分；`{state,value,fresh}` 只有在调用方声明该字段为 computed 时才解包，未 fresh 显示为计算中，不把格式化文本回写数据。
- 详情/表单页用 `mutation.preview` + `mutation.submit` 完成创建、更新、受控删除（delete 触发宿主破坏性确认）与预览后批量规范化（带 rowGuards 行保护；拒绝/取消零写入；`plugin_commit_unknown` 结果未知，不自动重放，需核对权威数据）。导出用 `data.export` 任务（csv/xlsx，字段显式列举）。
- 离线测试（`npm test`）用同一锁定 esbuild 打包真实 SDK 源码与 `src/records.ts`，宿主通道按公开合同伪造：550 条多页、空表、末页、变更、取消、授权不足、确认拒绝、冲突、未知提交不重放，以及两插件/两 surface token 隔离，全部用合成数据。
- 同包产品验收接入 `53-record-manager`：独立场景准备阶段用真实 CLI+packer 产出 `record-manager.vtplugin`，驱动安装、三页导航、创建/编辑/批量规范化原生确认、导出落盘、重开与禁用，截图与证据进入既有归档（本机未跑真实 WPF，由 CI 验证）。

表单显式使用 null 复选框；空文本 `""`、数字 `0` 和布尔 `false` 保持原值。更新仅提交发生变化的字段，
JSON/多选用 JSON 原值编辑，关系用 canonical 目标记录 ID（跨表关系还需目标表 read grant；当前示例只授 `$active`，
因此跨表未授权目标由 Host 拒绝）。附件、富文本 editor、计算和未知类型在此示例中明确只读；附件更改使用专属 API，
不会把显示文本回写。字段由概览的授权字段选择器控制，不截断为前六项。缺记录/row guard 的详情不能提交，
提交期间可取消；冲突、取消、拒绝后须重新查询行保护。unknown 只提示核对权威记录，不自动重放。
CSV 导出声明 `files: ["pickWrite"]`，原生保存选址与实际落盘仍归 Host。

离线 `createOfflineSurfaceHost` 仅供测试：复用 `createOfflineHost` 已与 Go 独立语料核对的 v2 查询验证、单次游标和 revision，
在 API 3.x 不施加旧 Worker 的终身 64 次预算。私设按可信 plugin/workspace 身份隔离，重开 token 不改变存储归属；
预览与提交必须显式提供合成 authority port，没有默认成功，也不连接真实业务库。查询独立语料为
`tests/contract/fixtures/plugin-capabilities-v2.json`；终态语料 `plugin-surface-outcomes-v1.json` 同时由真实
`PluginSurfaceApiTests` 与离线示例消费。原生 picker/确认、订阅和权限持久化仍以产品 QA47/48/49/51/52/53 为准，
DOM/离线测试不能替代 WPF 验收。

旧 Worker 包升级（强制迁移）：把业务计算保留为纯函数，数据读写改用 `createSurfaceClient`，写入使用 `mutation.preview/submit`；
入口改为 `application/headless`（或 surface `customView` + `script`）并移除 `workerEntry` 与旧输入/输出 schema。
安装/升级始终经过 Host 包计划确认，
新增 `delete`/`pickWrite`、改字段或授权表范围时由 Host 展示新的权限声明；插件不能自行批准。旧 Host 明确拒绝
v2/3.x 包；#486 起旧 Worker 执行链已删除，旧 v1/v2 包 inspect 返回迁移诊断、CLI `build`/`pack` 拒绝，
不存在兼容开关或改名必经框架。参考迁移：`examples/plugins/normalize-text`（旧 write Worker → headless）、
`examples/plugins/data-overview`（旧 read Worker + view → surface 视图）。本轮不改独立 WeRead 插件仓库；
`examples/plugins/weread-notes-dashboard/` 仅作为冻结历史 fixture 保留一个真实旧包 ZIP 供诊断/保全测试。

### 初始化三种无 Worker 包

```powershell
# 在包含 #484 的固定 VibeTable 源码中（真实提交 SHA 见上面的 pin 说明）
uv sync --frozen --group dev --group build
uv run --frozen --no-sync python scripts/vibetable_plugin.py init build/plugins/my-app `
  --plugin-id com.example.my-app --display-name "我的应用" --template application
# --template action 只有 headless 动作；--template combined 同时贡献应用和动作（默认）。
Set-Location build/plugins/my-app
npm ci
npm run typecheck
npm test
npm run build
Set-Location ../../..
uv run --frozen --no-sync python scripts/vibetable_plugin.py validate build/plugins/my-app
uv run --frozen --no-sync python scripts/vibetable_plugin.py inspect-permissions build/plugins/my-app
uv run --frozen --no-sync python scripts/vibetable_plugin.py pack build/plugins/my-app --output build/plugins/my-app.vtplugin
```

生成器复用仓库已锁定的 esbuild/TypeScript lock，包里不包含未发布的 npm SDK 依赖、Worker 文件或入口占位。
应用/动作都通过同一个 SDK；Node/npm 仅在构建时使用。独立插件目录在本次命令环境设置
`$env:VIBETABLE_SDK_ROOT = "已固定的 VibeTable checkout 路径"` 后使用相同 npm 脚本，路径不会写入源码或包。
类型检查临时配置只生成在该 checkout 的 `build/plugins/typecheck`。CLI 已不再接受旧链包：旧 v1
manifest 的 `build`/`pack` 固定拒绝，只读检查返回迁移诊断；新的 init 不生成旧链。

```text
manifest.json
package.json
package-lock.json
tsconfig.json
src/
  app.ts / action.ts
ui/
  index.html
dist/
  app.js / action.js（生成，不提交）
tests/
  *.test.mjs
.github/workflows/ci.yml
.gitignore
LICENSE
README.md
```

源码仓库应提交源文件、schema、测试、lock 和必要的静态视图源码。`build/`、`dist/`、
`release/`、ZIP 与 `.vtplugin` 是生成物，不应作为源码重复提交；正式 `.vtplugin` 只作为
GitHub Release 资产发布。本仓 tracked 示例的页面脚本输出（如 `dist/surface.js`、
`ui/normalize.js`）均由 CLI build 生成且不提交；唯一例外是 `examples/plugins/weread-notes-dashboard/`
冻结的旧 v1 历史包 fixture（含一个真实旧 ZIP，仅用于旧包诊断/保全测试），它不由 CLI 重建，
也不随升级改写。

## 3. 主仓内开发

从锁文件恢复依赖，不使用 `npm install` 改写依赖解析结果（以迁移后的 `data-overview` 为例；
`normalize-text`、`surface-data-editor`、`record-manager` 同）:

```powershell
Set-Location examples/plugins/data-overview
npm ci
npm run typecheck
npm test

Set-Location ../../..
uv run --frozen --no-sync python scripts/vibetable_plugin.py build examples/plugins/data-overview
uv run --frozen --no-sync python scripts/vibetable_plugin.py validate examples/plugins/data-overview
uv run --frozen --no-sync python scripts/vibetable_plugin.py inspect-permissions examples/plugins/data-overview
uv run --frozen --no-sync python scripts/vibetable_plugin.py pack examples/plugins/data-overview `
  --output build/plugins/data-overview.vtplugin
```

`build` 会用插件本地、锁定的 esbuild 把 v2 声明的页面脚本（application 模块引用、customView `script`、
headless `entry`）打成自包含 ESM，并拒绝动态 import、未打包
runtime import、网络全局与 WebRTC 出站 API（`RTCPeerConnection` 及其 webkit 别名）；
所有声明输出（即使位于 `dist/` 之外）同样纳入
这些检查与产物计数，包内其它宿主以 `text/javascript` 服务的 `.js`/`.mjs` 成员（大小写不敏感，
页面可经 `script-src 'self'` 加载）也在检查范围内；`src`/`tests`/`node_modules` 等本地开发输入不参与。
旧 `vibetable.plugin-manifest.v1`（1.x/2.x Worker）包在 `build`/`pack` 前置拒绝（`legacy_worker_package`），
不调用任何打包器，也不会重写旧 dist 产物；`validate`/`inspect-permissions` 对旧包保留只读检查并返回
`legacy: true` 与迁移提示。运行时纵深：Host 在首次 Navigate 前以 document-created 注入统一禁用两别名（不可写、不可配置），
构建检查为纵深，不单独承担封锁保证；不依赖 Runtime 尚不支持的 CSP `webrtc` 指令。
`pack` 会生成确定性 ZIP 和包内 `integrity.json`。不要手改
`dist` 或 `integrity.json` 后跳过 build/pack。

示例 `npm test` 会先调用上述真实 CLI build，再在合成 surface 宿主上测试编译后的模块（分页、拒绝、取消、
冲突与游标释放均用合成数据）。CLI 的 `validate`
检查包、manifest、schema 方言与静态 import；它不执行动作，也没有返回值校验
子命令。SDK 的 v2 查询/游标合同与 Go conformance 共用
`tests/contract/fixtures/plugin-capabilities-v2.json` 的独立预期；旧 `plugin-capabilities-v1.json`
Worker 执行语料随旧链一并退役。正式同包测试在产品 E2E 场景通过真实安装、Host 与 Go 验证示例。

## 4. Manifest 与最小权限

Manifest 只声明实现实际消费的 capability：

- `permissions.data`：按 collection、operation 和 field 限定数据访问；
- `permissions.files`：只有调用原生文件选择能力时才声明；
- `permissions.privateStorage`：只有使用插件私有设置时才声明；
- `permissions.network`：插件页面不能直接使用浏览器网络全局；不要用空 domain 或无消费者的
  method 预留未来权限；
- `actions[].risk`：必须与可观察行为一致，不能把 write/destructive 动作伪装成 read。

插件的公共类型应使用 SDK 的 `JsonObject`/`JsonValue`，不要用宽泛 `unknown` 或 `Any`
绕过 capability 契约。测试至少覆盖核心纯函数、空数据、字段映射和 manifest/静态资源存在性；
测试路径必须基于仓库根或 `import.meta.url`，不能硬编码开发者的本机目录。

## 5. 旧 Worker 包（1.x/2.x）退役与迁移

旧 `vibetable.plugin-manifest.v1` / `pluginApi 1.x`-`2.x` Worker 执行链已在 #486 删除：

- SDK 已移除旧 Worker 入口（`createCapabilityClient`、`PluginCapabilities`/`PluginAction` 类型、
  `mutationPlan`、`ok`/`failure`/`cancelled`、`startOfflineAction` 与旧 v1 conformance 离线执行器）；
  保留统一 surface API、`createOfflineHost`（v2 describe/query/cursor 合成数据宿主）与
  `createOfflineSurfaceHost`（surface 合成宿主）及其安全测试。
- CLI 对旧包 `build`/`pack` 前置拒绝（`legacy_worker_package`），不调用打包器、不重写旧 `dist`；
  `validate`/`inspect-permissions` 只读检查成功时返回 `legacy: true` 与迁移提示及真实 pluginId/version。
  真实旧包（如 weread fixture 的旧版 BOM `integrity.json`）在只读核验中 fail closed，不会执行旧代码。
- 旧包的安装、启动、禁用/重开与升级诊断由 Host/Go owner 提供（需要迁移/不兼容、身份与设置保全）；
  CLI 不提供旧包兼容开关。

迁移步骤（参考 `examples/plugins/normalize-text` 与 `examples/plugins/data-overview`）：

1. manifest 改为 `vibetable.plugin-manifest.v2` + `pluginApi: "3.x"`，动作改为
   `presentation: "application"`/`"headless"` 或缺省 `surface` + `ui.customViews`（含 `script`），
   删除 `workerEntry` 与 `formSchema/inputSchema/outputSchema`；
2. 业务计算保留为纯函数模块；数据读取改用 `createSurfaceClient` 的
   `context.read`/`data.describe`/`data.query`（200/页、游标单次消费、提前停止 `releaseCursor`）；
3. 写入用查询页的 `rowGuards` 作为 `expectedDigest`，经 `mutation.preview` → 宿主原生确认 →
   `mutation.submit` 单次原子提交；拒绝/取消零写入，`plugin_commit_unknown` 不自动重放；
4. headless 入口用 `finishHeadlessRun(window.name, status, summary)` 报告终态；
5. 旧输入表单改为页面内控件或 headless 内的确定性规则；同步更新测试为合成 surface 宿主驱动。

仍适用于新链的保障事实：

- rowGuards 是与同一次 Go 查询页对应的 opaque 元数据，按记录 id 索引，与获准的业务字段
  分离。它由 Go 权威行状态产生、MutationKernel 消费；插件把它放入 surface mutation 的
  `expectedDigest`，不用自己计算或解析。若预览后行发生变化，Go 原子拒绝 `mutation.digest_conflict`，
  不会用旧预览覆盖新值；缺 guard 时插件必须 fail closed，不得生成部分计划（见
  `examples/plugins/normalize-text/src/normalize-action.ts`）。
- 批准前，拒绝或取消都不提交。批准后的 Go 冲突按原码返回；丢失或不完整回执为
  `plugin_commit_unknown`。提交开始后取消不表示回滚；检查记录与审计再决定后续动作，
  宿主不会自动重放。已确认成功的任务不会被晚到取消覆盖。
- headless 模块完成/失败都通过 `finishHeadlessRun(window.name, status, summary)` 报告，
  summary 是给人看的纯文本；宿主任务终态与审计是权威。
- 离线 `createOfflineHost` 显式输入合成 permissions、descriptions、`queryAdapter` 与
  rowGuards；默认不授权。它只实现 v2 describe/query/cursor 合同（与 Go conformance
  语料 `plugin-capabilities-v2.json` 对齐），不再模拟旧 Worker 执行器。`createOfflineSurfaceHost`
  在其上叠加 surface 信封：mutation 预览/提交必须显式提供合成 authority port，没有默认
  成功，也不连接真实业务库。取消、进度、权限、未知提交等新链保障由 surface 测试与产品
  E2E 验证，离线 helper 不替代真实 Host 的 task registry。

## 6. GitHub Release 安装契约

VibeTable 插件中心支持输入 `owner/repo` 或标准 `https://github.com/owner/repo` 地址，从公共
GitHub 仓库的 latest 正式 Release 检查插件。首版不携带 GitHub token，因此私有仓库不在
支持范围内。

Release 必须满足：

1. 不是 draft 或 prerelease，并有 tag 与发布时间；
2. 正好包含一个状态为 `uploaded`、扩展名为 `.vtplugin` 的资产；
3. 资产大小为 1 byte 到 64 MiB；
4. GitHub API 为该资产提供合法的 `sha256:` digest；
5. `browser_download_url` 是 `https://github.com/...`；
6. 下载字节数与 Release metadata 一致，SHA-256 与 API digest 一致；
7. 下载后仍必须通过 VibeTable 既有包内 manifest、路径和 `integrity.json` 审查。

Release metadata 始终直连 `api.github.com`。`.vtplugin` 资产使用“设置 → 关于 → 软件更新”
中相同的下载通道：GitHub 直连、`ghproxy.net`、`gh-proxy.com` 或自定义 HTTPS 前缀。
第三方代理可能看到 GitHub 下载 URL；无论选择哪个通道，VibeTable 都会用直连 GitHub API
返回的 digest 验证最终字节。

插件中心只把仓库坐标交给原生宿主，不接受任意下载 URL，也不会把本机缓存路径暴露给
WebView。远程包下载后仍使用与本地 `.vtplugin` 相同的“检查计划 → 展示权限 → 用户批准 →
提交安装”事务；取消、替换计划、后端重启或退出会释放尚未提交的远程包缓存。

## 7. 独立插件仓库示例

微信读书可视化插件已迁移到
[FelixJI/VibeTable-WeRead-Notes-Dashboard](https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard)。
它展示了最小权限 manifest、独立 lock、聚焦测试、固定 VibeTable commit 的 CI，以及只把
`.vtplugin` 上传到正式 GitHub Release 的交付方式。


## Plugin API v2 受控读取（现属于 surface API 3.x）

`vibetable.plugin-data.v2` describe/query 合同继续作为统一 surface API（pluginApi 3.x）的
数据层；独立声明 `compatibility.pluginApi: "2.x"` 的 Worker 包已退役，不能新建或安装。
data grant 的 operations 同时声明 `read`、`query`。

先调用 `data.describe({ accepts: ["vibetable.plugin-data.v2"] })` 查看授权表，再传 `collection` 查看授权字段。查询使用返回的稳定 `fieldId`。元数据包含 logicalType、resultType、resultElementType、readonly、nullable、display、options、constraints、filterOperators、sortable；不返回公式源码、lookup 路径或 relation 目标字段。

```ts
const api = createSurfaceClient(window.name);
const description = await api.data.describe({
  accepts: ["vibetable.plugin-data.v2"], collection,
});
const request = { contract: "vibetable.plugin-query.v2" as const, collection,
  fields: ["id", fieldId], filters: [{ field: fieldId, operator: "gte" as const, value: 0 }],
  sorts: [{ field: fieldId, direction: "asc" as const }], pageSize: 200 };
let cursor: string | undefined;
try {
  do {
    const page = await api.data.query(cursor ? { ...request, cursor } : request);
    // items 保留原始 ID、0、false、null；不要用 truthy 判断缺值。
    consume(page.items);
    cursor = page.nextCursor ?? undefined;
  } while (cursor);
} finally {
  if (cursor !== undefined) await api.data.releaseCursor(cursor);
}
```

每个投影、筛选、排序字段都需授权；`ids` 需授权 `id`，最多 200 个。单页 1–200 条，默认 100；未知参数、raw SQL、关系路径、keyword/RPC 均不接受。筛选直接使用 Go QueryPort 的 operators：text 的 eq/ne/in/contains/starts_with/ends_with；number/date 的 eq/ne/in/gt/lt/gte/lte/between；bool/relation 的 eq/ne/in；select 的 eq/ne/in、multiSelect 的 contains；JSON 的 contains。普通字段均支持 is_null/is_not_null。实际字段能力以元数据为准。首版 text/number/bool/date 可稳定排序；select/relation/JSON/computed 不支持排序。

Formula/lookup 首版可投影、不可筛选和排序，结果为 `{state,value,fresh}`。非 fresh 时 value 为 null，page.complete 为 false；不会返回旧计算值和内部 diagnostic。关联字段返回原始关联 ID（多关联返回 ID 数组），不展开未授权目标表的标签。

游标是 surface 内随机、单次句柄；继续页必须保留相同 query。另一次查询、查询变化、workspace/session、安装修订或 schema/data/computed dependency 变化会失败，调用方应开始新查询并释放未消费句柄。沿用请求取消与 1 MiB 消息预算；长驻页面没有旧 Worker 的终身 64 次预算。

Go 在同一读取事务中读取 pluginstore 的 installation/status/grants/revision 并执行查询；插件不能传 scope。闭合 REST `/api/vibetable/v2/plugins/data` 只由 Host 的已鉴权会话使用，没有 renderer RPC 注册。

离线测试显式配置合成 Go 元数据 fixture 和 `queryAdapter`。adapter 接收闭合请求、返回 synthetic 权威分页结果；offlineHost 不实现另一套筛选引擎。缺少 adapter 明确失败。`data-overview` 示例展示元数据、筛选、稳定排序、全量分页与完整性。


v2 错误包括 `plugin_api_unsupported`（未协商/旧插件）、`plugin_read_denied`（授权不足/安装禁用）、`plugin_capability_invalid`（未知键/请求形态）、`plugin_query_limit`（页或ID上限）、`query.operator.unsupported`、`query.cursor.unsupported_sort`、`plugin_query_computed_unsupported`、`plugin_cursor_invalid`（句柄不存在/已消费/请求改变）、`plugin_cursor_stale`（安装或computed依赖修订改变）和 Go 的 `query.cursor_stale`（schema/data/clock变化）。取消、超时和消息上限继续使用 `plugin_cancel_requested` / `plugin_timeout` 等既有码，不得把失败结果解释为成功全量。

Go 真实生产者 conformance fixture 在 `tests/contract/fixtures/plugin-capabilities-v2.json`，SDK/offlineHost/BFF 均使用它。以合成库重建：在根目录设置 `VIBETABLE_PLUGIN_DATA_CAPTURE=../../../tests/contract/fixtures/plugin-capabilities-v2.json` 后，从 sidecar 执行 `go test ./internal/app -run '^TestPluginDataRealAuthorityPaginationAndGrants$' -count=1`；只归一化表/字段/记录/游标标识及schema修订，保留Go原值和元数据。真实跨进程本地合同设置 `VIBETABLE_PLUGIN_HOST_CONTRACT=1` 后运行 `go test ./internal/app -run '^TestPluginData' -count=1 -v`，需要仓库已同步Python依赖与Node；完整WPF安装执行由 `11-plugin-mutation` 产品场景覆盖。


### 应用与无界面动作的运行预算

`application` 页面只对每个请求/任务设置预算，没有 15 秒或 64 次的整页总限制。
`headless` 的两分钟执行预算从真实 WebView2 frame 创建开始，目录预分配 token 不启动计时。
模块主动完成、用户停止、禁用/卸载或 renderer 退役撤销该执行上下文；单个宿主任务完成不会结束整个模块。
同一 surface 可并发多个请求，宿主逐 taskId 展示、确认和取消；模块结束后所有已知任务回执仍可见，
某个任务成功不会覆盖另一个任务的 `plugin_commit_unknown`，模块完成也不代表所有写入已结算。未报告终态或 renderer 卡死时，
独立 WPF timer 撤权并重建当前 renderer，返回平台且不自动重放动作。该恢复保留 PocketBase 工作区。

卸载确认固定原安装的 `installationId` 与 `expectedRevision`，由 PocketBase 在同一事务中比较后删除安装、
包引用和用户明确选择的私有设置。升级、重载或卸载重装导致身份变化时拒绝原请求，保留新安装及其设置；
缺少原身份的旧调用或清理重试不会回退为无条件删除。
原生确认窗口默认将焦点放在拒绝按钮，Escape/关闭拒绝当前计划；过期与退役关闭确认窗口。

应用验收的系统高对比切换只在显式 `GITHUB_ACTIONS=true` 且 `CI=true` 的受控 Windows runner 中进行，
QA 父进程先保存原 flags/方案、按闭集操作切换，并在正常结束、失败和 driver 异常退出的 `finally` 恢复；
恢复失败使验收失败。本机不修改系统设置。内容 scale 与实际窗口 DPI 分别记录，不把内容缩放称为物理 DPI 变更。


### Plugin API 3.x 原生文件、附件和数据 IO

`createSurfaceClient(surfaceToken)` 的 `file` 接口复用 SDK 的 `ReadGrant` / `WriteGrant`。
`permissions.files` 必须显式声明 `pickRead` / `pickWrite`。选择与覆盖提示由原生 Host 完成，
取消返回 `null`。插件只收到 grantId、显示文件名与媒体类型，不收到路径。
每个 surface 最多持有 8 个文件 grant（普通文件与 IO 来源/目标共享），生命周期 5 分钟；普通文件最多 1 MiB，按
256 KiB 分块传输。`read()` / `write()` 一次消费，结束或 `dispose()` 释放。
`mediaTypes` 是调用方的类型提示，授权仍以用户在原生文件选择器中的实际选择为准。

```ts
const source = await api.file.pickRead({ mediaTypes: ["text/plain"] });
if (source) consume(await source.read());
const target = await api.file.pickWrite({ suggestedName: "result.txt", mediaType: "text/plain" });
if (target) await target.write(new TextEncoder().encode("result"));
```

附件通过 `attachments.list({ collection, recordId, fieldId })` 返回本 surface 的 opaque
`attachmentId`、文件名、类型和大小，最多缓存 64 个引用；重复列表复用引用。
`attachments.read(attachmentId)` 按块重查 Go 当前安装与字段授权，最多读取 1 MiB。
引用不包含 storedName、内部 stage handle、download capability 或路径，也不支持 iframe 下载。
关联/替换/移除先调用 `attachments.previewChange({ collection, recordId, fieldId, expectedDigest,
addFile, removeAttachmentIds })`，其中 addFile 触发原生选择；取消返回 null。
返回的 planId 交给 `mutation.submit`，由原生确认批准精确计划。Go 将 stage 绑定原安装、
surface、记录、字段及 Host 文件 run，并在 canonical `setAttachments` 事务中重查授权、
row guard、MIME/大小/数量策略。拒绝、过期和退役会丢弃未用 stage。

导入/导出使用已有 `HostDataIoTaskRegistry`，SDK 只获得本 surface 的任务句柄。
`task.status()` 读取进度/终态，`task.result(signal, onProgress)` 等待结果并报告进度；
`task.cancel()` 请求取消：先等待 Host 取消回执，再轮询到稳定终态并释放任务句柄后才 resolve。
resolve 不保证 worker 真被取消（可能早已 succeeded/failed）；取消回执本身不是终态。
跟踪失败会显式 reject，并在 surface 存活时保持任务占用，后续显式
`task.cancel()`/`task.status()`/`task.result()` 可恢复释放。关闭且未收到终态时，
重复 `task.cancel()` 仍 reject；已缓存终态的取消等待仍 resolve。
`result(signal, onProgress)` 收到取消回执后继续自己的轮询和进度通知，直到权威终态。
没有列出、广播或取消其它 surface/平台任务的入口。
普通请求与获准 IO 任务的取消回执各有 8 个槽，重复取消合并；SDK 对已知终态不再发送取消，
Host 拒绝外来 taskId。
导入源与导出目标由对应方法自行请求原生选择，不接受插件提供的路径或平台 grant。

```ts
const task = await api.data.previewImport({
  collection, schemaRevision, mode: "create_only",
  columnMapping: [{ sourceColumn: "名称", targetField: fieldId }],
});
if (task) {
  const preview = await task.result(signal, showProgress);
  if (preview.planId) await api.mutation.submit(preview.planId, signal);
}
const output = await api.data.export({ collection, fields: [fieldId],
  query: { filters: [], sorts: [] }, format: "xlsx", suggestedName: "result.xlsx" });
if (output) await output.result(signal, showProgress);
```

导入复用 Python `ImportService.preview` 和 Go import plan owner；Go 在事务中授权表、字段、
操作及 canonical 关系目标。upsert 仅接受获准唯一键，并在确认前固定 create/update、目标 ID、
规范化 values 与既有 row guard，提交时不重新匹配。最多单表 1000 行、768 KiB 导入计划；
跨表写入不受支持。预览返回完整 summary 与最多 20 行/128 KiB 样本；有错误时 planId 为 null。
预览不会按普通文件 EOF 规则消耗导入源。提交继续使用 `HostPluginTaskRegistry` 的原生精确确认。
显式关系映射复用已有 resolver，但目标表及匹配字段必须获得当前授权。

导出复用 `ExportService` 的 CSV/XLSX formatter，字段必须显式列举；隐藏 id 不自动加入，
关系默认只输出原始 ID，不提供关系 display label 或附件 ZIP 导出。
每一页以及原生最终保存前都复核当前安装/修订与表字段 grant，schema/data/clock 快照变化失败。
插件数据 IO 新增独立的 32 MiB 来源/输出预算，区别于普通文件的 1 MiB；XLSX 来源另限
128 MiB 展开内容及 4096 个 ZIP entry。格式化 spool 和最终 broker 均实施字节上限。
每 surface 最多 8 个 IO 任务、8 个导入计划，Go 全局最多 128 个插件导入计划与 128 个 stage。
这些是本插件入口的预算，不能推导为平台所有导出路径的既有保证。

grant 绑定 surface token、installationId/revision/package、workspace epoch、Host run 和用途。
关闭、禁用、安装替换、取消及 dispose 回收本 scope 的资源；旧 handle 不能转交新页面。
Go 撤权与 OS 文件替换不是跨系统原子事务：Go 授权后，最终替换仍必须经过 broker 的同步
workspace/generation/surface 门禁。已验证保存成功保持成功；缺少回执或输出任务异常保留
`plugin_commit_unknown` / unknown，不自动重放。清理/取消有界等待，无法确认停止时保留未知，
Go 临时计划仍受固定 TTL 与容量限制。常用拒绝码还有 `plugin_file_denied`、`plugin_file_limit`、
`plugin_read_denied`、`plugin_write_denied`、`plugin_plan_limit`、`plugin_plan_stale`、
`plugin_cursor_stale` 和 canonical mutation 冲突码。

新增 DataIO principal 只存在于 Host-only 任务组合和现有鉴权 surface HTTP 端口；没有新增
Product RPC 或旧 Worker 能力。旧 1.x/2.x Worker 包已退役（见第 5 节），不再有可执行的旧 grant/任务路径。

内部 Host-only `attachment.stage` 的完整 JSON/base64 编码请求上限为 2 MiB，解码后的附件仍限 1 MiB；
普通 frame 与其它 surface HTTP 请求保持 1 MiB。此编码预算不授予插件更大普通消息能力。
导出仅按 Go 授权 profile 的 `dataType=formula/lookup` 解包计算结果，且要求 ready/fresh；
普通 JSON 字段即使恰含 state/value/fresh 三键，也完整序列化原值。

### 无 Worker 页面的 UI 公共服务

`createSurfaceClient(token)` 同时使用当前 frame 的原生数据通道和可信父页
`https://app.vibetable.local` 的展示通道。父页严格核对 origin、source 和当前 surfaceToken；
UI 消息不会转成旧 Worker action 或通用 Product RPC。`client.ui.capabilities()` 返回当前闭集方法
及明确不支持列表。

- `context.read()` 先读取 Go 授权的项目、表、选择等数据上下文，再叠加父页此刻的 theme/locale/density。
  `context.current()` 只读取当前展示快照（mode、locale、density、CSS variables），
  `context.onChanged(listener)` 监听此展示快照并返回取消监听函数。这些展示值不是权限或身份。
- `tasks.list()` / `tasks.onChanged(listener)` 只提供当前 surface 创建的既有 Host 任务，含真实进度、
  成功摘要与安全错误；未知总量为 `null`。不暴露 input、原生批准预览、隐藏记录或其它插件任务。
  任务记录按 revision 去重并保持成功/失败/取消/aborted 终态粘滞，最多保留256条；
  超限保留活跃任务，并按本地受理顺序回收旧终态（不是按创建顺序裁掉刚结束的旧任务）。
  每条出站 `taskChanged`/`tasksChanged` 都使用同一精简投影；旧事件名不再携带父页确认或原始结果DTO。`aborted + plugin_commit_unknown` 表示结果未知，应核对实际数据，禁止自动重放。
- `tasks.cancel(taskId)` 仅请求取消当前 surface 拥有且可取消的任务。返回的 `cancelRequested` 只表示
  请求进入已有 Host 控制流程，仍须通过任务投影等待权威终态；不表示写入已取消或回滚。
  此通道没有任务创建、批准或插件自报进度接口。文件/导入导出等其它平台任务由对应受控能力返回，
  本列表不会把任意 DataIO/Worker taskId 当作当前页面的任务。
- `ui.notifications.show(text)` 在父页展示“插件 · 当前入口标题”归属的纯文本消息，不能插入HTML、
  自定义来源、按钮或安全批准UI；每条最多500字符、每surface生命周期最多8条、可见最多3条。
- `ui.navigation.back()` 请求返回平台，复用当前页面 token 核验和既有草稿离开守卫。
  用户拒绝丢弃草稿时返回 `{navigated:false}`。关闭页面不表示在途写入已取消，回执仍由Host保存。
  首版明确不支持表/记录/任意URL导航、任务创建和批准；这些不是空实现，也不会回退到内部RPC。

展示通道另有最多8个在途请求、20秒响应截止；失败不会自动重试或重放。`dispose()` 释放监听和待回复，
旧surface/工作区退役后消息不能进入新页面。

```ts
const client = createSurfaceClient(window.name);
const stopTheme = client.context.onChanged(theme => renderTheme(theme));
const stopTasks = client.tasks.onChanged(tasks => renderHostReceipts(tasks));
const context = await client.context.read(); // 权威数据范围 + 当前展示偏好
await client.ui.notifications.show("操作请求已发送，最终结果请见宿主任务记录。");
// 用户操作时：await client.tasks.cancel(ownedTaskId);
// 用户请求离开时：await client.ui.navigation.back();
// 卸载时：stopTheme(); stopTasks(); client.dispose();
```
