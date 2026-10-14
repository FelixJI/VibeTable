# 无 Worker 数据编辑示例

此包使用 `manifest.v2` / `pluginApi: 3.x`。动作仅打开现有隔离页面；业务代码通过公开 `createSurfaceClient(window.name)` 访问 Host/Go，不声明 `workerEntry`。

从本仓固定 commit 构建：在本目录运行 `npm ci`、`npm run typecheck`、`npm test`。在仓库根运行：

```powershell
uv run python scripts/vibetable_plugin.py validate examples/plugins/surface-data-editor --json
uv run python scripts/vibetable_plugin.py inspect-permissions examples/plugins/surface-data-editor --json
uv run python scripts/vibetable_plugin.py pack examples/plugins/surface-data-editor --output build/surface-data-editor.vtplugin
```

在合成工作区选中含至少一条记录和可写文本字段的表，安装并启用该包，然后运行“打开无 Worker 编辑器”。页面按 200 行分页读取全部记录，在草稿中编辑第一条；保存发出预览句柄，由 Host 展示精确计划并一次确认。拒绝和提交前取消不写入，行冲突或未知提交错误需人工核对，不能自动重试。

完整同包场景 `47-plugin-surface-api` 安装此包、读取 550 条记录并验证拒绝/取消/成功；该测试 Host 子进程显式禁用旧 Node 插件执行器，并用旧包 `describeAction` 的 `plugin_worker_unavailable` 回执核实开关生效。
