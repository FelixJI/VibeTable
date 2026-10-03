# 质量与发布门

查看执行顺序：

```powershell
.\.venv\Scripts\python.exe qa\next.py --list
```

完整 CI 必须输出发布身份摘要：

```powershell
.\.venv\Scripts\python.exe qa\next.py --ci `
  --package-root dist\VibeTable.Next `
  --package-archive dist\VibeTable-v0.2.0-win-x64.zip `
  --json-report .qa-next-summary.json
```

摘要只有在以下条件全部满足时才会标记 `releaseEligible: true`：

- 使用 `--ci` 跑完全部阶段，且所有阶段返回码为零；
- 执行期间 Git commit、四组 handoff artifact hashes 与发布源码
  `sourceHash` 没有变化；
- 摘要包含生成时间、当前 commit、artifact hashes、source hash 和逐阶段结果。

当 `--package-root` 的发布布局包含随包 `kopia.exe` 与 `age.exe` 时，完整门禁会
把其绝对路径分别注入 `VIBETABLE_KOPIA_CLI` 和 `VIBETABLE_AGE_CLI`，供普通
Go test 与 Go race 的官方 CLI 互操作测试使用。Windows release gate 还必须设置
`VIBETABLE_TEST_WINDOWS_CREDENTIAL_MANAGER=1`；因此恢复工具和 Credential
Manager 测试不会以“环境未配置”为由静默跳过。

`qa/handoff.py record <STAGE>` 会 fail-closed 校验上述摘要：必须成功、24
小时内生成，并且精确绑定当前 commit、artifact hashes 与 release source
hash。source hash 覆盖 backend、contracts、desktop/WPF、web grid、QA、
scripts、sidecar、全套 tests/E2E 及根依赖锁文件，同时排除
`node_modules/bin/obj/dist/build` 等生成目录。`--no-gate` 仅用于
生成不可发布的诊断 handoff；其 `releaseEligible` 为 `false`，后继阶段
`verify` 会拒绝它。

## Go race

Windows 上的 Go race detector 需要启用 cgo，并使用包含
`libsynchronization.a` 的新版 MinGW-w64。门禁优先查找：

- `.tools/w64devkit/bin/gcc.exe`
- `.tools/w64devkit/w64devkit/bin/gcc.exe`

否则使用 `PATH` 中的 `gcc`。没有合格编译器时必须失败，不能把 race 标成
跳过。当前验证基线是 w64devkit 2.10.0 x64（GCC 16.2.0），下载包 SHA-256：
`18d0a4c71a166f8401ab6305781bec5882b40b5e06ba9807c61cb5f3b3c6325e`。
Go 阶段只隔离 `GOTMPDIR`，不会覆盖调用者的 `GOCACHE`；CI 因而复用
`setup-go` 恢复的默认 build cache，本地显式设置的缓存路径也会原样继承。

PocketBase 的每个集成测试 app 会启动文件系统 watcher。为避免单一测试进程
累计 watcher 并触发 Go 的 10 分钟测试超时，race 门会：

1. 动态枚举 `go list ./...` 返回的全部包及其源码目录；
2. 对每个包动态枚举 `Test`、`Example` 与默认执行的 `Fuzz` seed；
3. 每个包只用 `go test -c -race` 编译一次；同一 lane 内只执行一个包，
   避免其他包的编译/执行挤占公式墙钟预算；两条 race lane 仍在独立 runner 并行；
4. Windows 上每个命名测试使用编译后 race 二进制的独立进程（仍为
   `-test.count=1 -test.parallel=1`），包括 migrations 与 integration，避免
   PocketBase 异步 watcher 与同一测试进程中的后续测试互相影响；包完成后立即
   删除其临时测试二进制；
5. 没有命名测试的包仍单独执行 `go test -race`；
6. 将 1,000/10,000/25,000 行压力测试各自放入独立进程，并给予更长但有界的
   超时；
7. 任一批次失败、发生数据竞争、超时或枚举到零测试时立即失败。

这只是隔离测试进程资源，不会关闭 race detector，也不会忽略任何测试。
Windows 偶发的 PocketBase watcher 与 Go `TempDir` 删除竞争只允许对完全相同的
race 命令最多重试两次；识别条件严格限定为 `testing.go` 的 “directory is not
empty” 清理诊断。出现 `WARNING: DATA RACE`、panic、业务断言或第三次仍失败时
一律失败。

历史基线：2026-08-04 的同机验证中，默认 Go cache 加三个 package worker 的完整 race 阶段
耗时 815.219 秒（13.59 分钟），相对历史两个 worker 的 994.422 秒下降 18.02%。
本次覆盖 46 个有测试包、575 个当前源码中的命名测试和 3 个无命名测试包；历史
报告来自不同源码版本，测试数量不可直接做增减比较，该次变更本身未删除测试。

当前默认每 lane 一个 worker。一次固定四逻辑 CPU 的同二进制对照中，301 行求值从串行约 31ms 增至三进程并发约 44–52ms。串行包调度隔离无关进程竞争，代价是 lane 可能变慢；该对照没有复现 CI 失败，串行调度后的 CI 仍发生集合公式超时。

产品默认单条求值保护期限为 250ms，含 TABLE 来源读取和递归计算。301 行完整来源与同表 DAG fanout 测试在普通 Go CI 使用此默认预算；race 构建仅对这两个功能场景通过既有 `Limits` 注入有限 1s 预算。依据 [Go 官方 race 开销说明](https://go.dev/doc/articles/race_detector#Runtime_Overhead) 的插桩运行时开销，插桩正确性测试与普通构建的产品预算验证分别执行；数据规模、结果、依赖、freshness 和收敛断言不变，cost/内存预算不变。应用 compiler 和递归来源读取共享同一注入预算，更早的父 deadline 仍优先。

250ms 是覆盖来源 I/O 与调度的有限保护期限，不是性能 SLO；本机完整 S37 预览冷热样本用于校准，原标量 p95 ≤100ms 检查保持。资源错误保留 `formula.resource_limit`，详情区分 deadline、cancelled、cost，并标识最内层失败公式字段；物化字节和递归上限独立保留。预览只在一次请求内复用来源 schema，后续请求重新读取权威版本。

formula 包在普通和 race CI 均验证真实默认 250ms deadline 会传给来源 reader、到期会取消且不能被 IFERROR 吞掉；使用 Go 标准库 `testing/synctest` 的虚拟时钟精确检查默认、自定义与更早父 deadline 和取消结果，避免插桩或调度消耗测试时间预算。其余取消、cost、内存契约和全部 race 测试继续运行，最终仍以完整 CI 为准。

## Fault injection

`qa/fault_injection.py` 默认包含命名 Go 故障测试、精确一个 .NET sidecar
恢复测试，以及真实 WPF/WebView2 场景。`.NET` 的执行数量从 TRX 的 counters
读取，必须恰好 `total=1, executed=1, passed=1, failed=0, error=0`，不会依赖
易变的控制台文本。所有子进程都有明确超时；超时会终止整个进程树并写入失败
报告。

`--component-only` 只适用于开发诊断，发布 CI 不得使用。

## A1 自然老化验收

这是一项人工、跨 24 小时的真实 WPF/WebView2 验收，不进入默认 CI。对同一已构建候选先运行：

```powershell
uv run --frozen --no-sync python qa/retention_natural_aging.py seed --package-root dist\VibeTable.Next
```

seed 通过真实 UI 创建内容不同的两个快照、公开核实其状态，并把状态仅写到
`build/qa/natural-retention-aging/state.json`。正常退出后等待至少 24 小时，再对同一候选运行：

```powershell
uv run --frozen --no-sync python qa/retention_natural_aging.py resume --package-root dist\VibeTable.Next
```

不要复制或移动该目录的工作区/`local-data`，不要修改数据库或系统时钟。resume 会在启动 Host
前拒绝过早、候选不一致、状态路径越界或 workspace UUID 不一致的 checkpoint；失败状态不会重写
checkpoint。证据在 `build/qa/natural-retention-aging/evidence/`，成功后保留其供 PR 人工复核。

失败的 seed 保留原目录与证据；可在同一 QA 根创建独立重试，例如
`--state build/qa/natural-retention-aging/attempt-3/state.json`。随后 resume 必须传入同一 `--state`，
不得迁移其工作区或 `local-data`。

## 发布包检查

`qa/package_check.py` 无参数时检查源码与提交的发布布局。传入发布目录时还会
检查 sidecar 二进制、执行权限、SHA-256、迁移、构建信息、许可证、
CycloneDX SBOM，以及安装目录与可变数据隔离策略。发布布局禁止旧提供方运行
时、Node/npm 或 `node_modules`。

provider gate 校验打包后的支持矩阵与源码一致。SMB network、registered cloud、用户标记同步
目录和 removable provider 都使用同一 advisory 目录副本实现；程序只验证并读写用户选择的
目录，不推断云端上传状态或设备生命周期。实现依赖不可变 no-replace 发布、独立 reopen、
checkpoint SHA-256 和冲突 heads 防御断线、部分写入、并发发布与自然损坏，不把摘要表达为
发布者认证。

正式安装器生成与签名、Windows SmartScreen/杀毒软件验证、全新用户安装/升级/
卸载 UI、跨版本真实数据恢复和断电/磁盘满注入仍需在发布环境保留独立证据。
