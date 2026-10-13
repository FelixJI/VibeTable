# 2026-10-13 非 main 分支与 worktree 复核

基线：`GitHub/main@28e3407bab0c8a1fc431f749a0d0c13d35a71bb5`。
范围：开始本轮时的 4 个本地非 main 分支、1 个远端非 main 分支、4 个 worktree（均为 detached HEAD），包含未提交及未跟踪源码。本文不将本轮新建的交付分支计入遗留项。

## 本地与远端分支

| 分支 | tip | 内容去向与处理 |
| --- | --- | --- |
| `codex/import-migration-435` | `47537e51` | 是 reviewfix 的祖先；迁移内核已随 [#440](https://github.com/FelixJI/VibeTable/pull/440) 进入 main，不重复移植。 |
| `codex/import-migration-435-reviewfix` | `656f9a66` | 是 lifecycle 的祖先；任务状态与暂存结算修复已随 #440 进入 main。 |
| `codex/import-migration-435-lifecycle` | `ac643e43` | 与 #440 的 squash 提交 `c7767076` 文件树一致；后续 [#441](https://github.com/FelixJI/VibeTable/pull/441) 已扩展飞书/WPS 来源和向导。 |
| `codex/release-security-fixes` | `baa0f6fe` | 与 [#442](https://github.com/FelixJI/VibeTable/pull/442) 的 squash 提交 `03a24719` 文件树一致；安全依赖、契约及 QA 修复均已保留。 |
| `GitHub/automation/release` | `527b902e` | [#478](https://github.com/FelixJI/VibeTable/pull/478) 仍开放，承载 0.5.4 版本/changelog 及派生契约。保留自动化所有权，不搬入普通 PR。 |

已用 GitHub PR 元数据确认 #440/#442 为 MERGED 且最终 head 对应上述 tip；两个 squash 提交均可从该 main 基线到达。早期导入分支通过祖先链确认内容已被最终分支包含，不以 `git cherry` 的 `+` 或分支名判断遗漏。

## 三个旧 worktree

| worktree 名称 | HEAD | 内容去向与判断 |
| --- | --- | --- |
| `document-diff-docx-provider` | `6cfbbfdd` | [#405](https://github.com/FelixJI/VibeTable/pull/405) 已迁入 DOCX 有预算读取、Unicode 对齐及 Worker 草稿；[#419](https://github.com/FelixJI/VibeTable/pull/419) 已接通生产链。未提交的三份 OpenXml 读取/预检文件与 main 一致；Worker/Supervisor/测试在 main 已增加 V2 协议、严格 Job 归属、修订元素覆盖等，不回退旧草稿。 |
| `fix-process-identity-lifecycle` | `9c8c2b1a` | 已提交修复的去向见 [9 月 30 日归并记录](2026-09-30-branch-consolidation.md)。未提交 `HostProcessScope.capture/bind/observe` 的手工 PID 谱系实验被 Windows Job 所有权替代；保留 main 的原子启动、成员观测、残留进程退出和端口复用回归。 |
| `test-retention-nonzero-apply` | `de577cfb` | 已提交及未提交的测试时钟接线被成熟度计划 A1 明确弃用；非零 Apply 与物理 Sweep 由[自然老化和独立 Sweep 证据](retention-natural-aging-evidence.md)分层承接。不得把旧生产 test-only 时钟参数重新引入。 |

复核覆盖 dirty diff、未跟踪 Worker/Supervisor 与测试源码、主干替代实现及现有归并记录。两个只读 GLM 子代理分别调查分支与 worktree，主代理复核关键 Git/PR 证据。结论是本轮未发现值得补移植的代码；不宣称所有历史提交均已逐函数证明等价。进程分支的已提交修复采用归并记录和关键文件抽查，DOCX 分支中非 DocumentDiff 的历史修改未逐文件重新审计。

## 本地原稿

- 主目录 `d7b1566b` 上的 Python 3.13 升级与旧 `_descendants` 修复已被后续工具链和 Windows Job 所有权方案替代。
- 未提交的 `TestSelectionProjectionUsesOneConsistentTransaction` 使用旧接口且依赖未定义的 `concurrentSelectionSource`；main 的 `TestSelectionProjectionCannotMixSchemaAndCursorRevisions` 已覆盖事务前后 schema/data revision 和行数，另有不一致来源拒绝测试，不复制旧草稿。
- 4 个 worktree 中的 `AGENTS.md` 修改均为移除截图要求，属于独立协作规则偏好；本轮保留本地原稿，不夹带到技术资料归档。
- **保留并完善**：[2026-08-17 成熟度审计原稿](archive/2026-08-17-vibetable-maturity-audit.md)。主干[成熟度计划](../plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md)已引用它作为输入，但原文尚未入库；本轮补上原文和双向链接，调整标题、增加历史边界告示，并把三处 Markdown 双空格换行改为等价的反斜线换行。
- `vibetable-audit-report-2026-08-11.md` 是更早基线，8 月 17 日报告及后续成熟度计划已经接续，不作为当前缺陷清单重新引入。
- `docs/research/updater-frameworks-2026-08.md` 保留本地；主干已有更晚的 [Velopack 官方证据](../research/2026-08-29-velopack-official-evidence.md)和[增量更新评估](../research/2026-08-29-vibetable-incremental-update-assessment.md)，本轮不重新发布旧框架推荐或启动更新器迁移。

## 交付边界

本轮保留所有原分支引用、worktree、未提交文件及未跟踪原稿；没有清理、合并或发布操作。文件树等价只说明代码已被保留，不代替以后清理时的最新远端、PR、工作树干净和授权检查。

历史审计中的测试成绩、根因判断和建议均为原稿内容，本轮没有重新验证历史 CI 或执行全栈发布资格。文档归档不能作为产品验收或发布证据。
