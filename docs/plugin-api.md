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
| `transaction.configure({declaration}, signal?)` | 配置回执或 null | 已安装声明与已确认模型；精确原生确认后登记持久保护规则 |
| `transaction.preview({plan, requestId?, refresh?}, signal?)` | `TransactionStatus` | 保存规范计划并签发业务 ID；显式 refresh 保留原 ID，更新影响边界并重新确认 |
| `transaction.submit({requestId}, signal?)` | `TransactionApplied \| TransactionUnknown \| null` | 精确原生确认后单次原子提交；没有自动重试 |
| `transaction.status({requestId}, signal?)` | `TransactionStatus` | 当前授权下读取 prepared/applied/unknown；不触发提交 |
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
操作，覆盖单表；任一冲突整批失败。跨表业务使用下述 transaction 能力；SQL、任意 HTTP/RPC 不在插件 API 中。

`planId` 绑定当前页面和精确计划且一次消费，插件不能传 `approved`。用户拒绝不写入；
`plugin_commit_unknown` 表示未获得可确认结果，可能已经写入，必须核对记录与任务，不能自动重放。

## 跨表事务与恢复

manifest 的 `permissions.transactions` 声明闭集业务规则，引用已经由 `model.configure` 完成绑定的
logical 实体及字段。`transaction.configure({declaration}, signal?)` 必须传入与已安装 manifest
完全相同的声明；Host 原生窗口展示规则、绑定及保护后果，经确认并验证现有数据后才持久登记。
规则包括 `unique`、`reference`、`conservation`、`balance` 和 `state`；不接收脚本、SQL 或可省略的
调用方条件。登记后普通编辑、导入、撤销和关系联动也受保护，插件停用或卸载不解除保护。

`decimals` 指定 `{entity,field,storage,scale,digits}`。`decimalText` 绑定 Text，值使用规范十进制
字符串；`integerUnits` 绑定整数 Number，值为安全整数最小单位。scale 为 0–18，digits 为
1–38，且 scale 不大于 digits；拒绝超精度、溢出、隐式舍入及 null 代零。普通小数 Number
不能当作可无损恢复的精确金额。

```ts
const prepared = await api.transaction.preview({ plan: {
  contract: "vibetable.transaction-plan.v1", modelId: "orders",
  operations: [
    { entity: "headers", kind: "create", tempId: "head", values: { number: "SYNTHETIC-001" } },
    { entity: "lines", kind: "create", tempId: "line", values: {
      header: { entity: "headers", tempId: "head" }, amount: "0.30",
    } },
  ],
} });
if (prepared.status !== "prepared") return;
// 在提交前保存业务 requestId；重开后用原 ID 查询，不生成替代请求。
await api.storage.set("pending-transaction", prepared.requestId);
const result = await api.transaction.submit({ requestId: prepared.requestId }, signal);
if (result?.status === "applied") consume(result.newIds, result.receipt);
```

完整类型从 SDK 导出的 `TransactionDeclaration`、`TransactionRule`、`TransactionPlan` 和
`TransactionStatus` 读取。新建用 `(entity,tempId)` 引用同一计划里的新行，支持循环关系；已有
关联用记录 ID。update/delete/archive/restore 必须提供 `recordId` 和查询得到的 `expectedDigest`。
整份消息至多 1 MiB、计划至多 1000 操作；派生写、规则读取及结果另有权威总预算，超限整笔拒绝。
不会静默拆成多个单表提交。prepared 返回权威 `effects`（含双向关联派生行）、提交前
`dataRevisions` 和 `previewRevision`；effects 包含实际表/记录/操作、结构修订和已有行的
`expectedDigest`。Host 原生确认显示各表操作数量及完整显式和派生操作明细，插件不能自行批准或
调用 Host-only `transaction.commit`。

`transaction.preview({plan,requestId})` 只核对原计划，规范化输入不同报 `transaction.input_conflict`；
用户显式调用 `transaction.preview({plan,requestId,refresh:true})`，才在原请求仍有效且未成功时
刷新同一计划的影响边界；保留原 ID 与新建 ID 映射，推进 `previewRevision`，之后必须重新原生确认。
Host 将确认的版本传给 commit，Go 在同一业务事务内核对该版本、effects 和 dataRevisions；
变化报 `transaction.plan_stale`，整笔拒绝。SDK 不自动刷新；影响预览不替代提交时的完整余额等规则检查。
旧 ID 缺失或恢复围栏失效返回 `unknown`，不会重新签发。`transaction.status({requestId}, signal?)`
在当前授权下返回 `prepared`、原 `applied`（包含稳定 `newIds` 和逐表逐行回执）或 `unknown`。
关闭再开、丢失 acknowledgement 或收到 `plugin_commit_unknown` 后只查询原 ID；SDK 从不自动
重试 submit，不以 `unknown` 换 key 补交。用户拒绝确认返回 null；迟到取消和后续通知失败不撤回
已确认成功。每次 Host task 独立于持久业务 requestId，任务列表不会把并发操作合并成一个任务。

