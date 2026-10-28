# Plugin API 3.x 参考

这是面向插件作者的 TypeScript SDK 参考。安装、权限和打包步骤见
[插件开发指南](plugin-development.md)，完整业务例子见 [简化 ERP](../examples/plugins/simple-erp/README.md)。
权威实现为 `sdk/plugin/src/surface.ts`、`surface-ui.ts`、`data.ts` 和
`contracts/plugin-surface-api.v1.json`。SDK 是主仓内的 private 包，尚未发布到 npm；请固定
包含所用能力的 VibeTable commit，使用同一 checkout 的 CLI、SDK 和 Host。

页面绘制遵循[插件 UI 规范](plugin-ui.md)。SDK 提供展示快照及消息通道，插件自行实现布局、
控件、校验、焦点与状态展示；没有自动渲染组件库。

## 初始化与生命周期

```ts
import { createSurfaceClient, type SurfacePresentation } from "@vibetable/plugin-sdk";
const api = createSurfaceClient(window.name);
const context = await api.context.read();
function applyTheme(theme: SurfacePresentation) {
  document.documentElement.style.colorScheme = theme.mode;
  document.documentElement.dataset.theme = theme.mode;
  document.documentElement.dataset.density = theme.density;
  const style = document.documentElement.style;
  for (const key of Array.from({ length: style.length }, (_, i) => style.item(i))) {
    if (key.startsWith("--vt-plugin-")) style.removeProperty(key);
  }
  for (const [name, value] of Object.entries(theme.variables)) {
    style.setProperty(name, value);
  }
}
let receivedTheme = false;
const stopTheme = api.context.onChanged(theme => { receivedTheme = true; applyTheme(theme); });
const initial = await api.context.current();
if (!receivedTheme) applyTheme(initial);
window.addEventListener("pagehide", () => { stopTheme(); api.dispose(); }, { once: true });
```

必须在 Host 创建的隔离 frame 内运行，不能在普通浏览器直接连 PocketBase。`window.name` 是
Host 提供的 surface token；不能自己生成或跨页面共享。`context.read()` 返回授权上下文及展示
偏好；`context.current()` / `onChanged()` 只返回或监听主题、语言、密度等展示信息，不授予数据权限。

## 方法速查

| SDK 方法 | 返回值 / 用途 | 权限及注意事项 |
| --- | --- | --- |
| `context.read()` | `CommandContext` | Host 绑定的工作区、活动表、选择上下文 |
| `data.describe({accepts, collection?})` | `DataCatalog \| DataDescription` | 未给表时返回授权目录；给表时返回授权字段 |
| `data.query(request, signal?)` | `DataQueryPage & {rowGuards}` | `read` + `query`；字段、筛选、排序均需授权 |
| `data.pages(request, signal?)` | 异步分页迭代器 | **本次新增 SDK 帮助方法**，不新增 Host 协议；自动管理继续页游标 |
| `data.releaseCursor(cursor)` | `Promise<void>` | 放弃手动分页时释放句柄 |
| `data.subscribe({collection}, listener, signal?)` | `{subscriptionId}` | 失效通知，不是数据；先订阅再查询 |
| `data.unsubscribe(subscriptionId)` | `Promise<void>` | 释放本页面订阅 |
| `mutation.preview(plan)` | `{planId, affectedCount}` | 单表 create/update/delete 计划；不会写入 |
| `mutation.submit(planId, signal?)` | `{status, taskId}` | 请求原生确认；`status` 为 `succeeded` 或 `rejected` |
| `storage.get/set/delete(key, ...)` | JSON 值 / 完成回执 | `privateStorage`；工作区及插件私有设置，不能代替业务表 |
| `file.pickRead(options?)` | `ReadGrant \| null` | `pickRead`，调用 `read()` 得到字节；取消为 null |
| `file.pickWrite(options)` | `WriteGrant \| null` | `pickWrite`，调用 `write(bytes)`；不返回文件路径 |
| `attachments.list(target)` / `read(id)` | 附件引用 / 字节 | 当前记录、字段授权；opaque 引用不包含本机路径 |
| `attachments.previewChange(request)` | 写入预览或 null | 文件原生选择及行保护；后续调用 `mutation.submit` |
| `data.previewImport(request)` | `SurfaceIoTask<SurfaceImportPreview> \| null` | 原生选择 CSV/XLSX；预览有错误时没有 planId |
| `data.export(request)` | IO 任务或 null | 字段显式列举；格式 csv/xlsx；原生选择保存位置 |
| `tasks.list()` / `onChanged(listener)` | 本页面宿主任务投影 | 不含其它插件任务或批准能力 |
| `tasks.cancel(taskId)` | `{cancelRequested}` | 只表示请求已受理，不是取消终态 |
| `ui.notifications.show(text)` | 完成回执 | 纯文本提示，每条最多 500 字符 |
| `ui.navigation.back()` | `{navigated}` | 返回平台；false 表示离开被阻止 |
| `ui.capabilities()` | 支持及不支持的方法列表 | 同步展示能力查询，不是权限查询 |
| `output.print(signal?)` | `{opened:true}` | 仅当前可见 application；表示已开预览，不表示打印成功 |
| `dispose()` | void | 关闭客户端，释放监听及句柄 |

## 查询与分页

模型配置 `model.configure({mode: "map"})` 由原生 Host 分页读取表摘要，每页最多 32 项，且完整响应
（包括模型声明和继续页信息）按生产 JSON 编码不得超过 1 MiB；单项无法容纳时明确拒绝。选中表后，
才按该表的 `schemaRevision` 分页读取字段摘要。每页保持 1 MiB 消息预算，并重新核对工作区、
安装及所选表结构。此目录不提供给插件 frame；结构变化后须重新配置。实际提交的完成回执经
请求、安装、模型和完成范围校验后保留成功，切换工作区不会把旧任务通知给新工作区。

