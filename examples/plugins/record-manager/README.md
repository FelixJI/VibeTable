# 记录管理示例应用（无 Worker）

最小完整的插件业务应用：单一 `application` 入口内含 **概览 / 列表 / 详情表单** 三个 hash 页面，外加一个不依赖任何界面组件的 `headless` 动作。业务持久化只经统一 surface API（`data.describe/query/subscribe`、`mutation.preview/submit`、`data.export`、`storage`），无可信宿主 import、无 Worker 回退。

- 概览：授权表/字段/关系展示、宿主权威 `totalRows` 计数（探针在 `finally` 释放继续页句柄）（不把分页累计当全量）、失效订阅与主题刷新、CSV 导出。
- 列表：游标分页（50/页），前进消费现有游标只读一页，回退释放后重查至目标页，刷新/取消/关闭清理未消费句柄；正确显示 `0/false/null/空串` 与未就绪计算值；末页、空表、数据变化（订阅失效后重查）与查询取消。
- 详情/表单：创建、更新、删除（Host 破坏性确认）、预览后批量规范化文本（带行保护、显式确认，最多 1000 条变更，超限明确拒绝且不自动分批，unknown 不重放）。

```powershell
Set-Location examples/plugins/record-manager
npm ci
npm run typecheck
npm test
Set-Location ../../..
uv run --frozen --no-sync python scripts/vibetable_plugin.py build examples/plugins/record-manager
uv run --frozen --no-sync python scripts/vibetable_plugin.py validate examples/plugins/record-manager
uv run --frozen --no-sync python scripts/vibetable_plugin.py inspect-permissions examples/plugins/record-manager
uv run --frozen --no-sync python scripts/vibetable_plugin.py pack examples/plugins/record-manager --output build/plugins/record-manager.vtplugin
```

SDK 未发布到 npm：构建通过仓库固定 commit 的 `sdk/plugin/src`（CLI esbuild 别名），不引用任何未发布 npm 版本。离线测试用同一锁定 esbuild 打包真实 SDK 源码与 `src/records.ts`，离线通道由 SDK 的 `createOfflineSurfaceHost` 消费真实 Go 独立语料，共享终态语料同时由真实 Host .NET 测试验证；显式合成 authority ports 不连接业务数据库。

保存时仅在 Host 的 submit/确认阶段启用取消按钮，预览期间不能取消；无界面快照先释放计数探针游标，清理失败不会落盘新快照。
保存使用受控按钮及输入框 Enter 事件，不触发原生表单导航；隔离 iframe 保持不授予 `allow-forms`。相邻浏览器合同在真实 Edge sandbox 中点击、按 Enter 验证 SDK 调用，仍不替代同包 WPF 产品场景。
