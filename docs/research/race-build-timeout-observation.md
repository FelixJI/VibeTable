# Race 编译超时观测

## 已有故障与证据边界

main `58032b97043c2bba80a8eb1f65ae2906797e3251` 的 Actions run `34430547220`、job `102727648194`：race-b 报告返回 124，总耗时 817.55 秒。首批 `internal/app`、`tests/integration`、`internal/filehistory` 的 `go test -c -race` 各约 420 秒超时，31 包随后停止，没有进入 race 测试二进制执行。报告按提交顺序聚合，因此首个打印的 app 不代表最早失败的进程；最终日志打印时间不是命令开始时间。

本变更提供下一次正常 CI 的诊断证据，不声称修复超时，不将编译超时归因为运行期竞态、TempDir 清理或某个依赖。

## 最小接口与输出

`qa/next.py` 仅在 race 编译调用向 `_run_command` 传 `race_build=True`。发现测试、普通测试和其它 stage 不采集进程树。编译仍使用 3 worker、420 秒超时及原失败传播；测试集合、required、已有编译后测试隔离逻辑不变。

每条编译命令保留原命令记录，附 `RACE_BUILD` JSON 行：

- `started`：成功创建子进程后的 UTC 时间、PID、相对耗时；启动失败为 `start_failed` 和原返回码 127。
- `timeout`：捕获 420 秒超时的 UTC 时间、相对耗时，以及随后采集的 `snapshot` 和 `snapshotSeconds`。
- `finished`：进程结束/终止完成时间、返回码及输出字符数。超时仍为 124，普通编译错误保留原非零值。

只有 `go test -c -race` 增加 `-x`，让尾部指出编译/链接/cgo 的最后步骤。报告每个输出流最多保留 16384 字符，并记录截取前字符数；这是报告大小限制，底层仍沿用 `communicate` 收集输出，并非流式内存限制。输出保存在现有 lane report，不新增外部工件或 hash。

`qa/race_build_process_snapshot.ps1` 只查询目标 PID 及逐层后代的 PID、父 PID、名称、创建时间、累计 CPU 秒和 working set。不会查询或输出命令行、环境、用户、可执行路径；不枚举无关系统进程。最多输出 64 个进程，超出标记 `truncated`。根进程已消失时返回 `root_missing`，不据此声称没有遗留后代。

采集在终止前执行，最多额外 5 秒；这段取证时间单列，420 秒判定和失败结果不变。CIM 不可用、超时、输出异常均记录缺口，然后继续原终止流程。非 Windows 明确为 `unsupported_platform`。不增加重试或新依赖。

单次 CPU 累计值不能证明当前 CPU 繁忙；树查询也不是原子快照，可能漏掉已经退出、重设父关系或查询期间产生的子进程。现阶段没有线程栈/等待原因证据，不能仅凭这些字段断言死锁或资源争用。`-x` 尾部与进程名称用于决定下一步最小诊断。

## 本地验证

复用已有 uv 环境，未运行 Go 构建或 CI。

- 新直接回归旧实现 RED：3 failed，缺少诊断 helper；覆盖成功/失败返回、超时先采集再 kill、采集失败保留 124。
- Windows 小进程验证一度因虚拟环境启动器增加合法后代而失败；修正测试为验证每个进程的父链都通向指定根，仍排除测试自身这个无关进程，不削弱树隔离断言。
- `uv run --frozen --no-sync python -m pytest tests/test_race_build_diagnostics.py tests/test_next_gate.py -q -o addopts=`：82 passed，3.02 秒。包括真实 Windows 父子进程、根退出后缺口、普通命令不采集、有界编译尾部，以及 compile timeout 停止该包、不执行测试。
- `uv run --frozen --no-sync python -m ruff check qa/next.py tests/test_next_gate.py tests/test_race_build_diagnostics.py`：PASS；对应 Ruff format 与 `git diff --check`：PASS。

本地结果只验证观测和门禁行为。需正常可审查 PR 的真实 CI 才能取得原问题的构建步骤和进程状态，当前不作 CI 或包资格放行。

额外类型检查：`uv run --frozen --no-sync python -m pyright --pythonpath <既有uv环境解释器> qa/next.py` 返回 EXIT 1，18 条错误均位于既有 `Popen(**popen_kwargs)` 的 `dict[str, object]` 展开。把 `58032b9:qa/next.py` 原文件只读导出到固定 build 证据目录的唯一文件，使用相同项目配置和解释器得到相同 18 条错误；没有将该扩展检查记为通过，也没有降低类型规则或混入基线修复。

## 复审修正：根退出但后代持有输出管道

复审指出原超时分支在终止后调用无期限 `communicate()`，而根已经退出时既有终止函数直接返回。后代持有 stdout/stderr 管道会让报告无限等待，之前内存中的观测无法交回。

修正只作用于 race 编译超时：

- 先在 `build/qa/race-tests/timeouts/` 创建唯一命名文件并关闭文件句柄，保存结构化阶段、PID、时间及白名单进程字段。没有保存原始 stdout/stderr，没有新增 CI 日志打印或上传。写入失败明确记录 `save_failed`，不覆盖旧证据或改变 124。
- 根进程已退出时记录 `root_exited`，不再按历史 PID 采集，也不追杀后代或可能复用的 PID。活根仍走已有进程树终止流程。
- race 超时后的 `communicate(timeout=5)` 到期记录 `drain_timeout` 和输出可能不完整，保留已知尾部并返回 124。不会关闭可能被读线程锁住的管道，避免把无限等待转移到 close；已存在的读线程可能在后代关闭管道前继续存活。
- 本地落盘证据在 kill/drain 之前可读取；CI 可达性依靠有界返回后既有 lane report 的结构化事件，本次不声称新增本地文件会被 artifact 上传。

