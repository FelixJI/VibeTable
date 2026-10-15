# VibeTable 插件开发与 GitHub 发布

VibeTable 插件是离线优先的 `.vtplugin` 包。新代码页面通过隔离 WebView2 frame 的闭集 SDK 读取数据、
保留草稿并请求 MutationPlan 预览/保存；Host 展示精确计划并一次确认，Go 完成事务。
旧 1.x/2.x 包暂时保留 Worker 链。两条链均不能直接访问 PocketBase、SQLite、本机路径或任意网络。宿主在安装前展示 manifest
中的权限、兼容性和动作风险，用户批准后才会提交安装事务。

## 1. 权威工具与版本边界

- Manifest schema：旧包 `vibetable.plugin-manifest.v1`；新页面 `vibetable.plugin-manifest.v2`
- Plugin result：`vibetable.plugin-result.v1`
- TypeScript SDK：`sdk/plugin/`
- 开发与打包 CLI：`scripts/vibetable_plugin.py`
- 包解析与完整性契约：`backend/infrastructure/plugin_package.py`
- 可运行示例：`examples/plugins/surface-data-editor/`（无 Worker）、`examples/plugins/data-overview/`、`examples/plugins/normalize-text/`（旧链）

当前 SDK 支持 Plugin API 1.x/2.x/3.x，包标记为 `private`，CLI 也属于 VibeTable 主仓，尚未发布到 npm 或独立工具仓。
外部插件仓库不得声明一个并不存在的 npm 版本；应在 CI 中 checkout 明确的 VibeTable commit，
并用该 commit 的 SDK/CLI 完成最终 validate/build/pack。固定 commit 需要由插件维护者显式更新，
这样一次 Release 的包契约不会随 `main` 漂移。

Host 产品版本取唯一版本源 `backend/_version.py`；SDK 的 `1.0.0` 和 manifest 的
`pluginApi: "1.x"` / `"2.x"` / `"3.x"` 是不同的版本边界。旧示例 `minHostVersion: "0.5.1"`，新示例为 `0.5.3`，不要求不存在的
Host 1.0。该最低版本号不承诺已发布的 0.5.1 包实现本分支新增合同；新增 rowGuards /
expectedDigest 必须使用同一固定 commit 的 SDK、CLI 和正式构建的 Host 配套验证。本 Task
不升级产品版本、不发版。使用仓库锁定的 Python、Node/npm；Python 环境通过 `uv sync --frozen --group dev
--group build` 恢复，插件不会启动额外的数据库权威。

## 2. 推荐目录

### 2.1 无 Worker 的隔离页面 API

`manifest.v2` 必须声明 `compatibility.pluginApi: "3.x"`，动作不允许 `workerEntry`；通过
`ui.customViews` 为每个动作绑定有效的 `actionId` 和 `entry` 页面，可另声明 `script`。
CLI 的同一个 esbuild 构建入口将 `src/<script-stem>.ts`
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
write 动作也不能降低删除确认风险，renderer 不能批准。旧 Worker 的 MutationPlan 仍只接受 create/update。
`requires.selection` 的 one-or-more/exactly-one
在 Host describe 时校验，不满足时不改变已绑定上下文。Interface 的动作按钮目前仅支持 Worker 动作，
3.x 页面动作不进入其选择器，已有绑定执行时明确提示需要独立插件页面。提交前及 MutationKernel 事务内重核安装
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
Worker 的 15 秒/64 次总寿命。完整示例和同包 `47-plugin-surface-api` 会在禁用旧 Node 执行器时
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
`examples/plugins/team-tasks-app`（application 三页 hash 路由 + headless 动作，同一包、同一 API）。

```typescript
import { createSurfaceClient, finishHeadlessRun } from "@vibetable/plugin-sdk";
const api = createSurfaceClient(window.name);
const context = await api.context.read();
finishHeadlessRun(window.name, "succeeded", "已完成");
```

```text
manifest.json
package.json
package-lock.json
tsconfig.json
src/
  <worker>.ts
views/
  <view>/index.{html,css,js}
schemas/
  action-input.v1.json
  action-output.v1.json
tests/
  *.test.mjs
.github/workflows/ci.yml
.gitignore
LICENSE
README.md
```

