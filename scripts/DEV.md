# 产品开发启动

开发环境由桌面宿主统一拥有：宿主启动 PocketBase sidecar 和 Python
JSON-RPC 后端，并加载已构建的 Web Grid。运行下面的命令会完成默认构建并
直接打开 VibeTable 桌面窗口：

```powershell
.\.venv\Scripts\python.exe scripts\dev.py
```

脚本只使用已经恢复好的依赖；不会执行 `npm install`、联网安装运行时，
发布包也不携带 Node/npm。`dev.py` 只启动桌面宿主，sidecar 会话密钥、
回环端口和就绪握手均由宿主内部管理，不会再额外启动一套重复的 sidecar、
Python 或 Vite 进程。

只验证构建链：

```powershell
.\.venv\Scripts\python.exe scripts\dev.py --build-only
```

在 Windows 本机运行 `qa/next.py` 的 Go race 阶段前，还需准备支持
`libsynchronization.a` 的 MinGW-w64。可把 w64devkit 解压到
`.tools/w64devkit/`；`qa/next.py` 会自动设置 `CGO_ENABLED=1`、`CC` 和
进程级 `PATH`，不会修改系统全局环境。

常用参数：

- `--no-web-build`：复用现有 Web 产物。
- `--no-sidecar-build`：复用 `build/dev/vibetable-pb.exe`。
- `--no-host-build`：跳过桌面宿主构建。
- `--data-dir PATH`：把该目录作为独立的开发期运行时根目录；脚本通过
  `--dev-data-root` 明确交给源码布局宿主，PocketBase、后端状态/日志、
  workspace mounts、附件预览和 WebView2 数据都从这里派生，不影响已安装
  版本的数据。脚本还会清除遗留的 E2E、sidecar 和状态目录覆盖变量。

正式发布使用 `scripts/build_next.py --release`。发布包内包含固定版本 sidecar、迁移清单及哈希、构建信息、第三方许可证和 CycloneDX SBOM。用户数据必须位于每用户本地数据目录，不得放在安装目录；升级前使用 `scripts/release.py prepare-upgrade` 创建可校验备份。
