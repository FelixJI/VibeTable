# VibeTable GitHub CI 加速方案

## 目标与不变量

目标是在不减少测试、覆盖率、E2E、release build/smoke 或 `required` 门禁的前提下，缩短 PR 与 `main` CI 的墙钟时间。VibeTable 可以采用不同于其他五仓的 workflow 拓扑，但继续遵守以下不变量：

- PR 和 `main` 都完成 `.ci/project.json` 声明的适用质量与发布门禁；
- squash merge 后由 `main` CI 验证合并结果并生成唯一 `release-candidate`；
- CD 只消费触发它的成功 `main` CI 候选，不重建、不重新运行完整 CI；
- `required` 聚合所有 lane 的真实结果并 fail closed。

## 当前基线

成功的 PR CI [run 31069109780](https://github.com/FelixJI/VibeTable/actions/runs/31069109780) 墙钟约 44 分钟：

| Job | 耗时 | 观察 |
|---|---:|---|
| `plan` | 20 秒 | 不是瓶颈 |
| `prepare` | 14 分钟 | 顺序执行 bootstrap、quality、e2e、release build |
| `core` | 约 11 分 24 秒 | 再次执行与 quality 大量重叠的 Go/Python/.NET/Web 门禁 |
| `resilience` | 约 12 分 29 秒 | fault injection 与 product E2E |
| `release` | 53 秒 | package 契约 |
| `race` | 约 29 分 13 秒 | 关键路径；报告中的 `go-race` 本体为 1625.75 秒 |
| `required` | 26 秒 | 不是瓶颈 |

当前单次 CI 内有两类主要成本：

1. `prepare` 的 quality 与 `core` lane 重复执行 Python、Web、Go 和 .NET 质量检查；
2. `go-race` 单 lane 独占约 27 分钟，其他 lane 完成后仍需等待它。

## 分阶段方案

### 阶段一：消除同一次 CI 内的重复质量检查

保持公共 `scripts/automation.py`/`scripts/automation_core.py` 不变，只在 VibeTable prepare step 注入候选模式。项目 adapter 在该模式仅恢复 release build 需要的 uv、web-grid npm 与 .NET 依赖，并把 quality 延后到候选绑定的 `core`、`race`、`resilience` 和 `release` lanes；19 个 `REQUIRED_STAGES` 保持不变。未设置候选模式的本地与串行入口继续执行完整 bootstrap/quality。

release build 已确认只依赖候选模式恢复的依赖，不消费 quality 生成物。PR #42 首轮 run `31102199882` 的 prepare 从历史约 14 分钟降至 4 分 55 秒，缩短约 9 分钟（约 65%）。

### 阶段二：按 Go package 拆分 race 关键路径

race worker 先输出结构化 `RACE_PACKAGE_TIMING`，记录 package、测试数和执行秒数。PR #42 首轮 run `31102199882` 测得整个 `go-race` 为 1669.547 秒，其中 `tests/integration` 单包为 1566.266 秒（99 项），占 stage 墙钟的 93.8%；因此纯 package 分片不能缩短关键路径。

最终采用实测秒数驱动的确定性贪心分配：普通 packages 精确属于 `race-a`/`race-b` 之一；只有实测主导的 integration package 按已经隔离的命名测试拆成两个子集，每条 lane 各编译一次该包并串行执行自己的子集。三个已知长测使用较高回退权重分散，新 package 在取得实测值前按测试数回退。两条 lane 的测试集合互斥且并集完整，聚合器要求两份同候选身份的 race 报告都成功后才合成唯一 `go-race` 通过证据。

按首轮 49 个 package 的实测值模拟，两个分片的调度权重分别约为 1792.3 与 1794.5 秒；integration 的 99 项测试分为 48/51 项。该权重只用于相对调度，不替代最终 Actions 墙钟验收。

同时使用固定 SHA 的 `actions/cache` 缓存声明版本的 `.tools/w64devkit`，只在两条 race lane 恢复/保存；通用 bootstrap 不再安装该 race 专属工具链，完整串行 smoke 仍在消费点显式安装。这里不添加无依据重试；任一真实 race 失败仍直接失败。阶段目标是在三次连续成功运行中把 race 墙钟降到 16 分钟以内，并将整体 CI 中位墙钟降到 25 分钟左右。

### 阶段三：再评估 setup 与缓存命中

用 Actions job 时间和项目报告检查 checkout、setup、`uv sync`、`npm ci`、Go module 与 .NET restore 的实际占比。只优化被数据证明占用显著的步骤；不为理论上的缓存 miss 增加额外校验层。

## PR CI、main CI 与 CD 是否重复

CD 本身没有重复 CI。它由成功的 `main` CI 唤起，下载同一 run、同一合并后 source SHA 的候选，然后进行 stage、attestation、Release 与镜像同步。

PR CI 与 `main` CI 的完整门禁确实重复，但职责不同：PR CI 决定是否允许合并，`main` CI 验证 squash 后的新提交并生成可发布候选。现阶段保留两者；跨 SHA 复用 PR 候选需要额外的 tree 等价与候选重绑定机制，复杂度和误用风险高于节省的时间。优先消除单次 run 内的重复并缩短 race 关键路径。

## 验收方式

- 修改前后各取至少三次成功 PR run，比较中位墙钟和各 job 耗时；
- 聚合报告仍包含全部 19 个 `REQUIRED_STAGES`，`required` 是唯一权威门禁；
- `main` CI 仍上传 `release-candidate`，普通 PR 不上传正式候选；
- CD 仍只消费成功 `main` CI 的候选，未新增构建或完整测试步骤；
- 不降低覆盖率，不跳过 E2E，不把失败改成 warning，不增加无依据重试。
