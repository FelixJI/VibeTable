# VibeTable GitHub CI 加速方案

## 目标与不变量

目标是在不减少测试、覆盖率、E2E、release build/smoke 或 `required` 门禁的前提下，缩短 PR 与 `main` CI 的墙钟时间。VibeTable 可以采用不同于其他五仓的 workflow 拓扑，但继续遵守以下不变量：

- PR 和 `main` 都完成 `.ci/project.json` 声明的适用质量与发布门禁；
- squash merge 后由 `main` CI 验证合并结果并生成唯一 `release-candidate`；
- CD 只消费触发它的成功 `main` CI 候选，不重建、不重新运行完整 CI；
- `required` 聚合所有 lane 的真实结果并 fail closed。

## 当前基线

### 2026-10-27 复核

下面是本轮修改前的实际 Actions 记录；后文的 44 分钟属于早期基线。

| 运行 | 总墙钟 | prepare | core | resilience | 最慢 race lane |
|---|---:|---:|---:|---:|---:|
| [main / #526](https://github.com/FelixJI/VibeTable/actions/runs/40462442231) | 73 分 23 秒 | 16 分 11 秒 | 27 分 20 秒 | 28 分 25 秒 | race-d：53 分 55 秒 |
| [PR #527](https://github.com/FelixJI/VibeTable/actions/runs/40464956891) | 76 分 20 秒 | 16 分 56 秒 | 33 分 29 秒 | 30 分 19 秒 | race-d：56 分 10 秒 |

关键路径是 `prepare → 最慢 race lane → required`。候选下载只占每 lane 约 12–30 秒，CodeQL 与候选构建并行，最长约 8 分钟；二者都不是这一轮的主要瓶颈。[GitHub 的 job 依赖规则](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-jobs)决定了增加有依赖的串行步骤会直接延长这条路径。

本轮把已有 race 调度扩为八个独立 runner，继续从 race 二进制发现完整测试集合，逐测试隔离执行，保留单 worker、超时和竞态检测。同时将 Python、contracts、tooling、.NET 和 Web 检查从 `core` 移到独立 `quality` lane；Go、开发构建及其 smoke 仍在 `core` 按原顺序执行。24 个必需阶段、全部产品 E2E、各栈覆盖率阈值和同一候选绑定不变。`required` 同时检查 CodeQL；任一 lane 缺失、失败或候选不一致仍拒绝通过。

这会增加 runner 并发和少量重复初始化成本。实际节省还取决于 runner 排队及每个测试的耗时分布；上述历史数据不能当作修改后的实测结果。

[PR #539 首轮](https://github.com/FelixJI/VibeTable/actions/runs/40490699063)的八条 race lane 全部通过，最长 `race-d` 为 28 分 16 秒，另有约 6 分钟排队；`quality` 为 20 分 44 秒，`file-io` 为 25 分 4 秒，`prepare` 为 19 分 51 秒。整轮在 57 分 54 秒结束，但因 Go 差异覆盖率不足及 JSON 往返场景失败而未通过，且 `required` 提前拒绝聚合，因此该总时长不能作为完整门禁通过的提速验收。race 分片的执行时长已从历史最长 54–56 分钟缩短至约 28 分钟；仍需成功全流程和后续多次运行验证。

[第二轮](https://github.com/FelixJI/VibeTable/actions/runs/40511704302)的八条 race lane、core 和 resilience 全部通过，S04 JSON 往返已恢复，resilience 的 29 项产品场景没有跳过。整轮因下面三项新故障失败，总墙钟 71 分 43 秒；`prepare` 执行 20 分 1 秒，最慢 race 执行 28 分 19 秒，新的关键路径 `resilience` 执行 36 分 45 秒，并在 prepare 完成后等待了 13 分 44 秒才启动。当时同仓有其他三轮 CI 并发，其他 shard 的启动也明显错开；该样本必须分别报告执行与 runner 等待，不能把增加并发宣称为无条件的总墙钟提速。

### 本轮故障定位

- [0.5.7 合并后的 main CI](https://github.com/FelixJI/VibeTable/actions/runs/40473110935)在 `49-plugin-application-security` 的 `contrast-off` 失败：系统切换高对比度时 `GetForegroundWindow` 返回 0，测试辅助代码在还原系统设置之前要求 Host 取得前台。该操作使用 `SystemParametersInfo`，不发送鼠标键盘输入。修复将系统设置恢复与输入焦点前提分开，保留 Host 身份、受控 runner、原始设置还原与读回校验；真正的输入仍必须通过前台/归属检查。相关系统 API 见 [Microsoft 高对比度参数文档](https://learn.microsoft.com/en-us/windows/win32/winauto/high-contrast-parameter)。CD 因这次 main CI 失败而跳过，并非独立的发布失败。
- [10 月 26 日 main CI](https://github.com/FelixJI/VibeTable/actions/runs/40255226840)的 S41 在工作区重开时等待 sidecar 就绪超时。该次关闭保护快照 551ms 完成，旧 sidecar 正常退出；新进程同步扫描 101 个文件，约 57 秒时仅完成 75 个，Host 在 60 秒截止。外部文件已经具有目标内容时，旧物化逻辑仍为新修订重复备份、复制和替换文件。本轮消除这段重复写入，同时保留仓储内容验证、同步索引、持久提交和回滚契约；不提前宣告就绪或放宽超时。优化后的托管 runner 表现仍需产品 E2E 实测。
- `required` 原先没有依赖 CodeQL，存在扫描失败但唯一必需门禁通过的缺口。本轮将 CodeQL 加入依赖与结果检查，失败、取消或跳过均拒绝通过；这一缺口由编排代码和失败传播契约测试确认，不作为上述原生焦点失败的归因。
- PR #539 首轮还暴露了 S04 JSON 往返场景的产品缺陷：导入与筛选均成功，但刷新布局时，共享表头映射遗漏了 JSON 的单个 `contains` 条件，输入框变空而权威筛选仍生效，随后退格无法清除实际筛选。Playwright trace 与真实 Tabulator 回归均确认这一状态不一致；修复只补充可无损往返的 JSON 文本条件映射，保留复杂分组、多选和其他字段条件的既有边界，不依赖延时或重跑。
- [PR #539 第二轮](https://github.com/FelixJI/VibeTable/actions/runs/40511704302)的 S35 再现关闭超时，并留下完整诊断：快照 gate 仅等待 16ms，随后 2,447 条审计记录逐条执行 SQLite `synchronous=FULL` 事务，append 耗时 27.630 秒、ack 耗时 2.282 秒，耗尽 30 秒 HTTP 预算；PB 连接池等待为零，尚未进入数据库快照。本轮让既有 256 条批次在一个 FULL 事务中提交，提交成功后才更新内存索引并逐条确认。INSERT、COMMIT 失败均整批回滚；取消或确认失败后的持久尾部通过现有事件去重重放，保持审计链和持久性契约。`AppendCalls` 记录实际批次调用数，`Appended` 仍记录新增条数。
- 同轮 S49 的新失败发生在 renderer 崩溃恢复检查：导航已恢复，但 `plugin.catalog.list` 尚未返回，测试立即断言动态应用入口可见。共享断言现在先等待该入口可见，再检查导航与入口；使用原有等待预算，缺失入口仍失败。它与首轮已修复的高对比度还原故障是不同原因。
- 同轮 .NET 的 `TryUseReadyClient_AdmissionPrecedesConcurrentShutdown` 在准入回调等待释放时失败。测试把持锁回调放在线程池中，编排又依赖线程池续体；程序集并行执行阻塞测试时，释放可能晚于原有 5 秒预算。修复用专用线程执行持锁窗口和有界同步等待，保留 Stop/Dispose 两种情形、200ms 内关闭的同步状态转换不能完成的断言，以及释放后的完整收尾；不改生产锁和超时。
- [10 月 9 日 CD](https://github.com/FelixJI/VibeTable/actions/runs/37965714045)失败在当时的 `--require-closed-evidence` 发布证据门禁。当前入口已改为检查证据文档的一致性；[0.5.6 的 CD](https://github.com/FelixJI/VibeTable/actions/runs/40315429196)已完整成功，约 9 分 26 秒，其中发布与镜像同步约 8 分钟。本轮不删除这些现有校验，也不通过重跑或放宽条件掩盖历史失败。

较早的 [S35 关闭超时](https://github.com/FelixJI/VibeTable/actions/runs/39903539985)只保留了 30 秒 HTTP 超时信息，诊断附件已不可用；本轮复现证明了审计落盘的瓶颈，但不足以追溯证明旧运行具有相同原因。另一次 [S41 请求捕获超时](https://github.com/FelixJI/VibeTable/actions/runs/39782966424)的现存日志也不足以定位，不计作本轮已验证修复。

文件扫描的同机临时探针使用真实 `workspacev2.Open`、Kopia 和持久协调器：保存 1 个原文件并关闭工作区，离线新增 100 个文件，再重开并同步完成扫描。计时仅覆盖 `StartBackgroundWorkers` 的同步扫描，不含 `Open`；旧实现为 17.695 秒，最终实现为 14.410 秒，均完整发现 101 个文件。各运行一次，约 18.6% 的改善只提供方向性证据，不能证明托管 runner 的 60 秒超时已彻底消除，也不能替代 S41 产品验收。

审计批量落盘的同机临时探针使用真实 FULL SQLite 与合成内存 outbox，串行比较 2,447 条记录：旧实现 2,447 次提交、0.930 秒，新实现 10 批提交、0.036 秒；两者均完整排空并验证既有审计链。机器并非独占，且该探针不包含真实 PB 确认及完整关闭路径，数值只用于验证瓶颈方向，不能代替 S35 产品 E2E 或承诺 CI 的绝对耗时。

### 早期基线

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

当时单次 CI 内有两类主要成本：

1. `prepare` 的 quality 与 `core` lane 重复执行 Python、Web、Go 和 .NET 质量检查；
2. `go-race` 单 lane 独占约 27 分钟，其他 lane 完成后仍需等待它。

## 分阶段方案

### 阶段一：消除同一次 CI 内的重复质量检查

保持公共 `scripts/automation.py`/`scripts/automation_core.py` 不变，只在 VibeTable prepare step 注入候选模式。项目 adapter 在该模式仅恢复 release build 需要的 uv、web-grid npm 与 .NET 依赖，并把质量检查延后到候选绑定的并行 lanes；`qa/release_eligibility.py` 声明的 24 个 `REQUIRED_STAGES` 保持不变。未设置候选模式的本地与串行入口继续执行完整 bootstrap/quality。

release build 已确认只依赖候选模式恢复的依赖，不消费 quality 生成物。PR #42 首轮 run `31102199882` 的 prepare 从历史约 14 分钟降至 4 分 55 秒，缩短约 9 分钟（约 65%）。

### 阶段二：按 Go package 拆分 race 关键路径

race worker 先输出结构化 `RACE_PACKAGE_TIMING`，记录 package、测试数和执行秒数。PR #42 首轮 run `31102199882` 测得整个 `go-race` 为 1669.547 秒，其中 `tests/integration` 单包为 1566.266 秒（99 项），占 stage 墙钟的 93.8%；因此纯 package 分片不能缩短关键路径。

此前采用四条 `race-a`/`race-b`/`race-c`/`race-d` lane，2026-10-27 扩为 `race-a` 至 `race-h` 八条。PR #521 run `40245371188` 的 `race-a` 记录 `internal/app` 为 3124.301 秒（331 项）、`internal/workspacev2` 为 800.191 秒（119 项）；旧 app 权重 41.922 秒已失真。这两个包与 `tests/integration` 都按编译后的真实命名测试分片。integration 当次 1276.169 秒只属于一个分片，因此保留此前完整包的 1566.266 秒权重，其余旧权重不变。

普通包先按实测秒数确定性贪心分配给唯一 lane，新包以权重 1 回退，不需要为无关包编译发现测试。三个拆分包在各 lane 只编译一次，并从同一 race 二进制的 `-test.list` 发现完整清单，再独立确定性分配命名测试并复用该二进制执行；三个已知长测保持较高权重。八条 lane 的命名测试集合互斥且并集完整，无分片的串行入口仍执行完整集合。无测试文件包允许不生成二进制；有二进制但没有命名测试时仍执行 TestMain，全部发现为空则失败。聚合器要求八份同候选身份的 race 报告全部成功后才合成唯一 `go-race` 证据。

早期双分片方案按首轮 49 个 package 的实测值模拟，两个分片的调度权重分别约为 1792.3 与 1794.5 秒；integration 的 99 项测试分为 48/51 项。该历史模拟不代表当前八分片的墙钟收益；本次以新 PR 的 Actions 实测为准。

同时使用固定 SHA 的 `actions/cache` 缓存声明版本的 `.tools/w64devkit`，只在八条 race lane 恢复/保存；通用 bootstrap 不再安装该 race 专属工具链，完整串行 smoke 仍在消费点显式安装。这里不添加无依据重试；任一真实 race 失败仍直接失败。早期双分片曾以 race 16 分钟、整体 CI 25 分钟为目标；该目标不作为当前八分片的已验证结论。

### 阶段三：再评估 setup 与缓存命中

用 Actions job 时间和项目报告检查 checkout、setup、`uv sync`、`npm ci`、Go module 与 .NET restore 的实际占比。只优化被数据证明占用显著的步骤；不为理论上的缓存 miss 增加额外校验层。

### 阶段四：候选 prepare 复用唯一的 release 构建

候选 prepare 阶段此前仍执行两轮完整四栈打包：`pr-e2e` 先跑非 release 的 `scripts/build_next.py` 再执行 workbench qualification 与 `release.smoke` product acceptance，`build` 随后又完整执行 `scripts/build_next.py --release` 并 archive、build identity、SBOM。现在候选模式下 `pr-e2e` 显式输出延期，`build` 在唯一一次 `--release` 构建成功后执行与独立 `pr-e2e` 完全相同的 workbench/product acceptance 旅程，全部通过才进入 `qa/release_candidate.py create`、build identity 与 SBOM；release 构建或任一 E2E 旅程失败都不会交接 archive。不引入缓存 marker 或版本 SHA 猜测旧包可复用，也不修改共享 core。未设置候选模式的入口行为不变：独立 `pr-e2e` 仍然构建加 E2E，独立 `build` 仍然是纯 release 构建加交接。同时 bootstrap 不再安装 race 专属的 w64devkit（core lane 不消费该工具链）；race lane 与串行 release smoke 在消费点安装保持不变。

当前证据（CI [run 39880277810](https://github.com/FelixJI/VibeTable/actions/runs/39880277810)）：本阶段合入前的基线为 `prepare` 20 分 17 秒、`core` 34 分 49 秒。本阶段的实际节省以新 PR 的 CI 运行为准，在取得新的 Actions 墙钟数据前不声称已节省的具体分钟数。

## PR CI、main CI 与 CD 是否重复

CD 本身没有重复 CI。它由成功的 `main` CI 唤起，下载同一 run、同一合并后 source SHA 的候选，然后进行 stage、attestation、Release 与镜像同步。

PR CI 与 `main` CI 的完整门禁确实重复，但职责不同：PR CI 决定是否允许合并，`main` CI 验证 squash 后的新提交并生成可发布候选。现阶段保留两者；跨 SHA 复用 PR 候选需要额外的 tree 等价与候选重绑定机制，复杂度和误用风险高于节省的时间。优先消除单次 run 内的重复并缩短 race 关键路径。

## 验收方式

- 修改前后各取至少三次成功 PR run，比较中位墙钟和各 job 耗时；
- 聚合报告仍包含全部 24 个 `REQUIRED_STAGES`，`required` 是唯一权威门禁；
- `main` CI 仍上传 `release-candidate`，普通 PR 不上传正式候选；
- CD 仍只消费成功 `main` CI 的候选，未新增构建或完整测试步骤；
- 不降低覆盖率，不跳过 E2E，不把失败改成 warning，不增加无依据重试。

## 门禁执行的一致性与失败收尾

race lane 使用 `go list -race` 枚举包，测试清单直接来自一次 `go test -c -race` 生成的二进制，包含 Test、带输出的 Example 和 Fuzz，与执行使用同一 build tags。此前普通构建发现的 `!race` 测试会在 race 二进制中空跑，未来仅 `race` 构建的测试也可能漏测；非 race 容量预算仍由普通 Go 测试和覆盖率阶段执行。完整测试集合、逐测试进程隔离与 race 检测保持不变。

QA 子进程超时后统一最多等待 5 秒排空输出，防止已退出根进程的后代仍持有管道时无限挂起；失败继续返回 124 并标记输出不完整；Windows 管道未关闭时可能无法取回输出，已有独立诊断文件仍保留。开发 pre-commit 的 Python 检查统一通过 `uv run --frozen --no-sync python`，避免系统 Python 版本与项目 3.14 不一致。