显式 `permissions.data` 与已完成模型配置的授权，按实际表和所请求操作合并字段；`$active`
解析后与同表授权共同生效。只读字段不会因另一条授权包含写操作而获得写权限。

先协商 `accepts: ["vibetable.plugin-data.v2"]`，用元数据的 `collection` 和 `fieldId` 查询。
显示名不保证唯一，不能直接作为查询标识符。示例 ERP 会检查显示名唯一后映射为稳定 ID。

```ts
const metadata = await api.data.describe({
  accepts: ["vibetable.plugin-data.v2"], collection,
});
if (!("fields" in metadata)) throw new Error("未收到字段元数据");
const query = {
  contract: "vibetable.plugin-query.v2" as const,
  collection, fields: ["id", priceFieldId],
  filters: [{ field: priceFieldId, operator: "gte" as const, value: 0 }],
  pageSize: 200,
};
for await (const page of api.data.pages(query, abortController.signal)) {
  if (!page.complete) throw new Error("计算字段未就绪，不能报告完整结果");
  consume(page.items, page.rowGuards);
}
```

`pages()` 惰性请求每一页，开始迭代时复制查询参数，继续页保持相同条件；正常耗尽、`break`、
消费代码抛错和查询因取消退出时释放剩余游标。清理失败也会抛错，SDK 保留句柄直到显式释放或客户端关闭。
它不聚合、不重试、不保证跨表快照；`complete` 保留 Host 原义，由业务决定是否接受不完整计算值。
手动调用 `iterator.next()` 的代码也必须在放弃时 `await iterator.return()`；推荐 `for await`。

`query()` 单页最多 200 条。`totalRows` 是表总行数，`filteredRows` 是筛选后行数，`items.length`
只是当前页条数。`ids` 最多 200 个；继续页使用 opaque 单次游标，不允许把游标存成持久分页位置。
排序、筛选能力以字段的 `sortable` / `filterOperators` 为准。computed 值包含 `{state,value,fresh}`；
关联值是原始 ID，不自动展开未授权表。

## 保存订单：单表原子写入

以下 `orders`、`productFieldId` 等必须来自真实授权元数据，金额按非负安全整数“分”保存。

```ts
const quantity = 2;
const unitPriceCents = 1299;
const amountCents = quantity * unitPriceCents;
if (!Number.isSafeInteger(amountCents)) throw new Error("金额超出精确范围");
const preview = await api.mutation.preview({
  contract: "vibetable.surface-mutation-plan.v1",
  collection: orders.collection, schemaRevision: orders.schemaRevision,
  operations: [{ kind: "create", values: {
    [productFieldId]: productId, [customerFieldId]: customerId,
    [quantityFieldId]: quantity, [priceFieldId]: unitPriceCents,
    [totalFieldId]: amountCents,
  } }],
});
const outcome = await api.mutation.submit(preview.planId);
if (outcome.status === "rejected") return; // 保留表单，不显示“保存成功”
// 成功后重新查询权威数据，不从本地猜测新记录 ID。
```

更新操作形态为 `{kind:"update", primaryKey, expectedDigest, values}`，删除为
`{kind:"delete", primaryKey, expectedDigest}`。`expectedDigest` 必须取自最近查询的
`page.rowGuards[primaryKey]`，不能本地计算。它是 Go 的既有并发保护契约。计划每次 1–1000 个
操作，覆盖单表；任一冲突整批失败。跨表订单+库存事务、建表、SQL、任意 HTTP/RPC 不在插件 API 中。

`planId` 绑定当前页面和精确计划且一次消费，插件不能传 `approved`。用户拒绝不写入；
`plugin_commit_unknown` 表示未获得可确认结果，可能已经写入，必须核对记录与任务，不能自动重放。

## 导出与任务

```ts
const task = await api.data.export({
  collection, fields: ["id", amountFieldId], format: "csv", suggestedName: "订单.csv",
});
if (task) {
  const result = await task.result(signal, progress => showProgress(progress));
  showMessage(`已导出 ${result.rowsWritten} 条：${result.outputDisplayName}`);
}
```

`task.status()` 读权威进度；`result()` 等待终态；`cancel()` 等待取消回执后继续跟踪终态，
可能最终仍成功。普通文件最多 1 MiB，数据 IO 来源/输出最多 32 MiB。更详细的附件、IO 授权
和预算见[开发指南](plugin-development.md)。

## 错误处理

| 情况 | 处理 |
| --- | --- |
| `plugin_read_denied` / `plugin_write_denied` | 检查 manifest、当前安装授权、动作风险；不能回退内部接口 |
| `plugin_cursor_stale` / `query.cursor_stale` / `plugin_plan_stale` | 丢弃旧查询或计划，刷新后由用户重新操作 |
| `mutation.digest_conflict` | 记录被修改，刷新并保留用户意图，不强制覆盖 |
| `plugin_cancel_requested` | 显示取消；不得据此假设已完成的写入回滚 |
| `plugin_commit_unknown` | 阻止再次提交，核对权威数据和宿主任务 |
| `plugin_timeout` / `plugin_transport_failed` | 明确失败，不把缺失回执当成功，不自动重试写入 |
| `plugin_query_limit` / `plugin_concurrency_limit` | 缩小请求或释放资源；不绕过预算 |

取消信号主要用于正在执行的查询、提交、订阅和 IO 等待；不支持 signal 的方法不要伪造参数。
完整接口类型以 SDK 为准，Host 拒绝未知字段和未授权能力。
