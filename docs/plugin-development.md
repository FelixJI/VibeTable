# VibeTable 插件开发与 GitHub 发布

VibeTable 插件是离线优先的 `.vtplugin` 包。插件 Worker 通过声明式 capability 读取或修改
当前工作区，不能直接访问 PocketBase、SQLite、本机路径或任意网络。宿主在安装前展示 manifest
中的权限、兼容性和动作风险，用户批准后才会提交安装事务。

## 1. 权威工具与版本边界

- Manifest schema：`vibetable.plugin-manifest.v1`
- Plugin result：`vibetable.plugin-result.v1`
- TypeScript SDK：`sdk/plugin/`
- 开发与打包 CLI：`scripts/vibetable_plugin.py`
- 包解析与完整性契约：`backend/infrastructure/plugin_package.py`
- 可运行示例：`examples/plugins/data-overview/`、`examples/plugins/normalize-text/`

当前 1.x SDK 标记为 `private`，CLI 也属于 VibeTable 主仓，尚未发布到 npm 或独立工具仓。
外部插件仓库不得声明一个并不存在的 npm 版本；应在 CI 中 checkout 明确的 VibeTable commit，
并用该 commit 的 SDK/CLI 完成最终 validate/build/pack。固定 commit 需要由插件维护者显式更新，
这样一次 Release 的包契约不会随 `main` 漂移。

## 2. 推荐目录

```text
manifest.json
package.json
package-lock.json
tsconfig.json
src/
  <worker>.ts
views/
  <view>/index.{html,css,js}
schemas/
  action-input.v1.json
  action-output.v1.json
tests/
  *.test.mjs
.github/workflows/ci.yml
.gitignore
LICENSE
README.md
```

源码仓库应提交源文件、schema、测试、lock 和必要的静态视图源码。`dist/`、`tmp/`、
`release/`、ZIP 与 `.vtplugin` 是生成物，不应作为源码重复提交；正式 `.vtplugin` 只作为
GitHub Release 资产发布。

## 3. 主仓内开发

从锁文件恢复依赖，不使用 `npm install` 改写依赖解析结果：

```powershell
Set-Location examples/plugins/data-overview
npm ci
npm run typecheck
npm test

Set-Location ../../..
uv run python scripts/vibetable_plugin.py validate examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py inspect-permissions examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py build examples/plugins/data-overview
uv run python scripts/vibetable_plugin.py pack examples/plugins/data-overview `
  --output build/plugins/data-overview.vtplugin
```

`build` 会用插件本地、锁定的 esbuild 将 Worker 打成自包含 ESM，并拒绝动态 import、未打包
runtime import 和 Worker 网络全局。`pack` 会生成确定性 ZIP 和包内 `integrity.json`。不要手改
`dist` 或 `integrity.json` 后跳过 build/pack。

## 4. Manifest 与最小权限

Manifest 只声明实现实际消费的 capability：

- `permissions.data`：按 collection、operation 和 field 限定数据访问；
- `permissions.files`：只有调用原生文件选择能力时才声明；
- `permissions.privateStorage`：只有使用插件私有设置时才声明；
- `permissions.network`：Worker 当前不能直接使用浏览器网络全局；不要用空 domain 或无消费者的
  method 预留未来权限；
- `actions[].risk`：必须与可观察行为一致，不能把 write/destructive 动作伪装成 read。

Worker 的公共类型应使用 SDK 的 `JsonObject`/`JsonValue`，不要用宽泛 `unknown` 或 `Any`
绕过 capability 契约。测试至少覆盖核心纯函数、空数据、字段映射和 manifest/静态资源存在性；
测试路径必须基于仓库根或 `import.meta.url`，不能硬编码它在 VibeTable monorepo 中的位置。

## 5. GitHub Release 安装契约

VibeTable 插件中心支持输入 `owner/repo` 或标准 `https://github.com/owner/repo` 地址，从公共
GitHub 仓库的 latest 正式 Release 检查插件。首版不携带 GitHub token，因此私有仓库不在
支持范围内。

Release 必须满足：

1. 不是 draft 或 prerelease，并有 tag 与发布时间；
2. 正好包含一个状态为 `uploaded`、扩展名为 `.vtplugin` 的资产；
3. 资产大小为 1 byte 到 64 MiB；
4. GitHub API 为该资产提供合法的 `sha256:` digest；
5. `browser_download_url` 是 `https://github.com/...`；
6. 下载字节数与 Release metadata 一致，SHA-256 与 API digest 一致；
7. 下载后仍必须通过 VibeTable 既有包内 manifest、路径和 `integrity.json` 审查。

Release metadata 始终直连 `api.github.com`。`.vtplugin` 资产使用“设置 → 关于 → 软件更新”
中相同的下载通道：GitHub 直连、`ghproxy.net`、`gh-proxy.com` 或自定义 HTTPS 前缀。
第三方代理可能看到 GitHub 下载 URL；无论选择哪个通道，VibeTable 都会用直连 GitHub API
返回的 digest 验证最终字节。

插件中心只把仓库坐标交给原生宿主，不接受任意下载 URL，也不会把本机缓存路径暴露给
WebView。远程包下载后仍使用与本地 `.vtplugin` 相同的“检查计划 → 展示权限 → 用户批准 →
提交安装”事务；取消、替换计划、后端重启或退出会释放尚未提交的远程包缓存。

## 6. 独立插件仓库示例

微信读书可视化插件已迁移到
[FelixJI/VibeTable-WeRead-Notes-Dashboard](https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard)。
它展示了最小权限 manifest、独立 lock、聚焦测试、固定 VibeTable commit 的 CI，以及只把
`.vtplugin` 上传到正式 GitHub Release 的交付方式。
