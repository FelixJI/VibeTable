# 包自更新回滚失败观测

## 问题与证据边界

本变更基于 `a3ca78b9181a529d978f9fba46586fbb924ecada`，用于缩小下一次包自更新回滚失败的定位区间，并让 smoke 对严格确认的 worker 失败及时报 FAIL。它不修复或解释 `0x80070005` 的根因。

2026-09-10 Content 候选 `6e25b56570222e73066220d115f0ea512b600737` 的完整 build 已退出 1，失败于 updated-crash 回滚。保留现场的 worker error 只有 `System.IO.IOException` 和 `HResult=-2147024891`；journal 为 `rollbackFailed / UPDATE_ROLLBACK_IO_FAILED`，`resources` ledger 为 `isolatePlanned`，目标 resources 存在、failed-package/resources 不存在、backup/resources 存在。这只能将区间缩至递归属性检查及目录移动附近，不能确认具体拒绝路径或当时的持有者。

同一时间窗 Application 事件 1025 记录的是预期的 smoke `Environment.FailFast`，不是拒绝路径证据。早期另一次 Win32 1175 不属于本次失败。原 Content 工作树、失败现场、ACL 与进程配置未改动；本任务未重新运行原 smoke。

## 内部接口与受控输出

`UpdateRecoveryFailureEvidence.WriteOnce` 保持现有文件位置、CreateNew 和 best-effort 语义。rollback worker 增加可选 enum operation 与 owned entry 上下文；其他调用方仍只输出原两字段。诊断写入失败或文件已存在时不覆盖证据、不替换原异常。

worker error 新形状只包含 `exceptionType`、`hResult`、`operation`、`entry`。operation 来自封闭 enum；entry 仅允许 `resources`、`release.json`、`VibeTable.Next.exe`，非入口阶段为 null。不输出异常 Message/StackTrace、任意路径、命令、环境、token、nonce 或新摘要。

阶段包括准备恢复、读/写 ledger、移动形状检查、源属性检查、递归树检查、实际文件移动、实际目录移动、恢复包校验、receipt 完成。特别是 `ValidateMoveTree` 与 `MoveDirectory` 分别在对应调用前记录，因此下一次可区分递归遍历失败与目录移动失败。operation 是调用阶段，不表示精确失败的子路径；隔离/恢复方向继续结合现有 ledger 判断。

上下文只在本次 worker 内存中更新，不新增 journal 写入。恢复策略、owned entry 顺序、原异常实例、worker 替换次数、重试、等待预算与 receipt 状态机均保持。现有 checkpoint seam 增加移动阶段，仅供聚焦回归注入失败。

## smoke 失败身份绑定

等待循环在原成功 receipt 验证前读取最多 16 KiB 的固定 pending journal。只有 state 为 `rollbackFailed` 才进入失败证据校验；未终态、不可读或未能解析的文件继续原等待路径。

失败与成功共用 `_self_update_rollback_evidence_error` 的完整字段和身份校验：schema、目标/stage 路径、本次 token、版本、smoke 标记、已知 updater/updated PID、watchdog 身份、worker PID/启动时间、时间顺序、ownedGroupId、rollbackAttempt、已消费 nonce 和 worker replacement count。失败分支另需未完成的 rolledBackAtUtc、封闭错误码以及合法且有序的 owned ledger 前缀；不要求先观察到中间状态。它只生成 FAIL，不授权恢复动作或成功。

完整绑定的失败立即报告 `scenario/UPDATE_ROLLBACK_IO_FAILED` 或 `scenario/UPDATE_ROLLBACK_SHAPE_AMBIGUOUS`；声称失败但身份不完整的记录仅报告受控校验字段名，不能回显字段值。成功 receipt 文件名、三项 restored ledger、时间、readiness、进程归属以及 crash 证据校验保持，120 秒预算与 50 ms 轮询间隔不变。

## 验证

- `uv sync --frozen --group dev --offline`：使用本地缓存建立独立工作树环境。
- `uv run --no-sync python -m pytest tests/test_release_tooling.py -k reports_bound_worker_failure -q --no-cov`：旧实现 1 FAIL，原因是终态失败仍等待成功 receipt；实现后通过。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter FullyQualifiedName~WorkerFailureIdentifiesOwnedEntryAndOperation --verbosity quiet`：旧实现 2 FAIL；实现后 2 PASS，分别验证递归树和目录移动阶段、原异常实例、受控字段及隔离前磁盘形状。
- `uv run --no-sync python -m pytest tests/test_release_tooling.py -q --no-cov`：109 PASS。包含陈旧 token/路径/进程、部分 worker 身份、无效 attempt/nonce、非法 ledger/错误码类型拒绝，以及非终态保持等待。边界测试曾暴露列表错误码 TypeError（1 FAIL / 15 PASS），补严格字符串检查后整文件通过。
- `dotnet restore desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --locked-mode --verbosity quiet`：PASS。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~UpdateRollbackWorkerTests|FullyQualifiedName~PendingUpdateActivationJournalTests|FullyQualifiedName~UpdateRecoveryWatchdogTests' --verbosity quiet --logger 'trx;LogFileName=rollback-observation.trx' --results-directory build/qa/rollback-observation`：89 PASS、1 SKIP；`ActivationPointerLinkIsRejectedAndRetained` 因当前 Windows 环境不允许创建文件符号链接而 Inconclusive。不能记作 90 PASS。
- `uv run --no-sync python -m ruff format scripts/build_next.py tests/test_release_tooling.py`、`uv run --no-sync python -m ruff check scripts/build_next.py tests/test_release_tooling.py`：PASS。
- `uv run --no-sync python -m pyright scripts/build_next.py`：0 errors。

