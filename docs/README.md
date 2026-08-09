# VibeTable 文档

VibeTable 是一个通用的建表与文件管理桌面工具。本目录存放项目文档。

完整的安装、开发、构建说明见仓库根目录的 [README](../README.md)。

## 现行架构与质量入口

- [`adr/`](adr/)：架构决策记录；历史决策会在正文顶部标明已替代关系。
- [跨进程 seam 索引](architecture/interprocess-seams.md)：Web、WPF、Python BFF 与 Go sidecar 的
  authority、session/epoch、错误和取消语义。
- [质量门禁](quality-gates.md)：本地最小反馈、GitHub `required` 与发布资格范围。
- [稳定化台账](quality/stabilization-ledger.md)与[能力闭环矩阵](quality/capability-matrix.md)：
  当前缺陷、验收空白、用户能力可见性与冻结退出证据。
- [Node 开发工具链](node-toolchain.md)：锁定版本、来源、恢复方式和发布包边界。

## 计划与历史材料

- [当前稳定化实施方案](plans/2026-08-08-technical-debt-stabilization.md)是冻结期主计划；其他
  [`plans/`](plans/) 文件保留各能力的历史范围、状态和未完成证据。
- [`research/`](research/) 保存研究结论；`research/archive/` 仅是历史材料，不代表现行实现。
