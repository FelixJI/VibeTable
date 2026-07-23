# VibeTable

通用建表与文件管理桌面工具。基于 Directus 12 数据层，支持自由创建/删除数据表，配合完整的文档工作区（版本管理、内容比对、发布链接）。

## 特性

- **自由建表**：动态发现 Directus 集合，应用内 UI 创建/删除表与字段
- **表格交互**：查询、筛选、排序、批量粘贴、CSV/Excel 导入导出
- **文件管理**：文件上传/预览/恢复、git-like 本地内容存储、OpenXML 文档比对
- **文档工作区**：文档版本、发布、链接到任意业务表、修订历史
- **实时同步**：Directus WebSocket 实时更新
- **离线友好**：设备本地网格状态、修订历史、安全恢复

## 技术栈

- **桌面宿主**：.NET 10 WPF + WebView2
- **后端 BFF**：Python 3.11+（JSON-RPC over stdio）
- **前端表格**：TypeScript + Vite + Tabulator
- **数据层**：Directus 12（PostgreSQL）

## 开发环境

- Windows 10/11
- Python 3.11+ 或 3.13（venv）
- .NET 10 SDK
- Node.js 24.18（见 `.nvmrc`）
- WebView2 Runtime
- Directus ≥12 且 <13

## 验证命令

```bash
# Python 后端测试
uv run pytest

# .NET 测试
dotnet test desktop/VibeTable.Desktop.sln

# Web-grid 测试
cd desktop/web-grid && npm test

# 综合 QA
python qa/run.py
```

## 构建

```bash
python scripts/build_next.py   # 输出到 dist/VibeTable.Next/
```

## 版本管理

单一版本来源：`pyproject.toml [project].version`。发布用 `python scripts/release.py`。

## License

Copyright (c) 2026 Felix Ji. 本项目基于 [MIT License](LICENSE) 发布。

仓库中捆绑或引用的第三方组件仍遵循其各自的许可证；相关许可证文件随对应组件保留。