源码仓库应提交源文件、schema、测试、lock 和必要的静态视图源码。`build/`、`dist/`、
`release/`、ZIP 与 `.vtplugin` 是生成物，不应作为源码重复提交；正式 `.vtplugin` 只作为
GitHub Release 资产发布。本仓现有 tracked 示例 Worker 也必须由 CLI build 更新；升级入口
名称时删除旧入口，提交对应的新生成 Worker，不手改编译结果。

## 3. 主仓内开发

从锁文件恢复依赖，不使用 `npm install` 改写依赖解析结果：

```powershell
Set-Location examples/plugins/data-overview
npm ci
npm run typecheck
npm test

Set-Location ../../..
uv run python scripts/vibetable_plugin.py build examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py validate examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py inspect-permissions examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py pack examples/plugins/data-overview `
  --output build/plugins/data-overview.vtplugin
```

`build` 会用插件本地、锁定的 esbuild 将 Worker 打成自包含 ESM，并拒绝动态 import、未打包
runtime import、Worker 网络全局与 WebRTC 出站 API（`RTCPeerConnection` 及其 webkit 别名）；
v2 surface 声明的 `script` 输出（即使位于 `dist/` 之外）同样纳入
这些检查与产物计数，包内其它宿主以 `text/javascript` 服务的 `.js`/`.mjs` 成员（大小写不敏感，
页面可经 `script-src 'self'` 加载）也在检查范围内；`src`/`tests`/`node_modules` 等本地开发输入不参与。
运行时纵深：Host 在首次 Navigate 前以 document-created 注入统一禁用两别名（不可写、不可配置），
构建检查为纵深，不单独承担封锁保证；不依赖 Runtime 尚不支持的 CSP `webrtc` 指令。
`pack` 会生成确定性 ZIP 和包内 `integrity.json`。不要手改
`dist` 或 `integrity.json` 后跳过 build/pack。

示例 `npm test` 会先调用上述真实 CLI build，再测试编译后的 Worker。CLI 的 `validate`
检查包、manifest、schema 方言、Worker 入口与静态 import；它不执行动作，也没有返回值校验
子命令。返回值由实际 Node Worker 的 Python closed DTO 校验；SDK 与 Worker 测试共用
`tests/contract/fixtures/plugin-capabilities-v1.json` 的独立预期。正式同包测试在
`11-plugin-mutation` 场景通过真实安装、Host、BFF 与 Go 验证两个示例。

## 4. Manifest 与最小权限

Manifest 只声明实现实际消费的 capability：

- `permissions.data`：按 collection、operation 和 field 限定数据访问；
- `permissions.files`：只有调用原生文件选择能力时才声明；
- `permissions.privateStorage`：只有使用插件私有设置时才声明；
- `permissions.network`：Worker 当前不能直接使用浏览器网络全局；不要用空 domain 或无消费者的
  method 预留未来权限；
- `actions[].risk`：必须与可观察行为一致，不能把 write/destructive 动作伪装成 read。

Worker 的公共类型应使用 SDK 的 `JsonObject`/`JsonValue`，不要用宽泛 `unknown` 或 `Any`
绕过 capability 契约。测试至少覆盖核心纯函数、空数据、字段映射和 manifest/静态资源存在性；
测试路径必须基于仓库根或 `import.meta.url`，不能硬编码开发者的本机目录。

## 5. SDK v1 执行契约

| 接口 | 当前行为 | 权限与边界 |
| --- | --- | --- |
| `data.read(request)` | 支持，返回 `{items,nextCursor,rowGuards}` | `permissions.data` 的 read/collection/fields；`$active` 对应 context.collection，`$configured` 对应产品可读字段 |
| `data.mutate(plan)` | 已弃用，始终拒绝 `plugin_direct_mutation_unsupported`，类型为 `Promise<never>` | write 动作返回 MutationPlan，由 Host 确认后提交 |
| `file.pickRead` / grant.read | 支持，取消选择返回 null | 声明 pickRead；原生一次性 grant，只读所选内容，不暴露本机路径；最大 1 MiB |
| `file.pickWrite` / grant.write | 支持，取消选择返回 null | 声明 pickWrite；提供 suggestedName/mediaType；仅写所选 grant，最大 1 MiB |
| `storage.get/set/delete` | 支持，get 缺键返回 null | privateStorage；插件私有命名空间，key 为 1–128 个允许字符，JSON 值最大 64 KiB |
| `context.read()` | 支持，返回当前 CommandContext | 当前项目、表、选择、querySnapshot、locale/theme/density/user/hostVersion；不是可变的全局状态 |
| `ui.reportProgress(progress)` | 支持，返回 `{cancelRequested:boolean}` | current/total 为非负整数，total>0 时 current<=total；进度单调，cancellable 原请求值保留 |
| `ui.emitResult(result)` | 已弃用，始终拒绝 `plugin_emit_result_unsupported`，类型为 `Promise<never>` | 直接 return 最终 PluginResult；不存在独立结果推送消费者 |
| `permissions.network` | 未提供 Worker 网络 capability | 不声明占位权限；Worker 不可直接调用网络全局 |

`data.read` 必须显式提供 collection 和 fields；字段值保持原始 JSON，包括 0、false、null、
数组与对象，不转换成展示文本。`fields:["*"]` 仍受权限和产品字段限制。v1 的 filter 只接受
缺省、null 或 `{}`，非空值稳定拒绝 `plugin_filter_unsupported`。pageSize 缺省 100，整数
夹在 1–200；null、布尔和非整数拒绝。`totalRows` 是该次查询的权威总行数，与 items
同页返回，不额外消耗调用。nextCursor 非空时继续读取，满页后可能还需读取一个
空页才能结束。cursor 是 offset 字符串；宿主兼容既有 Python 整数形式（如 `"1_0"`），插件
应直接转发收到的 nextCursor，不自行拼装。

单次 Worker 默认预算为 15 秒、最多 64 次 capability 调用、单条 JSON 消息最大 1 MiB；
宿主最多并行两个 Worker。分页、文件和进度调用都消耗该预算；这些限制没有因示例而放宽。
逐 200 行扫全表同步计数会撞上调用预算：6200 行的表需要 1 次 `context.read` 加 32 次读页
和 32 次进度，共 65 次调用，已超过 64 次上限。只需计数时用一次 `pageSize:1` 读取并采用
返回的权威 `totalRows`（见 `examples/plugins/data-overview/src/overview.ts`），不新建计数
接口，也不放宽预算。

rowGuards 是与同一次 Go 查询页对应的 opaque 元数据，按记录 id 索引，与获准的业务字段
分离。它由 Go 权威行状态产生、MutationKernel 消费；插件把它放入 operation.expectedDigest，
不用自己计算或解析。若预览后行发生变化，Go 原子拒绝 `mutation.digest_conflict`，不会用
旧预览覆盖新值。旧 expectedDateUpdated 已弃用；非 null 只接受已有 `row_` 加至少四位数字
的 revision，映射 Go expectedRevision。日期、digest、空字符串稳定拒绝，不能被忽略。

read 动作使用 `PluginReadAction<Input, Output>` 返回 PluginResult；write/destructive 动作
使用 `PluginWriteAction<Input>` 返回 MutationPlan（最多 10,000 operations，affectedCount
必须等于 operations.length）。两个类型都只需 input、capabilities 两个参数。旧 PluginAction
返回 union 保持兼容，但第三参数是已弃用的协作取消快照，不是真正 AbortSignal，没有 DOM
取消事件。每次耗时循环 await reportProgress 后检查 receipt.cancelRequested；Host 的公开
task 终态仍是取消与成功的权威。`cancellable:false` 只表达该进度的协作取消提示，不移除
Host 对任务的取消管理能力。Host 取消在 Worker 执行阶段先设置取消标记，并给最多约 1 秒
协作宽限（允许一次进度回执与清理），仍未结束则强制终止 Node 进程；确认与提交阶段不设
宽限，取消立即生效；提交开始后的取消保持现有 `aborted`/`plugin_commit_unknown` 边界，
不会重放。

```typescript
import { ok, mutationPlan, type PluginReadAction, type PluginWriteAction } from "@vibetable/plugin-sdk";

