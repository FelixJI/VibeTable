# 代码质量控制工具

本目录包含版本/打包契约、代码格式化、静态检查、测试覆盖率和跨栈门禁脚本。

## 使用的工具

项目使用以下代码质量工具（已在 `pyproject.toml` 中配置）：

| 工具 | 用途 | 配置位置 |
|------|------|----------|
| **ruff** | 代码格式化 + linter + import 排序 | `[tool.ruff]` |
| **pyright** | 静态类型检查 | `[tool.pyright]` |
| **pytest** | 测试框架 | `[tool.pytest.ini_options]` |
| **pytest-cov** | 测试覆盖率 | `[tool.coverage]` |

> 注：format.py 也支持可选的 black/isort（通过 `--ruff` 切换），type_check.py 也支持可选的 mypy

## 快速开始

### 交互式运行（推荐）

```bash
# 交互式选择检查项
python qa/run.py

# 自动修复问题
python qa/run.py --fix

# 生成报告文件
python qa/run.py --report
```

### 运行所有检查

```bash
# 运行全部只读检查（含版本与打包契约）
python qa/run.py --all

# 自动修复问题
python qa/run.py --all --fix

# 快速检查（跳过测试）
python qa/run.py --all --quick

# CI 模式（严格检查，生成报告）
python qa/run.py --all --ci
```

### 选择性运行

```bash
# 只运行格式化和代码检查
python qa/run.py format lint

# 只运行类型检查
python qa/run.py type_check

# 运行多个检查项
python qa/run.py format lint type_check
```

### 单独运行各检查

```bash
# 格式化代码
python qa/format.py           # 格式化
python qa/format.py --check   # 只检查

# 代码检查
python qa/lint.py             # 检查问题
python qa/lint.py --fix       # 自动修复
python qa/lint.py --stats     # 显示统计

# 类型检查
python qa/type_check.py           # 运行所有类型检查
python qa/type_check.py --pyright # 只运行 pyright
python qa/type_check.py --mypy    # 只运行 mypy

# 测试覆盖率
python qa/coverage.py           # 运行测试并生成报告
python qa/coverage.py --html    # 生成 HTML 报告
python qa/coverage.py --min 80  # 设置最低覆盖率阈值

# 版本与打包契约（不编译、不修改文件）
python qa/version_check.py
python qa/package_check.py

# 依赖升级
python qa/upgrade_deps.py           # 升级依赖并更新 pyproject.toml
python qa/upgrade_deps.py --dry-run # 预览变更
python qa/upgrade_deps.py --sync    # 升级后同步环境
```

## 命令行选项

| 选项 | 说明 |
|------|------|
| `--all` | 运行所有检查 |
| `--fix` | 自动修复可修复的问题 |
| `--quick` | 快速检查（跳过测试） |
| `--ci` | CI 模式（严格检查，生成报告） |
| `--report` | 生成报告文件 |
| `--report-format` | 报告格式：text/json/all |
| `--no-interactive` | 禁用交互模式 |

## 报告输出

使用 `--report` 选项会在 `reports/` 目录生成报告文件：

```bash
python qa/run.py --all --report
```

生成的文件：
- `reports/report_YYYYMMDD_HHMMSS.txt` - 文本报告
- `reports/report_YYYYMMDD_HHMMSS.json` - JSON 报告

## Makefile 命令

项目也提供 Makefile 命令：

```bash
make all         # 运行所有检查
make format      # 格式化代码
make lint        # 运行 linter
make lint-fix    # 修复 lint 问题
make type-check  # 类型检查
make ci          # CI 模式检查
make clean       # 清理缓存
```

## 脚本说明

| 脚本 | 功能 |
|------|------|
| `run.py` | 主入口脚本，支持交互式选择和报告生成 |
| `version_check.py` | 检查 Python、WPF、Web、Directus 与清单版本一致性 |
| `package_check.py` | 只读检查打包输入、扩展入口与发布清单 |
| `format.py` | 代码格式化（ruff format，可选 black + isort） |
| `lint.py` | 代码问题检查（ruff） |
| `type_check.py` | 静态类型检查（pyright，可选 mypy） |
| `coverage.py` | 测试覆盖率（pytest-cov） |
| `upgrade_deps.py` | 依赖升级（uv lock --upgrade + 同步 pyproject.toml） |

## 推荐工作流

1. **开发时**: `python qa/run.py --fix` - 自动修复问题
2. **提交前**: `python qa/run.py --all --quick` - 快速验证
3. **CI 中**: `python qa/run.py --all --ci` - 严格检查并生成报告

## VibeTable 统一构建与跨栈门禁

`scripts/build_next.py` 负责把 .NET host、Python backend、web-grid 和 Directus
extension 从同一版本源编排进 `dist/VibeTable.Next/`。`pyproject.toml` 的
`[project].version` 是唯一版本源；发现任一组件漂移时，构建会在清理 staging 前失败。

```bash
# 版本更新、清单与 release 参数回归
python -m pytest tests/test_release_tooling.py --no-cov -q

# 完整 release 构建（需要 node + dotnet + nuitka 在 PATH，且可联网下载依赖）
python scripts/build_next.py --release

# 跨栈完整门禁
python qa/next.py --ci
```

关键约束：
- **原子 staging**：所有阶段先写入 `dist/.VibeTable.Next.staging/`，通过 `publish-layout.json`
  校验后才原子替换 `dist/VibeTable.Next`；任一阶段失败则已发布目录保持原样。
- **混版拒绝**：清单记录四个组件版本、`protocolVersion=1.0`、WebView2 SDK
  `1.0.4078.44`、Tabulator `6.5.2` 及相对启动路径。
- **扩展可部署**：发布目录包含 Directus 可识别的 package.json 与 dist/index.js。
- **后端去膨胀**：Nuitka 使用 package `-m` 模式，并拒绝把开发 venv 中无运行时引用的 mypy/pytest/numpy/pandas 打入发布包。
- **开发开关受限**：`--skip-web` / `--skip-backend` / `--skip-desktop` /
  `--skip-directus` 仅用于开发期
  分阶段验证，与 `--release` 组合会被 argparse 直接拒绝（exit 2）。
