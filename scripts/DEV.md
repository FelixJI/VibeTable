# 一键开发启动器 `scripts/dev.py`

一条命令拉起**整个 VibeTable 栈**用于真实数据测试：本机 Directus 12（SQLite）+ Python 后端 + WPF 客户端。**无需 Docker**，与主环境完全隔离。

```powershell
.\.venv\Scripts\python.exe scripts\dev.py
```

`dev.py` 依次做：

1. **启动本机 Directus 12**（复用 `scripts/local_directus/run.py`）：运行时 `npm install`、生成密钥、端口冲突自动避让、首次 bootstrap 数据库并灌入 VibeTable schema。
2. **构建 WPF host（Release）**：若 `desktop/src/VibeTable.Desktop` 的 `.cs`/`.xaml`/`.csproj` 比已构建的 `VibeTable.Desktop.exe` 新，则自动 `dotnet build --configuration Release`；否则复用现有产物。
3. **用隔离环境启动 WPF 客户端**：注入 `VIBETABLE_DIRECTUS_URL=http://localhost:<port>` 与 `VIBETABLE_DIRECTUS_PROJECT=default`。客户端自行拉起 Python 后端（`.venv\Scripts\python.exe -m backend`），所以前后端+数据层一次性就绪。

`Ctrl+C` 会同时终止 Directus 与客户端进程并释放端口。

## 环境隔离

客户端进程拿到的是**最小、受控的环境**：只带系统 PATH 和本次运行专属的 `VIBETABLE_DIRECTUS_*` 变量，**不会**继承或污染全局已有的 `VIBETABLE_DIRECTUS_URL`，因此与主环境不会冲突。

## 常用选项

| 参数 | 说明 |
|------|------|
| `--no-host` | 只起 Directus，不构建/启动客户端（用于纯数据调试） |
| `--no-directus` | 跳过启动 Directus（它已在跑时用），需配合 `--directus-url` 或环境变量 |
| `--directus-url <url>` | 显式指定 Directus 地址，覆盖本机启动 |
| `--host-directus-auto` | **测试生产行为**：dev.py 只构建+启动 host（带 `--directus-auto`），由 host 自己拉起并监督 Directus（复用客户端启动器代码路径） |

## 客户端启动器（`--directus-auto`，C# 内建）

WPF host 自带客户端启动逻辑（`VibeTable.Infrastructure/Directus/`）：开发态加 `--directus-auto` 启动时，
host 自己依次：① 检测 Node.js 24.x（缺失则弹窗引导到 nodejs.org）② 解析本地 Directus 目录
（打包 `<baseDir>/local-directus/` 或 dev `scripts/local_directus/`）③ 用 `DirectusSupervisor`
拉起 `run.py` → 轮询 `/server/ping` 就绪 ④ 把算出的 `VIBETABLE_DIRECTUS_URL` 注入后端环境 ⑤ 再起后端。

单机发布包在没有外部 `VIBETABLE_DIRECTUS_URL` 时会自动启用该模式，无需快捷方式附加参数。发布态
复用 `backend/vibetable-backend.exe --local-directus-runner` 执行随包的 runner，不依赖系统 Python；
host 自管 Directus 生命周期，JobObject 保证不残留进程。
开发态用 `dev.py --host-directus-auto` 走同一条代码路径验证。

| 相关文件 | 作用 |
|---|---|
| `desktop/src/VibeTable.Infrastructure/Directus/DirectusSupervisor.cs` | 进程监督（轮询 ping、JobObject 兜底、stderr 捕获） |
| `desktop/src/VibeTable.Infrastructure/Directus/DirectusLaunchOptions.cs` | 解析本地 Directus 目录 + dev Python/发布 backend runner |
| `desktop/src/VibeTable.Infrastructure/Directus/NodeRuntime.cs` | 检测 Node.js ≥ 24 |
| `desktop/src/VibeTable.Infrastructure/LaunchPaths.cs` | 共享的 repo-root / 路径解析 |
| `desktop/src/VibeTable.Desktop/Services/HostStartupOptions.cs` | `--directus-auto` 标志 |
| `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs` | `EnsureLocalDirectusAsync()` 启动序注入 |
| `--directus-url <url>` | 显式指定 Directus 地址，覆盖本机启动 |

## 登录

客户端首次启动本机实例时会提示设置邮箱，可选择自定密码或“无密码登录”。无密码模式由 VibeTable
生成随机强密码并托管，账号本身并非空密码。保存的密码进入 Windows 凭据管理器；refresh token
由 Python broker 放入 DPAPI 受保护存储。只有勾选“自动登录”才会在后续启动自动 refresh/回退登录。
开发者绕过客户端直接运行 `run.py` 时，仍可从 gitignored `.env` 使用随机管理员口令。

## 前置

- Node.js 24.x（`.nvmrc`）、.NET 10 SDK、仓库 `.venv`（`uv sync --group dev`）。
- bulk-mutation 扩展需已构建（`directus/extensions/vibetable-bulk-mutation` 下 `npm ci && npm run build`）。
- WebView2 Evergreen Runtime（WPF 客户端依赖）。

## 故障排查

- **isolated-vm 编译失败 / 找不到 Visual Studio**：`package.json` 已用 `overrides` 锁 `isolated-vm@6.1.2`（自带 win32-x64 预编译二进制，ABI137=Node24），**终端用户无需装 VS**。开发机若仍要本地编译，`run.py` 会自动确保 app-private 的 `node-gyp@latest`（识别 VS 2026）。
- **端口 8055 被占**：自动避让到 8056+；实际端口会写回 `scripts/local_directus/.env` 并打印。
- **`/vibetable-bulk-mutation/apply` 返回 404（但日志显示 "Loaded extensions"）**：检查 `.env` 是否误设了 `EXTENSIONS_PATH` 为绝对/POSIX 路径——Directus 12 对该值解析敏感。**不要设 `EXTENSIONS_PATH`**，让它默认用相对的 `./extensions`。
- **npm install 报 `ENOTEMPTY: rmdir ...`**：Windows + Defender/OneDrive 下的并发文件锁偶发问题。重跑即可（cache 已就绪）。
- **想完全重置 Directus 数据**：删除 `scripts/local_directus/{data,uploads,.env,.schema-applied,.bootstrapped}` 后重跑。

## 分发（客户端安装包）

VibeTable 单机安装包不预置 `node_modules`（省约 600MB），但包含 runner、schema/capability 和扩展。
客户端首次启动时由发布 backend runner 联网拉取 Directus 12，**全部写入
`%LOCALAPPDATA%\VibeTable\directus`**
（`.npm-cache`/`.npm-prefix`/`node_modules` 都在 `scripts/local_directus/` 内），不污染用户
全局 Node/npm/PATH。详见 `scripts/local_directus/README.md` 的「分发形态」一节。
