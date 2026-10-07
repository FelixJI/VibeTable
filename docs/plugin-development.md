# VibeTable 插件开发与 GitHub 发布

VibeTable 插件是离线优先的 `.vtplugin` 包。插件 Worker 通过声明式 capability 读取数据，返回
MutationPlan 请求修改；由 Host 展示最终确认后交给 Go 提交。Worker
当前工作区，不能直接访问 PocketBase、SQLite、本机路径或任意网络。宿主在安装前展示 manifest
中的权限、兼容性和动作风险，用户批准后才会提交安装事务。

## 1. 权威工具与版本边界

- Manifest schema：`vibetable.plugin-manifest.v1`
- Plugin result：`vibetable.plugin-result.v1`
- TypeScript SDK：`sdk/plugin/`
- 开发与打包 CLI：`scripts/vibetable_plugin.py`
- 包解析与完整性契约：`backend/infrastructure/plugin_package.py`
- 可运行示例：`examples/plugins/data-overview/`、`examples/plugins/normalize-text/`

当前 1.x SDK 标记为 `private`，CLI 也属于 VibeTable 主仓，尚未发布到 npm 或独立工具仓。
外部插件仓库不得声明一个并不存在的 npm 版本；应在 CI 中 checkout 明确的 VibeTable commit，
并用该 commit 的 SDK/CLI 完成最终 validate/build/pack。固定 commit 需要由插件维护者显式更新，
这样一次 Release 的包契约不会随 `main` 漂移。

Host 产品版本取唯一版本源 `backend/_version.py`；SDK 的 `1.0.0` 和 manifest 的
`pluginApi: "1.x"` 是不同的版本边界。示例当前 `minHostVersion: "0.5.1"`，不要求不存在的
Host 1.0。该最低版本号不承诺已发布的 0.5.1 包实现本分支新增合同；新增 rowGuards /
expectedDigest 必须使用同一固定 commit 的 SDK、CLI 和正式构建的 Host 配套验证。本 Task
不升级产品版本、不发版。使用仓库锁定的 Python、Node/npm；Python 环境通过 `uv sync --frozen --group dev
--group build` 恢复，插件不会启动额外的数据库权威。

## 2. 推荐目录

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
runtime import 和 Worker 网络全局。`pack` 会生成确定性 ZIP 和包内 `integrity.json`。不要手改
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
夹在 1–200；null、布尔和非整数拒绝。nextCursor 非空时继续读取，满页后可能还需读取一个
空页才能结束。cursor 是 offset 字符串；宿主兼容既有 Python 整数形式（如 `"1_0"`），插件
应直接转发收到的 nextCursor，不自行拼装。

单次 Worker 默认预算为 15 秒、最多 64 次 capability 调用、单条 JSON 消息最大 1 MiB；
宿主最多并行两个 Worker。分页、文件和进度调用都消耗该预算；这些限制没有因示例而放宽。

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
Host 对任务的取消管理能力。

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

完整分页与选择处理见 `examples/plugins/normalize-text/src/normalize.ts`；真实计数见
`examples/plugins/data-overview/src/overview.ts`。PluginResult 的最小形状是
`{contract:"vibetable.plugin-result.v1",status:"success"|"warning"|"error",summary:string}`，
可加 metrics、table、refresh、warnings、artifacts。`ok(data)` 把原始 JSON 放在 table.data，
`failure(code,message)` 把错误码放在 table.code；这些是结果内容，不自动改变 Host task 的
执行终态。MutationPlan 的最小形状是 `{contract:"vibetable.mutation-plan.v1",collection,
operations,preview:{affectedCount}}`。返回额外未声明 wire 字段会被 closed DTO 拒绝。

批准前，拒绝或取消都不提交。批准后的 Go 冲突按原码返回；pending、提交传输失败或不完整
回执会成为现有 `aborted` 终态与 `plugin_commit_unknown`。提交开始后取消不表示回滚；检查
记录与审计再决定后续动作，宿主不会自动重放。已确认成功的任务不会被晚到取消覆盖。

离线 `createOfflineHost` 显式输入合成 collections、fields、permissions、rowGuards；默认不
授权读写。write 测试还必须提供 approveMutation 与合成 applyMutation，它不会默认伪造提交
成功，也不能连接真实业务库。`startOfflineAction` 支持挂起动作取消/超时、确认拒绝和未知
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
