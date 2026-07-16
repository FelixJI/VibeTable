# local_directus — Directus 12 运行时引入源（npm manifest）

本目录是 **Directus 12（SQLite）运行时引入的声明源**。它本身不再是可运行脚本——
Directus 的安装、启动、bootstrap 全部由 **C# 宿主** 负责（见
`desktop/src/VibeTable.Infrastructure/Directus/`）。

## 目录里现在有什么

| 文件 | 作用 |
|------|------|
| `package.json` | 声明 `directus@^12` + `isolated-vm@6.1.2`（`overrides`）依赖 |
| `package-lock.json` | 锁定的依赖图（`npm ci` 严格按它安装） |
| `.env.template` | Directus 运行配置模板（端口/密钥/SQLite 路径等占位） |

> `run.py` / `install.py` 已删除：宿主的 `DirectusPackageManager`（npm ci + 完整性校验 +
> marker 缓存 + 自愈）、`DirectusEnvMaterializer`（.env 生成 + 端口避让）、
> `DirectusSchemaBootstrapper`（bootstrap DB + seed schema）和 `DirectusSupervisor`
>（直接 `node <directus-cli> start`）接管了全部职责。

## 运行时如何被引入

1. **安装**：宿主 `DirectusPackageManager` 用捆绑 Node（`runtime/node/`）在本目录跑
   `npm ci`，把 `node_modules/directus` 拉到**应用私有目录**
   （`%LOCALAPPDATA%\VibeTable\directus`），npm 的 cache/prefix 全部重定向到该目录，
   **不污染用户全局 Node/npm/PATH**。
2. **校验**：装完做三层完整性校验（结构 + `isolated-vm` 原生加载 + lockfile 哈希），
   通过后写 `.install-verified` marker（7 天周期重校验；失败自愈重装）。
3. **配置**：`DirectusEnvMaterializer` 从 `.env.template` 生成 `.env`（随机密钥、端口避让）。
4. **bootstrap**：`DirectusSchemaBootstrapper` 跑 `directus bootstrap` 建 DB + 管理员，
   再通过 REST 灌入 VibeTable schema（collections/relations/policies），均幂等。
5. **启动**：`DirectusSupervisor` 直接 `node <directus-cli> start`，轮询 `/server/ping`。

## 开发期手动操作（可选）

开发期可绕过宿主，直接在本目录手动起 Directus 用于调试：

```powershell
Set-Location scripts\local_directus
npm ci                     # 首次（隔离环境由宿主管；手动跑用系统 npm）
npx directus bootstrap     # 首次：建表 + 管理员
npx directus start         # 启动，访问 http://localhost:8055
```

Schema 灌入已由 C# 宿主负责；如需手动灌，参考 `directus/blueprints/vibetable-empty.json`
通过 Directus Admin UI 或 REST API 应用。

## 重置实例

完全重置（清库重来）——开发态本目录：

```powershell
Stop-Process -Name "node" -ErrorAction SilentlyContinue   # 停掉 directus
Remove-Item -Recurse -Force data, uploads, .env, .bootstrapped, .schema-applied, .install-verified
```

下次宿主启动会重新 bootstrap + 灌 schema。

## 产物与数据（均被 gitignore）

| 路径 | 内容 |
|------|------|
| `.env` | 运行配置 + 随机实例密钥（由宿主生成） |
| `node_modules/` | `npm ci` 拉取的 Directus 运行时 |
| `data/directus.sqlite` | SQLite 业务数据库 |
| `uploads/` | Directus Files 本地存储 |
| `extensions/vibetable-bulk-mutation/` | 宿主复制的端点扩展 |
| `.install-verified` / `.bootstrapped` / `.schema-applied` | 幂等标记 |

## 许可证（MSCL）

个人/内部/免费分发属于 Permitted Purposes，允许随应用分发 Directus。不得去除/绕过
license key 机制。商用销售给第三方需另购 Directus 商业许可。
