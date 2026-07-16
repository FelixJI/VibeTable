# local_directus — 本机运行时引入的 Directus 12（SQLite）

VibeTable 定位为**单机应用**，Directus 作为本地数据层随应用一起运行。本目录实现两种形态：

- **开发/测试**：`run.py` 一键拉起本机 Directus 12 + SQLite（运行时 `npm install`）。
- **分发**：客户端安装包不含 `node_modules`（保持小体积），首次启动时由已打包的
  `vibetable-backend.exe --local-directus-runner` 联网把 Directus 12 拉取到**应用私有目录**，
  `%LOCALAPPDATA%\VibeTable\directus`，**不污染用户的全局 Node/npm/PATH**，也不要求系统 Python。

Directus 用本地 SQLite 文件做数据库，因此**不需要 Docker，也不需要外部 PostgreSQL**。
`isolated-vm@6.1.2`（Directus 的原生依赖）自带 `win32-x64` 预编译二进制（ABI137=Node24），
终端用户**无需安装 Visual Studio/C++ 工具链**。

> **许可证（MSCL）**：个人/内部/免费分发属于 Permitted Purposes，允许随应用分发 Directus。
> 不得去除/绕过 license key 机制。商用销售给第三方需另购 Directus 商业许可。

## 前置

- Node.js 24.x（仓库 `.nvmrc` 已固定），`npm`/`npx` 在 PATH 上。
  - 终端用户机器：客户端启动器应引导安装 Node（弹链到 nodejs.org），或随附便携 Node。
- Python 仓库 venv（用 `.venv\Scripts\python.exe` 运行 `run.py`）——仅开发需要。
- 已构建 bulk-mutation 扩展：`directus\extensions\vibetable-bulk-mutation` 下执行过
  `npm install && npm run build`（产出 `dist/index.js`）。分发时该 `dist` 由安装器复制进
  `extensions/`。

## 一键启动

```powershell
.\.venv\Scripts\python.exe scripts\local_directus\run.py
```

`run.py` 做的事（幂等）：

1. `node_modules/directus` 不存在则 `npm install`（运行时引入 Directus 12）。
2. 从 `.env.template` 生成 `.env` 和随机 `KEY`/`SECRET`。开发者直接运行时也随机生成并保留
   `ADMIN_PASSWORD`；客户端首次设置时使用用户密码或托管随机密码，bootstrap 完成后从 `.env`
   清除明文密码。
3. 把已构建的 `vibetable-bulk-mutation` 扩展复制进 `extensions/`，注册
   `POST /vibetable-bulk-mutation/apply` 端点。
4. 以子进程启动 `directus start`，等待 `GET /server/ping` 就绪。
5. **首次启动**用 admin 登录换 token，在 runner 进程内调用已有 bootstrapper
   灌入 VibeTable 的 collections / relations / policies 蓝图；之后用 `.schema-applied`
   标记跳过，避免重复 bootstrap。

启动成功后，按提示设置客户端环境并运行 VibeTable：

```powershell
$env:VIBETABLE_DIRECTUS_URL = "http://localhost:8055"
$env:VIBETABLE_DIRECTUS_PROJECT = "default"
# 管理员账号见 .env（首次启动时随机生成）
```

按 `Ctrl+C` 停止（会一并终止 Directus 子进程）。

## 分发形态（客户端首次启动）

安装包不含 Directus `node_modules`（避免约 600MB 体积），但包含 `run.py`、锁文件、
blueprint/capability 和已构建扩展。双击发布包且未配置外部 URL 时，WPF 自动调用随包的
backend runner，联网把 Directus 12 拉到本目录并弹出首次账号设置。发布态无需执行下面的
Python 命令；实际可写运行目录是 `%LOCALAPPDATA%\VibeTable\directus`，安装目录中的
`local-directus/` 只是只读模板。这些命令仅用于开发：

```powershell
# 由客户端启动器在首次运行时调用（需用户机器有 Node.js 24.x + 联网）：
python scripts\local_directus\install.py
# 完成后，run.py 负责后续每次启动（bootstrap/schema/start 已幂等）
python scripts\local_directus\run.py
```

`install.py` 会：① `npm install`（cache/prefix 重定向到 `./.npm-cache`、`./.npm-prefix`）
② 校验 `isolated-vm` 原生二进制能加载（最易出错的一步，提前暴露）。

**环境隔离保证（实测）**：所有 npm 副作用落在 `scripts/local_directus/` 内，**不写**用户全局
`%APPDATA%\npm`、不改系统 PATH、不污染全局 npm cache。卸载只需删应用目录。

> **Windows + npm 偶发 `ENOTEMPTY`**：并发文件操作在 Defender/OneDrive 下偶发 `rmdir`
> 失败，是 npm 在 Windows 的已知问题。`install.py`/`run.py` 失败后**重跑即可**（cache
> 已就绪，`--prefer-offline` 秒装）。

## 选项

| 参数 | 说明 |
|------|------|
| `--no-apply` | 只启动 Directus，不灌入 VibeTable schema（空实例） |
| `--port <n>` | 覆盖端口（默认读 `.env`/模板里的 `PORT=8055`） |

## 产物与数据（均被 gitignore）

| 路径 | 内容 |
|------|------|
| `.env` | 运行配置 + 随机实例密钥；客户端模式不会长期保存管理员明文密码 |
| `node_modules/` | `npm install` 拉取的 Directus 运行时 |
| `data/directus.sqlite` | SQLite 业务数据库（所有业务数据在此） |
| `uploads/` | Directus Files 本地存储 |
| `extensions/vibetable-bulk-mutation/` | 复制自构建产物的端点扩展 |
| `.schema-applied` | schema 已 bootstrap 的幂等标记 |

## 重置实例

完全重置（清库重来）：

```powershell
Stop-Process -Name directus -ErrorAction SilentlyContinue   # 或在 run.py 里 Ctrl+C
Remove-Item -Recurse -Force data, uploads, .schema-applied, .env
```

再次运行 `run.py` 即得到全新空实例并重新 bootstrap。

## 仅启动 Directus（手动操作）

```powershell
Set-Location scripts\local_directus
npm install                # 首次
$env:PORT = "8055"         # 或直接用 .env 里的值
npx directus start
```

然后另开终端用 `scripts/directus_project.py apply` 灌 schema（等价于 `run.py` 的自动步骤）。

## 注意

- Directus 12 引入了 license enforcement（MSCL）；本机开发/测试用途不受影响，商用分发
  需另行评估许可。
- 真正离线分发（无网安装）时，需把 `node_modules/directus` 预置进安装包——这是后续独立
  的打包工作，不在本目录范围内。
- Realtime/WebSocket：`.env` 已设 `WEBSOCKETS_ENABLED=true`，使用 handshake 鉴权模式。
