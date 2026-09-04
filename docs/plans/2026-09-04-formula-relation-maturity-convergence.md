# 公式与关联端到端成熟度收敛计划

> 状态：Active；本文件冻结工作边界，不代表所列能力已经通过。
>
> 差距分析基线：`main@57cbad609cced20ae5024ae46dc5b5c8d635b0bc`（需求附件冻结）
>
> 集成基线：`main@24ead0cf0ae49ad0e9bad211bda247d891c64164`（提交前 fresh main）
>
> 架构决策：[ADR 0013](../adr/0013-formula-relation-product-authority.md)
>
> 资格门槛：[公式与关联资格规范](../quality/formula-relation-qualification.md)

## 1. 目标与非目标

本轮把已有 Formula、Relation、Lookup 和重算内核收敛为一个可编写、可验证、可恢复、可规模化的产品
纵切。完成时，普通用户可以创建双向 Relation、选择记录、建立 Lookup 和 Formula，修改来源后观察精确
重算，并在重启和 snapshot 恢复后继续得到一致结果。

本轮不增加 Rollup、任意跨表 `SUMIF`、通用 map/filter、列表 Formula、用户函数、脚本公式、跨
workspace Relation 或 Decimal。条件能力只有在资格证据触发时立项。

## 2. 当前基线

| 范围 | 已有证据 | 尚缺证据 |
|---|---|---|
| Formula | cel-go、类型/依赖/循环/预算、validate/preview、关联聚合、可恢复 backfill | author document/source map、完整 CEL v1 golden、有界缓存、结构化工作台 |
| Relation | pair identity、成对 Schema 计划、reciprocal transaction、自关联、delta API | pair update、many→one 预检、完整性 inspect/repair、完整 picker 冲突恢复 |
| Lookup | 类型化结果、来源分页、最长 8 跳、批量 traversal chunk | 页面级 projection plan、查询数门禁、与 Formula 的统一 DAG |
| Recalculation | `changedFieldIds`、持久 fan-out、取消/恢复/重放、10k 测试 | 成本计划、公开状态闭集、精确反向发现与 100k 资格 |
| Product E2E | `05-formula-lifecycle`、`06-relation-fanout` | Relation→Lookup→Formula→跨表重算→重开/恢复闭环 |

`05-formula-lifecycle` 只证明字段转换和迁移回滚；`06-relation-fanout` 只证明 cascade 影响预览。两者不能
替代本轮产品纵切。

## 3. PR 路线

| 顺序 | PR seam | 主要验收 | 前置 |
|---:|---|---|---|
| 1 | `docs(computation): 冻结公式关联成熟度与资格场景` | ADR、路线、资格规范和诚实能力矩阵 | fresh main |
| 2 | `feat(contract): 增加公式作者文档与统一计算状态` | closed schema、四语言生成物、UTF-16 range、状态闭集 | 1 |
| 3 | `refactor(formula): 建立服务端作者 token 与 source map` | 同名/改名/粘贴/`#REF!`/往返测试 | 2 |
| 4 | `refactor(rpc): 原子迁移公式与关系 Product RPC owner` | Go producer、Host allowlist、Python 注销、inventory | 2；避开在途 RPC PR |
| 5 | `perf(formula): 冻结 CEL 语义与有界编译缓存` | golden、LRU/singleflight、失效、fuzz | 3 |
| 6 | `feat(formula-ui): 引入结构化 Formula Workbench` | token、补全、诊断、键盘、stale preview | 3 |
| 7 | `feat(relation): 完成 pair 更新与完整性模型` | patch、基数预检、immutable target、inspect/repair | 2 |
| 8 | `feat(relation-ui): 完成分页选择与冲突恢复` | cursor、键盘、多选、canonical refs、显式重载 | 7 |
| 9 | `refactor(computation): 统一 Lookup 与 Formula 依赖计划` | 跨字段/跨表 DAG、循环检测、原子依赖提交 | 3、7 |
| 10 | `perf(lookup): 批量执行路径投影` | 查询数不随 rows×fields 线性增长、分页/取消 | 2；可与 9 并行 |
| 11 | `perf(computation): 精确 fan-out 与公开重算状态` | changed fields 裁剪、成本、状态、取消/恢复 | 9、10 |
| 12 | `test(e2e): 覆盖 Relation Lookup Formula 产品闭环` | 三条真实打包场景及截图 | 4、6、8、11 |
| 13 | `test(qualification): 收口规模恢复兼容与能力声明` | 10k/100k、snapshot、N-1、矩阵和旧 adapter 清理 | 12 |

