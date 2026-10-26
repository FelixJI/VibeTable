# 简化 ERP 插件

一个真实的 Plugin API 3.x `application` 示例，提供商品、客户和单商品订单界面。
业务数据只经过 Host → PocketBase 权威接口，所有保存先预览并经宿主原生确认。

## 能做什么

- 商品：新增、编辑商品编码/名称/单价；以元输入、整数分保存，不使用浮点金额累加。
- 客户：新增、编辑名称与邮箱。
- 订单：选择已授权商品和客户，录入订单号与正整数数量；保存单价快照和金额，后续只变更状态。
- 三页切换、当前表搜索、空态、失效提示、CSV 导出、主题跟随、宿主任务回执。
- 表单切换/刷新/返回平台前提示丢弃；宿主拒绝保存时保留输入；结果未知时锁住写入口。

界面遵循[插件 UI 规范 v1](../../../docs/plugin-ui.md)：初始及动态主题、紧凑/舒适密度、统一
宿主 token、可换行工具栏与自适应表单、字段级错误及首错聚焦、Escape 保留草稿和恢复焦点。
状态用明确文字区分加载、等待确认、拒绝、成功与错误；未知结果独立警示，刷新不解除写入锁定。
普通状态和错误分别使用 status/alert 通道，数据区声明忙碌状态。界面文字当前为简体中文；
宿主 locale 不会自动翻译文案。亮/暗和高对比样式不等于已经完成真实 WebView2 可访问性认证。

不包含库存扣减、税费、多币种、多明细、采购、财务记账或跨表事务。状态是普通业务文本，
不是审批流。订单单价取当前页面已读取的商品快照；各表不在同一事务读取，不承诺下单时价格锁定。
每张表最多 1000 条，超过上限明确失败，不能把截断列表作为全量。搜索只影响显示，导出是当前表
全量原值（金额单位“分”、关系为 ID），不受搜索框影响。

## 1. 在专用演示工作区准备表

先用平台“创建表/字段设置”建立以下三表。表名和字段显示名必须精确匹配且唯一；程序随后使用
`describe()` 返回的稳定表/字段 ID 请求数据。表/字段重命名或类型不匹配会阻止加载，不猜测映射。

| 表名 | 字段显示名 | 类型与推荐约束 |
| --- | --- | --- |
| ERP 商品 | 商品编码、名称 | 文本，必填；商品编码设置唯一 |
| ERP 商品 | 单价分 | 数字，必填、非负整数 |
| ERP 客户 | 名称、邮箱 | 文本，必填 |
| ERP 订单 | 订单号 | 文本，必填、唯一 |
| ERP 订单 | 客户 | 单关联 → ERP 客户，必填 |
| ERP 订单 | 商品 | 单关联 → ERP 商品，必填 |
| ERP 订单 | 数量 | 数字，必填、正整数 |
| ERP 订单 | 单价分、金额分 | 数字，必填、非负整数 |
| ERP 订单 | 状态 | 文本，必填；示例使用草稿/已确认/已取消 |

`id` 是系统记录 ID，不需手工添加。商品单价可为 0。金额和数量还会在示例中校验安全整数范围。
平台直接修改数据也需受字段约束保护；示例代码不能替代数据库约束。

`manifest.json` 的 `erp_products`、`erp_customers`、`erp_orders` 是**待配置的稳定表 ID**，不是
通配符或按名称查找的规则。打包前把它们替换为上述三张表的真实 canonical collection ID
（平台左侧表列表在显示名下方显示 `collection` 小字；也可从表元数据读取）；显示名和表 ID 不能混用。
插件没有自行建表、枚举未授权表或动态扩大权限的接口。安装计划中核对三表授权后再批准。
示例为方便教学读取所选三表全部字段；用于真实工作区前应把 manifest 的 `fields:["*"]`
收紧到 `id` 和上表所需的真实字段 ID。

## 2. 构建、测试、安装

在包含此示例的 VibeTable checkout 中，按根 README 恢复锁定开发环境，再执行：

```powershell
uv sync --frozen --group dev --group build
Push-Location examples/plugins/simple-erp
npm ci
npm run typecheck
npm test
npm run build
Pop-Location
uv run python scripts/vibetable_plugin.py validate examples/plugins/simple-erp --json
uv run python scripts/vibetable_plugin.py pack examples/plugins/simple-erp --output dist/simple-erp.vtplugin
```

在“插件中心”选择本地包或开发目录，检查权限并安装，然后从应用导航打开“简化 ERP”。
首次先新增商品、客户，再切换订单新增；输入数量 2、单价 12.99 元时，订单金额为 2598 分。
Host 需支持现有 API 3.x；新 `data.pages()` 是随插件打包的 SDK 帮助方法，不新增宿主方法。
manifest 的 `minHostVersion` 不是本轮发版声明。

离线业务测试在本示例 `tests/erp.test.mjs`；实际 HTML/JS 的 DOM 交互测试在
`desktop/web-grid/src/components/plugins/simpleErpSurface.test.ts`。它们不冒充真实 WebView2/Go 集成测试。
仓库 `node-tests` 入口已纳入此示例的类型检查、测试和构建。

## 3. 源码与 API 对照

| 文件 / 函数 | 阅读重点 |
| --- | --- |
| `src/erp.ts` → `loadTable` | describe 授权目录、唯一字段绑定、`data.pages` 读全分页并检查完整性 |
| `parseMoney` / `orderTotal` | 元字符串到整数分、数量和溢出校验 |
| `orderValues` | 真实关联 ID、商品价格快照、订单金额 |
| `mutationPlan` | create 或携带 rowGuard 的 update，单表事务 |
| `src/app.ts` | 三页交互、草稿离开提示、订阅失效、原生确认、导出和主题 |
| `ui/` | 无外部资源的 HTML/CSS；使用纯文本节点显示业务数据 |

[API 参考](../../../docs/plugin-api.md)提供完整方法表、参数和错误处理示例。
示例不自动删除记录，不持久保存表单；关闭整个插件或工作区会丢失未保存输入。
`plugin_commit_unknown` 后请在平台表格和任务记录核对结果，再重新打开插件；不要直接重复新增订单。
