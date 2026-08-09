# Node 开发工具链

Node.js 只用于 Web、插件 SDK/示例和本地插件开发工具；正式 VibeTable 离线发布包禁止携带
Node、npm 或 `node_modules`。

## 锁定来源

- 版本来源：仓库根目录 `.node-version` 与 `.nvmrc`，当前均为 `24.18.0`。
- 官方制品：`https://nodejs.org/dist/v24.18.0/node-v24.18.0-win-x64.zip`。
- 官方 SHA-256：`0ae68406b42d7725661da979b1403ec9926da205c6770827f33aac9d8f26e821`。
- 恢复位置：`.tools/node/node-v24.18.0-win-x64/`；下载缓存位于
  `build/tooling/node-v24.18.0-win-x64.zip`。两者均为仓库声明的本地生成目录，不提交到 Git。

`scripts/node_toolchain.py` 在外部制品导入时验证一次官方 SHA-256，并拒绝 ZIP 危险路径；提交后
依赖 Git、锁文件和既有 package contract，不维护逐文件 hash 清单。

## 使用

```powershell
uv run python scripts/automation_project.py bootstrap
```

bootstrap 会恢复锁定 Node，并用该目录的 npm 执行所有 `npm ci`。`scripts/vibetable_plugin.py`
优先复用这个已恢复的 Node；若尚未 bootstrap，才回退到系统 PATH 中显式安装的 Node。CLI 不会在
执行插件命令时静默联网下载工具链。

原 `runtime/node` portable tree 已删除：它只有插件开发 CLI fallback consumer，不是产品运行时；
恢复旧树只需 revert 对应 Git 变更，不影响 workspace 数据或发布包语义。
