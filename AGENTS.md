# Repository Guidelines（仓库指南）

## 项目结构与模块组织

VibeTable 是仅支持 Windows 的离线优先桌面应用。`desktop/src/` 是 .NET 10 WPF/WebView2 宿主，`desktop/web-grid/` 是 Vue 3 + TypeScript 界面；Python 3.11 BFF 位于 `backend/`，Go/PocketBase sidecar 位于 `sidecar/`。共享 JSON 契约放在 `contracts/`，插件 API 放在 `sdk/plugin/`，示例插件放在 `examples/plugins/`。

Python 测试位于 `tests/`；Go 测试与源码同目录；Web 测试使用相邻的 `*.test.ts` 文件。质量门禁、开发脚本和文档分别位于 `qa/`、`scripts/`、`docs/`。

## 构建、测试与开发命令

- `uv sync --frozen --group dev --group build`：安装锁定的 Python 工具。
- `uv run python scripts/dev.py`：构建并启动完整桌面栈。
- `uv run pytest`：运行 Python 测试并检查后端覆盖率。
- 在 `desktop/web-grid/` 执行 `npm ci && npm run test && npm run build`：安装、测试、类型检查并打包 Web UI。
- `dotnet test desktop/VibeTable.Desktop.sln --configuration Release`：运行 .NET Release 测试。
- 在 `sidecar/` 执行 `go test ./...`：运行 Go 测试。
- `uv run python qa/next.py --ci --json-report build/qa/report.json`：运行完整 Windows 发布门禁。

## 编码风格与命名

Python、C# 使用四空格缩进，TypeScript/Vue 沿用两空格。Python 由 Ruff 格式化，使用双引号和 100 列目标；提交前运行 `uv run ruff format .` 与 `uv run ruff check .`。Python 模块和函数使用 `snake_case`，类与 Vue 组件使用 `PascalCase`。Go 代码必须通过 `gofmt`。CI 同时执行 Pyright、mypy、Vue 类型检查、`go vet`，并将 .NET 警告视为错误。

## 测试规范

Python 文件命名为 `test_*.py`，测试函数命名为 `test_*`；集成、慢速和 E2E 用例使用已注册的 pytest marker。后端覆盖率不得低于 85%。修改 Go 或 Web 功能时，在受影响代码旁添加聚焦的回归测试；先运行最小相关测试，再运行完整门禁。

## 构建产物与杀软约定

编译产物只保留在仓库固定目录：开发中间文件放入 `build/`，发布包放入 `dist/`，本地展开包放入 `VibeTable.Next/`。不要将 `.exe`、sidecar 或打包后的 Python 运行时复制到 `%TEMP%`、用户数据目录或系统目录，也不要编写自动删除、移动隔离文件或关闭杀软的脚本。上述目录不提交 Git，但在验证结束前应保留，避免频繁生成、搬移可执行文件触发误报。

若杀软误报，记录产物路径、SHA-256、构建命令和门禁报告；仅为精确的仓库产物目录配置排除项，并遵循组织安全策略，不得排除整个磁盘或用户目录。重新构建前先确认隔离状态，禁止静默下载或替换二进制。

## 空间治理与定期清理

每周先运行 `uv run python scripts/clean_workspace.py --scope all` 预览，再用同一命令加 `--apply` 清理超过 3 天的白名单缓存和构建产物；默认保留 `build/qa/` 最近 2 个运行目录。完整发布门禁结束且证据已归档后执行一次；若需立即回收，显式传入 `--older-than-days 0`。清理失败必须排查占用进程，不得改为宽泛递归删除。

worktree 不由脚本自动清理。先确认工作树干净，并用 `git merge-base --is-ancestor <head> main` 验证已合并，再执行 `git worktree remove <path>` 和 `git worktree prune`。禁止删除 `.git/`、`.venv/`、`.tools/`、`node_modules/`、用户数据或杀软隔离区。仍需保留验证中的 `dist/`、`VibeTable.Next/` 与最新 QA 报告，避免重复生成可执行文件。

## 提交与 Pull Request

提交遵循 Conventional Commits，例如 `feat(fields): add validation`、`fix(ci): preserve release gate`。提交应单一、命令式且范围明确。PR 需说明行为与风险，关联 issue 或设计文档，列出已运行命令；WPF/Web 可见改动需附截图。不得提交密钥、本地数据库、缓存或生成包。`backend/_version.py` 是唯一版本来源，版本更新必须通过 `scripts/release.py`。
