# 文本规范化示例

使用同一固定 VibeTable commit 的 SDK、CLI 和构建后的 Host；当前已发布版本号不能代替
新增 rowGuards / expectedDigest 合同的配套构建证据。

在表格选择记录，指定字段的 physicalName 和 trim、collapse-whitespace、lowercase 或
uppercase。Worker 分页读取，保留非字符串与未选择记录，仅为实际改变的文本返回
MutationPlan。预览列出 before/after、准确 affectedCount，以及跳过数。每个更新携带同页
Go 权威 rowGuard；预览后并发变化会原子失败，不覆盖新文本。

`npm ci`、`npm run typecheck`、`npm test` 使用锁定依赖；test 先真实 CLI build，再验证
编译后的 Worker。根目录执行：

```powershell
uv run python scripts/vibetable_plugin.py build examples/plugins/normalize-text
uv run python scripts/vibetable_plugin.py validate examples/plugins/normalize-text
uv run python scripts/vibetable_plugin.py pack examples/plugins/normalize-text --output build/plugins/normalize-text.vtplugin
```

在合成工作区安装后，拒绝或取消最终确认均不写入；批准才经 Host/BFF 提交到 Go。返回冲突
时重新查看记录和预览；`plugin_commit_unknown` 时检查记录与审计，不自动重复提交。
正式同包 E2E `11-plugin-mutation` 覆盖真实安装、拒绝、取消、并发冲突和批准更新。