export const read: PluginReadAction<{}> = async (_input, capabilities) => {
  const { collection } = await capabilities.context.read();
  if (!collection) throw new Error("请选择表");
  const page = await capabilities.data.read({ collection, fields: ["id"], pageSize: 200 });
  return ok({ countOnPage: page.items.length });
};

export const write: PluginWriteAction<{ field: string }> = async ({ field }, capabilities) => {
  const { collection, selectedKeys } = await capabilities.context.read();
  if (!collection || selectedKeys.length !== 1) throw new Error("请选择一条记录");
  const page = await capabilities.data.read({ collection, fields: ["id", field] });
  const row = page.items.find(item => String(item.id) === String(selectedKeys[0]));
  if (!row || typeof row[field] !== "string") throw new Error("当前页没有选中的文本记录");
  const before = row[field];
  const after = before.trim();
  const expectedDigest = page.rowGuards[String(row.id)];
  if (!expectedDigest) throw new Error("缺少宿主行保护");
  const operations = before === after ? [] : [{ kind: "update" as const,
    primaryKey: String(row.id), expectedDigest, values: { [field]: after } }];
  return mutationPlan(collection, operations, {
    affectedCount: operations.length, sampleRows: [{ before, after }],
  });
};
```

完整分页与选择处理见 `examples/plugins/normalize-text/src/normalize.ts`；权威计数（一次
`pageSize:1` 读取 `totalRows`）见
`examples/plugins/data-overview/src/overview.ts`。PluginResult 的最小形状是
`{contract:"vibetable.plugin-result.v1",status:"success"|"warning"|"error",summary:string}`，
可加 metrics、table、refresh、warnings、artifacts。`ok(data)` 把原始 JSON 放在 table.data，
`failure(code,message)` 把错误码放在 table.code；这些是结果内容，不自动改变 Host task 的
执行终态。MutationPlan 的最小形状是 `{contract:"vibetable.mutation-plan.v1",collection,
operations,preview:{affectedCount}}`。返回额外未声明 wire 字段会被 closed DTO 拒绝。

批准前，拒绝或取消都不提交。批准后的 Go 冲突按原码返回；pending、提交传输失败或不完整
回执会成为现有 `aborted` 终态与 `plugin_commit_unknown`。提交开始后取消不表示回滚；检查
记录与审计再决定后续动作，宿主不会自动重放。已确认成功的任务不会被晚到取消覆盖。

`startOfflineAction` 的第四参数 `risk` 应与 manifest 动作一致，默认 `"read"`；写动作测试使用
`startOfflineAction(run, input, host, { risk: "write" })`，破坏性动作使用 `"destructive"`。
风险错配不会进入确认或提交；返回计划先按 Worker 校验写权限，空计划也不例外。

离线 `createOfflineHost` 显式输入合成 collections、fields、permissions、rowGuards；默认不
授权读写。`cancelRequested` 与进度单调基线按单次 `startOfflineAction` 执行隔离：取消或超
时后的旧异步动作即使晚返回，也只影响自己的执行，同一宿主上的并行运行互不串扰；宿主级
`requestCancel` 只作用于直接经 `host.capabilities` 发起的调用。storage、progressEvents 历史、
mutationPlans 与 writtenFiles 仍由宿主共享，用于跨执行观察。write 测试还必须提供合成产品可写 profile，例如
`writableFields: { articles: { create: [], update: ["title"] } }`；集合与本次操作的字段列表须显式
配置，缺少时在确认前以 `plugin_action_failed` 拒绝。它独立于读取 `fields` 和 manifest 权限，
不会从字段名称推断可写性。write 测试还须提供 approveMutation 与合成 applyMutation，它不会
默认伪造提交成功，也不能连接真实业务库。`startOfflineAction` 支持挂起动作取消/超时、确认拒绝和未知
提交结果；离线 helper 不替代真实 Host 的 task registry。

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


## Plugin API v2 受控读取

新插件声明 `compatibility.pluginApi: "2.x"`，同一 data grant 的 operations 同时声明 `read`、`query`。旧版 `1.x` 的 `data.read` 继续使用物理字段、空 filter 和原有分页契约；非空 filter 仍明确拒绝。旧 Host 会拒绝 v2 包，不能静默降级。

先调用 `data.describe({ accepts: ["vibetable.plugin-data.v2"] })` 查看授权表，再传 `collection` 查看授权字段。查询使用返回的稳定 `fieldId`。元数据包含 logicalType、resultType、resultElementType、readonly、nullable、display、options、constraints、filterOperators、sortable；不返回公式源码、lookup 路径或 relation 目标字段。

```ts
const description = await capabilities.data.describe({
  accepts: ["vibetable.plugin-data.v2"], collection,
});
const request = { contract: "vibetable.plugin-query.v2" as const, collection,
  fields: ["id", fieldId], filters: [{ field: fieldId, operator: "gte" as const, value: 0 }],
  sorts: [{ field: fieldId, direction: "asc" as const }], pageSize: 200 };
