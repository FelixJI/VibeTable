# 质量与发布门

查看执行顺序：

```powershell
.\.venv\Scripts\python.exe qa\next.py --list
```

完整 CI 必须输出发布身份摘要：

```powershell
.\.venv\Scripts\python.exe qa\next.py --ci `
  --package-root dist\VibeTable.Next `
  --package-archive dist\VibeTable.Next.zip `
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
跳过。当前验证基线是 w64devkit 2.8.0 x64（GCC 16.1.0），下载包 SHA-256：
`6252bf34fe2231a55ac7f03d482b36d2c7c58697990551bba508102cfb3f342e`。

PocketBase 的每个集成测试 app 会启动文件系统 watcher。为避免单一测试进程
累计 watcher 并触发 Go 的 10 分钟测试超时，race 门会：

1. 动态枚举 `go list ./...` 返回的全部包；
2. 对每个包动态枚举 `Test`、`Example` 与默认执行的 `Fuzz` seed；
3. Windows 上每个命名测试使用独立进程（仍为 `-race -count=1`），包括
   migrations 与 integration，避免 PocketBase 异步 watcher 与同一测试二进制
   中后续测试的 `TempDir` 清理相互干扰；没有命名测试的包也会单独执行
   `go test -race`；
4. 将 1,000/10,000/25,000 行压力测试各自放入独立进程，并给予更长但有界的
   超时；
5. 任一批次失败、发生数据竞争、超时或枚举到零测试时立即失败。

这只是隔离测试进程资源，不会关闭 race detector，也不会忽略任何测试。
Windows 偶发的 PocketBase watcher 与 Go `TempDir` 删除竞争只允许对完全相同的
race 命令重试一次；识别条件严格限定为 `testing.go` 的 “directory is not
empty” 清理诊断。出现 `WARNING: DATA RACE`、panic、业务断言或第二次仍失败时
一律失败。

## Fault injection

`qa/fault_injection.py` 默认包含命名 Go 故障测试、精确一个 .NET sidecar
恢复测试，以及真实 WPF/WebView2 场景。`.NET` 的执行数量从 TRX 的 counters
读取，必须恰好 `total=1, executed=1, passed=1, failed=0, error=0`，不会依赖
易变的控制台文本。所有子进程都有明确超时；超时会终止整个进程树并写入失败
报告。

`--component-only` 只适用于开发诊断，发布 CI 不得使用。

## 发布包检查

`qa/package_check.py` 无参数时检查源码与提交的发布布局。传入发布目录时还会
检查 sidecar 二进制、执行权限、SHA-256、迁移、构建信息、许可证、
CycloneDX SBOM，以及安装目录与可变数据隔离策略。发布布局禁止旧提供方运行
时、Node/npm 或 `node_modules`。

启用任一非 fixed provider 时，必须同时传入 `--package-archive`。provider lab
证据的 `sourceHash` 会按 handoff 的 release source identity 重算（排除
`qa/provider-evidence` 本身以避免自引用），`artifactHashes` 必须精确等于发布
候选的 package tree hash 和 ZIP SHA-256。每份
`<evidence-id>.json` 还必须带同目录
`<evidence-id>.attestation.json`，其中记录固定的 Go、Kopia、age、SQLite
版本和证据文件 SHA-256，并以 HMAC-SHA256 签名。release Environment 需要提供：

- secret `PROVIDER_EVIDENCE_HMAC_KEY`：硬件实验室与受保护发布门共享的高熵密钥；
- variable `PROVIDER_EVIDENCE_KEY_ID`：当前受信密钥标识。

工作流只在受保护的 `release` Environment 中把它们映射到校验器环境变量。
证据过期、源码或候选产物变化、版本缺失、key id 不匹配、密钥缺失或签名不可信
都会 fail closed。非 fixed provider 仍为 `blockedPendingLab` 时不要求伪造证据。

正式安装器生成与签名、Windows SmartScreen/杀毒软件验证、全新用户安装/升级/
卸载 UI、跨版本真实数据恢复和断电/磁盘满注入仍需在发布环境保留独立证据。
