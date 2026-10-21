# 文本规范化示例（无界面动作 / pluginApi 3.x）

已从旧 Worker 链迁移为 `headless` 动作：包声明 `vibetable.plugin-manifest.v2` 与
`compatibility.pluginApi: "3.x"`，入口是宿主内置受控文档加载的 `ui/normalize.js`
（由 CLI 从 `src/normalize.ts` 打包生成，不提交）。业务逻辑保持纯函数
（`normalizeText` 只收敛空白），全部数据读写经统一 surface API：

- `context.read` 获取活动表与选中记录；
- `data.describe` 读取授权元数据，按选中 ID 分批（`query.ids`，每批≤200）直达选中记录，
  不分页扫描全表；`scanned` 只计实际返回的选中行；
- 同一记录多个文本字段的变更合并为**一个** update（Go 按顺序 apply：同一行两个携带同一原始
  digest 的 update 会触发 digest_conflict 回滚整批），字段变更数独立统计；更新携带同页
  `rowGuards` 的 opaque `expectedDigest`，缺保护时 fail closed；
- `mutation.preview` → `mutation.submit` 走宿主原生确认与单次原子提交，插件不能自带批准；
- 拒绝、取消零写入；`plugin_commit_unknown` 结果未知，不自动重放。

没有旧 `workerEntry`、form/input/output schema 或表单输入：规范化策略固定为
“trim + 收敛连续空白”，作用于选中记录全部授权可写文本字段。

`npm ci`、`npm run typecheck`、`npm test` 使用锁定依赖；`npm test` 先经真实 CLI
build 生成 `ui/normalize.js`，再用合成 surface 宿主（真实 Go 语料口径）验证按 ids 分批、
同记录合并、跳过、拒绝与缺失选择失败。根目录执行：

```powershell
uv run --frozen --no-sync python scripts/vibetable_plugin.py build examples/plugins/normalize-text
uv run --frozen --no-sync python scripts/vibetable_plugin.py validate examples/plugins/normalize-text
uv run --frozen --no-sync python scripts/vibetable_plugin.py pack examples/plugins/normalize-text --output build/plugins/normalize-text.vtplugin
```