这些是源码局部验证，不是完整包、GUI、CI 或真实 updated-crash 资格。下一阶段由本候选正常 CI 取得新包证据；若拒绝再次出现，使用受控 operation 与现有 ledger 缩小调用区间，仍需同一失败时刻的文件操作/句柄证据才能判断具体路径和持有者。不据此猜测杀软或调整 ACL、延迟与重试。

## 激活完成与进程退出的终态竞态
PR330 CI 34439650206 的 prepare 报进程在激活完成前退出；远端证据未包含激活完成现场，因此未确认其具体根因。独立本地回归证明现有读取顺序存在竞态：先读取完成文件，再观察退出；若文件在两者之间写入且进程随即退出，会误拒绝有效完成。
回归使用真实临时 JSON 文件，在退出观察时写入完成记录。旧实现四种情形中2 FAIL/2 PASS（build/qa/activation-observation/red.log）：错拒合法终态且未对错误身份走完整校验。修复在观察退出后立即执行一次完整最终文件校验，不再查询进程、不sleep、不延长预算；缺失文件和错误身份仍拒绝，所有成功字段校验保留。
Ruff format/check PASS，tests/test_release_tooling.py 全文件112 PASS/3.15s（green.log）。首次Ruff发现参数集容器与复合assert规范问题，修正后通过。该本地RED/GREEN不替代远端失败归因，也不声称原目录移动Win5失败已修复。

## Journal 进程锁释放与路径检查的真实竞态

后续 S24 的新包构建（源码 `999e8427`）在普通 activation 阶段失败：watchdog 的 `.recovery-read-error.json` 记录 `System.IO.FileNotFoundException`／HResult `-2147024894`，pending 为 `rollbackFailed`／`UPDATE_ACTIVATION_INVALID`，只有 process evidence，没有 readiness/completion。其原失败现场保持不变；诊断文件没有具体文件或操作类别，因此本地复现不能代替该次失败的精确归因，也不能归因杀软或认定既有 Win5 回退问题已修复。

静态候选是锁生命周期而非 pointer 原子替换：旧 Acquire 在 File.Exists(lock) 后用 File.GetAttributes 检查 reparse；旧 Release 先关闭持有句柄，再删除同一路径。竞争者可已观察到路径，随后持有者删除它，使属性读取抛 FileNotFoundException。这不是现有 Win32 32/33 sharing/lock contention，不能扩大重试错误集合来掩盖。

先将既有获取／释放逻辑原样提取到内部 `UpdateActivationJournalLock`，所有 journal 读／写入口继续使用同一锁。唯一可选 checkpoint 在“已观察路径存在”处控制测试交错；生产不传 checkpoint。真实 FileStream 的持有者在该点 Dispose，竞争者继续走真实属性检查和独占打开，没有伪造异常、替换 File API 或读写 activation pointer。

- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter FullyQualifiedName~JournalLockSurvivesHolderReleaseAfterContenderObservedItsPath --logger "trx;LogFileName=lock-red.trx" --results-directory build/qa/update-journal-lock`：旧释放策略 **1 FAIL，19ms**；`red.log`／`lock-red.trx` 堆栈精确为 File.GetAttributes→RejectReparsePoint→Acquire。随后相同过滤的 `--no-build --no-restore` 再次 **1 FAIL**，`red-repeat.log`／`lock-red-repeat.trx`，证明交错稳定。最初插入测试时因换行 marker 不匹配而提前停止，未运行测试，不算 RED。
- 修复仅把普通释放改为 Dispose OS handle，保留空锁文件路径供后续进程复用。锁文件无 token、nonce 或 journal 内容；它的存在不代表被占用，独占 FileStream 才是锁。没有改 pointer 的 File.Replace、删除/receipt 转移、owned entry 恢复、权限、reparse 检查、5s/25ms 预算或可重试的 32/33 码。
- 相同测试以及整个 `PendingUpdateActivationJournalTests`：**24 PASS／1 SKIP／0 FAIL**，`green.log`／`lock-green.trx`。回归同时证明新持有者期间第二 FileStream 被 Win32 32 拒绝，释放后可再次获取；新增锁路径 junction 拒绝测试通过，未触碰其目标。旧 writer/reader 竞争、超时 fail closed、pointer/祖先 reparse 与 activation 清理测试保留。唯一 SKIP 是原 `ActivationPointerLinkIsRejectedAndRetained` 缺文件符号链接权限。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-build --no-restore --filter "FullyQualifiedName~UpdateRecoveryWatchdogTests|FullyQualifiedName~UpdateRollbackWorkerTests|FullyQualifiedName~PendingUpdateActivationJournalTests|FullyQualifiedName~UpdateActivationSettlementTests|FullyQualifiedName~ReleaseUpdateServiceTests|FullyQualifiedName~UpdateProcessCommandTests" --logger "trx;LogFileName=update-lock-related.trx" --results-directory build/qa/update-journal-lock`：**137 PASS／1 同上 SKIP／0 FAIL，7s**，`host-related.log`。
- `uv run --frozen --no-sync python -m pytest tests/test_release_tooling.py tests/test_release_eligibility.py --no-cov -q`：**125 PASS，2.39s**，`python-related.log`。随后按原 main 同步资格的精确集合执行 `uv run --frozen --no-sync python -m pytest tests/test_release_tooling.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_surface_python_oracle.py --no-cov -q`：**140 PASS，4.02s**，`python-140.log`。

本节日志均在 `build/qa/update-journal-lock/`。没有执行新包构建、GUI、S24 或远端写入；修复仍待 root 独立双轴和 fresh CI／新包资格，不覆盖此前任何构建失败。
