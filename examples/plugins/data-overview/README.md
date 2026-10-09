# 数据概览示例

使用同一固定 VibeTable commit 的 SDK、CLI 和构建后的 Host。`minHostVersion` 只用于产品
兼容范围判断，不保证已发布同版本 Host 实现当前 commit 的新合同。

从 `context.collection` 读取当前表，显式声明 Plugin API 2.x 与 read/query，授权当前配置字段。
Worker 用 data.describe 选择可排序字段，通过 Go 查询逐页读取完整结果；支持 fieldId/contains 输入。
输出 count/filteredRows/totalRows/nonNullCount/complete 与最多5条样本，清楚区分筛选范围和全表。
游标或修订失效即明确失败；不拿首屏冒充全量，也不申请写、文件或网络权限。
单页200、能力调用最多64；6200条在预算内，12400条会明确拒绝而不返回成功全量统计。

在仓库锁定工具链中执行 `npm ci`、`npm run typecheck`、`npm test`。test 先通过实际 CLI
编译 Worker，再运行分页测试。根目录执行：

```powershell
uv run python scripts/vibetable_plugin.py build examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py validate examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py pack examples/plugins/data-overview --output build/plugins/data-overview.vtplugin
```

在合成工作区安装该包并运行“打开数据概览”，Host 返回当前表计数。正式同包 E2E 的
`11-plugin-mutation` 场景验证真实包安装与计数；离线测试不等同于 Host 验收。
