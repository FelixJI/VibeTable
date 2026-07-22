# 一键开发启动器 `scripts/dev.py`

一条命令拉起**整个 VibeTable 栈**：WPF 客户端自动拉起本机 Directus 12（SQLite）+ Python 后端。
**无需 Docker**，与主环境完全隔离。

```powershell
.\.venv\Scripts\python.exe scripts\dev.py
```

只验证完整开发构建链路而不启动 WPF、Directus 或 Python 后端：

```powershell
.\.venv\Scripts\python.exe scripts\dev.py --build-only
```

该模式由跨栈质量门禁作为 `dev-build` 阶段调用，用来防止源码布局、扩展清单、lockfile 或宿主产物路径变化后，单独构建仍通过但开发启动器已经失配。

`dev.py` 负责宿主自己做不到的构建准备与启动：

1. **构建 Web 与扩展**：Web Grid 或 manifest 中任一 Directus 扩展缺少产物/源码更新时，自动执行依赖安装与构建。
2. **增量构建 WPF host（Release）**：每次调用 `dotnet build --configuration Release`，由 MSBuild 根据完整项目图、共享 props 与生成目标可靠判断增量工作。
3. **用隔离环境启动 WPF 客户端**：客户端裸跑（不带参数）即自动拉起 Directus + Python 后端。

> `run.py` / `install.py` / 打包 runner 已移除：Directus 的安装、配置、bootstrap、启动现在
> 全部由 C# 宿主负责（见下文「宿主内建的 Directus 启动」）。`dev.py` 不再自管 Directus。

`Ctrl+C` 会终止客户端进程；宿主通过 JobObject 保证 Directus / 后端子进程一并清理、释放端口。

## 环境隔离

客户端进程拿到的是**最小、受控的环境**：只带系统 PATH 和运行所需变量，**不会**继承或污染
全局已有的 `VIBETABLE_DIRECTUS_URL`，因此与主环境不会冲突。

## 常用选项

| 参数 | 说明 |
|------|------|
| `--directus-url <url>` | 显式指定外部 Directus 地址，宿主不再起本机 Directus |
| `--no-directus-auto` | 只启动 WPF 宿主，不让它自动起本机 Directus（配合外部 Directus 或调试 UI） |
| `--build-only` | 构建 Web、全部 Directus 扩展和 WPF host 后退出，不启动任何进程 |

## 宿主内建的 Directus 启动（C#，`VibeTable.Infrastructure/Directus/`）

裸跑 WPF 宿主（不带参数、无 `VIBETABLE_DIRECTUS_URL`）时，宿主自己拉起本机 Directus：

1. **Node 解析**：`NodeRuntime` 优先用捆绑 Node（`runtime/node/node.exe`），PATH 兜底，校验 ≥24。
2. **包管理**：`DirectusPackageManager` 用捆绑 Node 跑 `npm ci`（隔离 cache/prefix），
   三层完整性校验（结构 + `isolated-vm` 原生加载 + lockfile 哈希），`.install-verified` marker
   缓存（7 天周期重校验），校验失败自愈重装。
3. **配置**：`DirectusEnvMaterializer` 从 `.env.template` 生成 `.env`（随机密钥、端口避让）。
4. **bootstrap**：`DirectusSchemaBootstrapper` 跑 `directus bootstrap` 建 DB + 管理员，
   再通过 REST 灌入 VibeTable schema（幂等）。
5. **启动**：`DirectusSupervisor` 直接 `node <directus-cli> start`，轮询 `/server/ping` 就绪，
   把 `VIBETABLE_DIRECTUS_URL` 注入后端环境，再起 Python 后端。

单机发布包在没有外部 `VIBETABLE_DIRECTUS_URL` 时自动启用该模式，无需快捷方式附加参数。

| 相关文件 | 作用 |
|---|---|
| `DirectusSupervisor.cs` | 进程监督（轮询 ping、JobObject 兜底、stderr 捕获） |
| `DirectusPackageManager.cs` | npm ci + 完整性校验 + marker 缓存 + 自愈 |
| `DirectusEnvMaterializer.cs` | .env 生成 + 端口避让 |
| `DirectusSchemaBootstrapper.cs` | bootstrap DB + seed schema（payload 构造） |
| `DirectusLaunchOptions.cs` | 解析本地 Directus 目录布局 |
| `NodeRuntime.cs` | 检测 Node.js ≥ 24（捆绑优先） |
| `LaunchPaths.cs` | 共享的 repo-root / 路径解析 |
| `HostStartupOptions.cs` | `--directus-auto` / `--no-directus-auto` 标志 |
| `MainWindow.xaml.cs` | `EnsureLocalDirectusAsync()` 启动序注入 |

## 登录

客户端首次启动本机实例时会提示设置邮箱，可选择自定密码或“无密码登录”。无密码模式由 VibeTable
生成随机强密码并托管，账号本身并非空密码。保存的密码进入 Windows 凭据管理器；refresh token
由 Python broker 放入 DPAPI 受保护存储。只有勾选“自动登录”才会在后续启动自动 refresh/回退登录。

## 前置

- Node.js 24.x 已捆绑在 `runtime/node/`（无需系统安装；开发期 PATH 上有也可作兜底）。
- .NET 10 SDK、仓库 `.venv`（`uv sync --group dev`）。
- Web 与 Directus 扩展由 `dev.py` 按需构建；Node 依赖缺失或 lockfile 更新时自动执行 `npm ci`。
- WebView2 Evergreen Runtime（WPF 客户端依赖）。

## 故障排查

- **isolated-vm 加载失败**：`package.json` 已用 `overrides` 锁 `isolated-vm@6.1.2`（自带
  win32-x64 预编译二进制，ABI137=Node24），终端用户无需装 VS。完整性校验会捕获损坏的 native
  模块（如杀软误杀），并自动自愈重装。
- **端口 8055 被占**：`DirectusEnvMaterializer.PickFreePort` 自动避让到 8056+；实际端口写回
  运行目录的 `.env`。
- **`/vibetable-bulk-mutation/apply` 返回 404（但日志显示 "Loaded extensions"）**：检查 `.env`
  是否误设了 `EXTENSIONS_PATH` 为绝对/POSIX 路径——Directus 12 对该值解析敏感。
  **不要设 `EXTENSIONS_PATH`**，让它默认用相对的 `./extensions`。
- **npm ci 报 `ENOTEMPTY: rmdir ...`**：Windows + Defender/OneDrive 下的并发文件锁偶发问题。
  删除运行目录的 `node_modules` 后重跑（cache 已就绪，秒装）。
- **想完全重置 Directus 数据**：删除运行目录（`%LOCALAPPDATA%\VibeTable\directus` 下，或开发态
  `scripts/local_directus/` 下）的 `{data,uploads,.env,.bootstrapped,.schema-applied,.install-verified}`
  后重跑。

## 分发（客户端安装包）

VibeTable 单机安装包：捆绑 Node 与 npm 客户端（`runtime/node/`）+ PyInstaller onedir 后端 + npm manifest/lockfile
+ schema/capability + 扩展。**不含 `node_modules`**（省约 600MB），客户端首次启动时由宿主联网
`npm ci` 拉取 Directus 12，全部写入 `%LOCALAPPDATA%\VibeTable\directus`
（`.npm-cache`/`.npm-prefix`/`node_modules` 都在该目录内），**不污染用户全局 Node/npm/PATH**。
详见 `scripts/local_directus/README.md`。
