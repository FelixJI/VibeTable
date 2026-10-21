# 数据概览示例（无 Worker surface 视图 / pluginApi 3.x）

已从旧 Worker 链迁移为统一 surface API 的 `customView` 页面：包声明
`vibetable.plugin-manifest.v2` 与 `compatibility.pluginApi: "3.x"`。`ui/index.html`
是宿主隔离 frame 加载的入口文档，`dist/surface.js` 由 CLI 从 `src/surface.ts`
打包生成（`dist/` 不提交）；页面内提供字段选择与“包含”筛选，替代旧
`inputSchema` 表单输入。

- `context.read` 获取活动表；`data.describe` 读取授权字段元数据；
- 文本筛选（contains）只对 `filterOperators` 声明该运算符的字段生成；数值/日期等字段
  禁用筛选输入并保持无过滤统计，不发送不支持的运算符；
- `data.query` 按 200 条分页跟随 `nextCursor` 直到为空，汇总权威
  `totalRows`/`filteredRows`、非空计数与 computed `complete`，提前失败时显式
  `releaseCursor`；
- 计数与权威 `filteredRows` 不一致时 fail closed，不把截断结果当全量；
- 页面主题/密度经可信父页展示快照同步，不新增任何权限。

`npm ci`、`npm run typecheck`、`npm test` 使用锁定依赖；`npm test` 先经真实 CLI
build，再用合成 surface 宿主验证 0/550/6200 行分页、筛选、未授权字段拒绝与
不完整计算。根目录执行：

```powershell
uv run --frozen --no-sync python scripts/vibetable_plugin.py build examples/plugins/data-overview
uv run --frozen --no-sync python scripts/vibetable_plugin.py validate examples/plugins/data-overview
uv run --frozen --no-sync python scripts/vibetable_plugin.py pack examples/plugins/data-overview --output build/plugins/data-overview.vtplugin
```