PR 4 合并原建议的只读和变更 RPC owner，只在所有方法已共享相同 Host transport、parity tests 与回滚边界
时采用；否则拆回两个 PR。其他相邻项只有同时满足以下条件才可放大：

- 本地定向测试和适用质量入口已通过；
- 不增加第二个语义 owner 或跨越不可逆迁移边界；
- diff 仍可由一个 seam 和一个验收结论准确描述；
- 与在途 PR 没有共享热点，或已先把 fresh main 合入新基线；
- 双轴审查仍可在一次审查中完整覆盖。

## 4. 条件路线

- 可重建关系边投影：只有反向发现的查询数/扫描量在既定 fixture 上不达标，且批量 SQL 与分层反向查找
  已有证据不足时才实施。
- 多跳关联聚合：属于本轮 Closed 之后的增强，不阻塞当前完成定义。

## 5. 每个 PR 的固定交付循环

1. 从 fetch 后的 `GitHub/main` 建独立 `codex/*` worktree；检查开放 PR、其他活跃任务和文件 owner。
2. 先运行最小定向测试，再运行受影响语言的格式、类型、单元/集成、生成一致性和适用 E2E。
3. 显式暂存任务文件，正常运行 hooks；不使用 `--no-verify`，不把规划记忆、缓存或构建产物提交。
4. 以 `GitHub/main...HEAD` 为固定点，独立并行执行 Standards 与 Spec 双轴审查；修复回到原 owner。
5. push 后记录精确 head SHA，仅接受该 SHA 的 fresh required CI；pending 不等于通过。
6. required、review conversation 与 base 同步条件均满足后才 squash merge，不用 admin/bypass。
7. fetch 后验证 merge commit 在最新 main 可达，且 branch tree 与 merge commit tree 等价。
8. 精确跟踪该 merge commit 的 main CI 和关联 CD；普通 PR 不应意外发布。
9. worktree 干净且 PR 已确认 MERGED 后，使用 `git worktree remove` 和安全分支清理流程。

CI/CD 状态监控只读，不重跑、触发、取消或批准 run。主线程负责失败归因、代码验收和最终放行。

## 6. 本地环境复用

- Python 复用仓库已按 lock 建好的 `.venv`，使用 `uv run --frozen --no-sync`；只有依赖确实变化时才
  `uv sync --frozen --group dev --group build`。
- Node、Go 与 .NET 使用仓库锁定版本和本机缓存；不修改 registry、lock、remote 或 CI 来适配本机。
- 同一未变化提交不重复构建完整发布包；先聚焦测试，合并前再运行一次适用的完整本地入口。
- 真实 WPF/WebView2 和 release smoke 产物保留到本 PR 证据确认后，再按仓库清理脚本处理。

## 7. 路线汇合

```text
资格基线 → contract/author ──┬→ Formula 语义 → Formula UI ───┐
                            ├→ RPC owner ─────────────────────┤
                            ├→ Relation 模型 → Relation UI ───┤
                            └→ Lookup 批量 ─┬→ ComputationPlan │
                                           └→ fan-out/status ─┤
                                                               ↓
                                                        产品 E2E → 最终资格
```

每次 merge 后重新基于 main 和在途 PR 检查路线，不让本计划中的旧文件清单覆盖当前仓库事实。