## 一致数据集与精确汇总

API 3.x 增量能力 `vibetable.plugin-dataset.v1` 提供同一短事务中的 1–3 表只读投影。
需要该能力的 manifest 可声明 `compatibility.dataset: "vibetable.plugin-dataset.v1"`；SDK
`dataset.open` 协商精确 contract，不兼容的 Host 明确报 `plugin_api_unsupported`。Host 产品版本
不用于推断该能力是否存在。数据集不增加权限：复用 `data` 或已确认模型的 `read/query` 授权，
字段使用显式 canonical ID，不把用户后来添加的字段自动并入结果。
数据集要求显式字段授权或受控模型角色；仅有 `*`/`$configured` 通配符的单表授权不能开集，
旧单表查询继续沿用原语义。

```ts
const dataset = await api.dataset.open({
  contract: "vibetable.plugin-dataset.v1",
  sources: [{
    query: { contract: "vibetable.plugin-query.v2", collection, fields: ["id", amountFieldId] },
    groupBy: [],
    sums: [{ field: amountFieldId, storage: "decimalText", scale: 2, digits: 38 }],
  }],
}, signal);
try {
  const total = await dataset.summary(collection, signal);
  const firstPage = await dataset.page(collection, 0, 200, signal);
  // 展示 firstPage 与 total；用户点击保存时仍使用这个 dataset。
  const saved = await dataset.export(collection, "合成明细.csv", signal);
  // null 是原生选址取消；成功回执包含同一 datasetId、versions 和 rowsWritten。
} finally {
  await dataset.release();
}
```

源查询复用既有 filters/sorts/ids，不接受 cursor/pageSize、SQL、join 或新查询 DSL；分组和
求和字段必须显式出现在投影中。关系仅返回原始 ID，目标表及其 ID 也必须获准；本地公式递归
核对依赖字段授权。跨表公式、lookup、关系路径计算及 computed 筛选明确报
`plugin_dataset_computed_unsupported` 或既有 computed unsupported 错误，不回退原始 label。

金额与受保护事务共用整数系数：`decimalText` 保存定标十进制文本，`integerUnits` 保存安全
整数最小单位，scale≤18、digits≤38；汇总始终返回定标字符串。null、0、未知文本、空白未分配和
computed pending 分别计数；有未知、未分配或 pending 时 `value:null/complete:false`，不伪造完整 0。
分组键包含 `state/value`，因此 null 与空白组不同。明细仍保留原值或 computed freshness envelope。
`page.complete`/source `complete` 表示投影中的计算值就绪，是否已读完分页由 `nextOffset:null`
判断；`dataset.summary` 顶层和内层 `summary.complete` 一致，表示计算与精确汇总均已知。
行读取完整但金额含未知文本时，汇总的两个完整性标记均为 false，金额保持 null。

全部源合计最多 2000 行、每源 16 字段/8 汇总/2 分组字段。SQLite 在数据跨驱动前量测投影字节；
完整 Go JSON（范围、版本、明细和分组汇总）还必须≤512 KiB，超限拒绝开集，不返回截断的完整结果。
Host 每页面最多 2 个、全局最多 8 个快照，5 分钟到期；每页最多 200 行。每次 page/summary/export
均由 Go 核验安装、权限闭包及全部来源版本；并发 CRUD、schema、clock 或 computed 变化报
`plugin_dataset_stale`。请明确标记旧展示失效，并由用户显式重开数据集，不拼接新旧页。

CSV 直接消费同一不可变数据集，继续使用 `files:["pickWrite"]` 的原生选择及原子文件替换，保存前
再验当前版本和权限；取消/提交前失败不改原文件。保留原始金额字符串，不把它转为浮点。现有
单表 `data.export` 保持原有 CSV/XLSX 与 DataIO 任务语义。

使用既有 `data.subscribe` 对最多三个来源有界订阅以标记失效；退出时取消订阅并 release。
`dataset.pages(collection)` 是消费型 async generator，在耗尽、break、取消及消费者异常的 finally
中释放整个数据集；需要边分页边保留总览/导出时使用显式 `page` 并在最后 release。`api.dispose`
和 Host 页面离开/重挂载/撤权会回收资源，取消后的晚响应不能重新登记到新页面。

`examples/plugins/transaction-conformance` 与 S57 以同一 SDK/CLI/Host/Go 组合串联公开建模、受保护
三表提交、两表各 201 行、精确汇总和同源原生 CSV；独立 Python CSV/Decimal 读取器逐行对账。
S57 只在受控真实桌面 runner 执行，本地合同测试不替代真实桌面结果。

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
