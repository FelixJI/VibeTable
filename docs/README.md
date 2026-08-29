# VibeTable 文档

VibeTable 是一个通用的建表与文件管理桌面工具。本目录存放项目文档。

完整的安装、开发、构建说明见仓库根目录的 [README](../README.md)。

## 现行架构与质量入口

- [面向初学者的源码阅读指南](source-reading-guide.md)：从进程模型、启动链和两条真实请求链开始阅读多栈源码。
- [`adr/`](adr/)：架构决策记录；历史决策会在正文顶部标明已替代关系。
- [跨进程 seam 索引](architecture/interprocess-seams.md)：Web、WPF、Python BFF 与 Go sidecar 的
  authority、session/epoch、错误和取消语义。
- [质量门禁](quality-gates.md)：本地最小反馈、GitHub `required` 与发布资格范围。
- [产品 E2E 能力索引](quality/product-e2e-capability-index.md)：由权威 manifest 生成的当前场景与
  selector tag 双向映射；实际通过状态仍以对应 `required` 报告为准。
- [稳定化台账](quality/stabilization-ledger.md)与[能力闭环矩阵](quality/capability-matrix.md)：
  当前缺陷、验收空白、用户能力可见性与冻结退出证据。
- [Node 开发工具链](node-toolchain.md)：锁定版本、来源、恢复方式和发布包边界。
- [插件开发、打包与 GitHub Release 安装](plugin-development.md)：插件 SDK、权限边界、发布和远程安装约定。
- [自我更新能力与安全边界](self-update-assessment.md)：应用更新的信任、下载与代理边界。

## 计划与历史材料

- [成熟度收敛与运行时职责演进实施指南](plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md)
  是当前实施入口：它以 2026-08-17 成熟度审计为历史基线，逐项核对 TD-01～TD-14、旧路线和验收清单的
  完成度，并给出剩余成熟度工作、Python/Go/C# 职责演进以及并行/串行依赖。
- [2026-08-08 技术债治理与架构稳定化方案](plans/2026-08-08-technical-debt-stabilization.md)保留为已经完成主要目标的
  冻结期方案；其他 [`plans/`](plans/) 文件保留各能力的历史范围、状态和未完成证据。
- [`research/`](research/) 保存研究结论；`research/archive/` 仅是历史材料，不代表现行实现。