受控回归在旧 `1b2da7da` 上 RED：1 failed，明确在无界 drain 调用处发现证据尚未保存。修正验证了退出根不触发 PID 查询/杀进程、5 秒 drain 参数、124 返回、缺口及阶段证据可达；另验证活根终止前文件已存在。保留 3 worker、420 秒及原测试集合。

修正后最终验证：`uv run --frozen --no-sync python -m pytest tests/test_race_build_diagnostics.py tests/test_next_gate.py -q -o addopts=` 为 **84 passed，3.02 秒**；Ruff check 和 `git diff --check` PASS。没有重跑 CI、Go 编译或完整构建。

## PR330：分离收集语义与诊断预算

PR330 head `f7c9d532127d4d105e1b3f69206e3f11c818e897` 的 CI `34443408744` / core job `102766444544` 在 `tests/test_race_build_diagnostics.py::test_windows_snapshot_contains_only_requested_tree` 第143行失败，经 `qa/next.py:621` 的 `subprocess.run` 抛出 `TimeoutExpired`：PowerShell 执行同一采集脚本、目标 PID 3820，5秒到期。Python 汇总为1 failed、1870 passed；该阶段停止，不能宣称此次 CI 已通过后续 .NET 门禁。日志仅证明启动、CIM查询、输出整体调用超时，不能细分原因或归因为杀软、资源争用、Go运行期竞态。

原5秒是终止前诊断的额外预算，既有契约明确允许采集超时后记录缺口。真实进程树功能测试却把这个预算同时当成每次必须成功的性能门槛。此次只调整测试：

- 真实 Windows 测试直接执行原 `.ps1`，每次调用采用独立30秒上限，非零退出、超时或非法 JSON 均失败；仍严格验证 `captured`、根与子进程、每个进程的完整父链、无关进程排除、精确白名单字段及根退出后的 `root_missing`。子进程通过专属 stdin 等待父进程清理，不再与30秒采集预算共用30秒存活期限。
- wrapper 契约通过受控 `subprocess.run` 核对精确脚本/根PID参数、5秒预算与输出捕获，验证成功解析及同一个 `TimeoutExpired` 向上抛出，每次只调用一次。
- 原采集失败回归保留 `OSError` 并增加 `TimeoutExpired`，穿过真实 wrapper 到 `_run_command`；读取kill前已经关闭写入的证据文件，验证 `collection_failed` 及具体异常类型，随后终止并返回124，报告保留完整事件顺序。

生产 `qa/next.py`、采集脚本、Go420秒预算、3 worker、完整测试集合及 required 均未修改；未添加重试、吞超时或 Windows 跳过逻辑。原非Windows平台分流保持。

验证复用已有锁定 uv 环境并将导入路径绑定当前 worktree，没有安装依赖：

- `uv run --frozen --no-sync python -m ruff format tests/test_race_build_diagnostics.py` 与 `uv run --frozen --no-sync python -m ruff check tests/test_race_build_diagnostics.py`：PASS。
- `uv run --frozen --no-sync python -m pytest tests/test_race_build_diagnostics.py tests/test_next_gate.py -q -o addopts= --durations=5`：87 passed、0 skipped，3.28秒；真实 Windows 树检查2.30秒。日志：`build/qa/query-notification/snapshot-budget-tests.log`。
- `uv run --frozen --no-sync python scripts/automation_project.py python-quality` 首次 EXIT1：2 failed、1871 passed、1 skipped，97.08秒，覆盖率91.42%。两个失败分别为 `test_node_runner_inventory_matches_the_product_scenario_manifest` 与 `test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime`，都是该 worktree 缺少 Web 依赖引发的 `ERR_MODULE_NOT_FOUND`（Vue compiler、Playwright），日志 `build/qa/query-notification/snapshot-budget-python-quality.log` 保留。主clone package/lock不同而未复用；改用与当前两个清单完全一致的已有worktree依赖目录建立本地junction，无依赖/lock/源码变更。
- 修正依赖可达性后同一完整 Python 质量入口 EXIT0：Ruff format/check、backend Pyright/mypy均通过，1873 passed、1 skipped，75.93秒，覆盖率91.42%。唯一跳过是既有 `test_packaged_baseline_rejects_resolved_checksum_alias` 的符号链接平台条件，本次真实 Windows 快照测试通过。日志 `build/qa/query-notification/snapshot-budget-python-quality-ready.log`；固定Node24.19由仓库既有toolchain入口恢复，未改pin或来源。

PR326 新 CI `34443371974` / core `102766143315` 再次在相同的 `GridStateCoordinatorTests.cs:301` 得到通知数0/预期1（Desktop 1 failed、1116 passed）。PR330既有 `7e3119f3` 已覆盖该等待边界：手动推进原250ms timer，已完成fake Task使revision拒绝通知在推进返回前完成，原业务断言不变。这与本次PowerShell诊断超时是两个故障。`5ff28ac0` 的界面迁移及 `7f440d90` 的激活锁修复未改相关路径。

本次不重复运行未受影响的 .NET/Go/Web 完整构建、产品包或产品E2E；没有重跑旧CI。当前增量的独立双轴审查、最新主干同步及fresh完整CI仍待主代理执行，本地结果不代替required。
