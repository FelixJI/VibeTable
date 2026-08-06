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

为公共自动化增加可选的 sharded prepare 阶段声明，默认仍为现有阶段，只有 VibeTable 配置为 `bootstrap + release_build`。完整 quality/E2E 证据继续由 `core`、`race`、`resilience` 和 `release` lanes 提供，19 个 `REQUIRED_STAGES` 保持不变。

实施时先确认 release build 只依赖 bootstrap 产物；若存在由 quality 生成的输入，把该输入生成动作移入 bootstrap 或 build adapter，而不是恢复整套 quality。预期收益是移除 prepare 与 core 的重复时间，首轮目标为将成功 PR CI 中位墙钟降到 35 分钟以内。

### 阶段二：按 Go package 拆分 race 关键路径

先在 race 报告中记录各 package 的编译和测试耗时，再按实际重量分成两个近似均衡的 matrix lane。每个 package 仍执行 `-race`，聚合器要求所有 race 分片成功后才产生 `go-race` 通过证据。

同时缓存声明版本的 w64devkit 工具目录，避免每个新 runner 重复下载和解压。这里不添加无依据重试；真实 race 失败仍直接失败。阶段目标是在三次连续成功运行中把 race 墙钟降到 16 分钟以内，并将整体 CI 中位墙钟降到 25 分钟左右。

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