let cursor: string | undefined;
do {
  const page = await capabilities.data.query(cursor ? { ...request, cursor } : request);
  // items 保留原始 ID、0、false、null；不要用 truthy 判断缺值。
  consume(page.items);
  cursor = page.nextCursor ?? undefined;
} while (cursor);
```

每个投影、筛选、排序字段都需授权；`ids` 需授权 `id`，最多 200 个。单页 1–200 条，默认 100；未知参数、raw SQL、关系路径、keyword/RPC 均不接受。筛选直接使用 Go QueryPort 的 operators：text 的 eq/ne/in/contains/starts_with/ends_with；number/date 的 eq/ne/in/gt/lt/gte/lte/between；bool/relation 的 eq/ne/in；select 的 eq/ne/in、multiSelect 的 contains；JSON 的 contains。普通字段均支持 is_null/is_not_null。实际字段能力以元数据为准。首版 text/number/bool/date 可稳定排序；select/relation/JSON/computed 不支持排序。

Formula/lookup 首版可投影、不可筛选和排序，结果为 `{state,value,fresh}`。非 fresh 时 value 为 null，page.complete 为 false；不会返回旧计算值和内部 diagnostic。关联字段返回原始关联 ID（多关联返回 ID 数组），不展开未授权目标表的标签。

游标是执行内随机、单次句柄；继续页必须保留相同 query。另一次 run、查询变化、workspace/session、安装修订或 schema/data/computed dependency 变化会失败，调用方应开始新查询。沿用执行超时、取消、64 次能力调用和 1 MiB 消息预算，不保证无限大表能在一次执行中完成。

Go 在同一读取事务中读取 pluginstore 的 installation/status/grants/revision 并执行查询；BFF 只传 Host 可信 projectKey/sessionEpoch，插件不能传 scope。闭合 REST `/api/vibetable/v2/plugins/data` 只由 Host 的已鉴权会话使用，没有 renderer RPC 注册。

离线测试显式配置 `pluginApi: "2.x"`、Go 元数据 fixture 和 `queryAdapter`。adapter 接收闭合请求、返回 synthetic 权威分页结果；offlineHost 不实现另一套筛选引擎。缺少 adapter 明确失败。`data-overview` 示例展示元数据、筛选、稳定排序、全量分页、完整性与进度。


v2 错误包括 `plugin_api_unsupported`（未协商/旧插件）、`plugin_read_denied`（授权不足/安装禁用）、`plugin_capability_invalid`（未知键/请求形态）、`plugin_query_limit`（页或ID上限）、`query.operator.unsupported`、`query.cursor.unsupported_sort`、`plugin_query_computed_unsupported`、`plugin_cursor_invalid`（句柄不存在/已消费/请求改变）、`plugin_cursor_stale`（安装或computed依赖修订改变）和 Go 的 `query.cursor_stale`（schema/data/clock变化）。执行取消、超时、总调用数和消息上限继续使用 v1 的 `plugin_cancel_requested` / `plugin_timeout` / `plugin_worker_failed`，不得把失败结果解释为成功全量。

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
