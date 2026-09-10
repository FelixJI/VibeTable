# Provisional 副本操作准入资格

基于 main `cadf51533ed45c3793c8f75b21025375d4d3f633`，恢复旧 S24 半成品中独立的准入修复意图。当前 Conflict Center 会提交 `replica.forceTakeover` 和 `conflict.apply`，但 Host 默认写入门禁将所有 Writable=false 会话拒绝，导致 provisional 会话不能到达既有 Go 权威操作。

仅在当前 workspace scope 和 epoch lease 存在、会话为 OpenedProvisional / Provisional / Idle 且非 writable 时，允许这两项操作继续进入既有 capability 和 Go 权威校验。普通写入、只读会话与过期/转换中会话仍拒绝；不改变 owner、claim、冻结计划、receipt 或 Go 状态机。

旧提交 ba646da15 的两个文件迁入最新 main 时，保留主干新增的取消、迟到响应、epoch lease 完成与 takeover 后 RefreshNowAsync 行为。测试夹具新增显式 CaptureCurrentSession 模式；旧 lease 注入模式仍为默认，既有生命周期测试保持原语义。

## 本地证据

复用锁定 .NET 10.0.400 与既有 NuGet 缓存，`dotnet restore desktop/VibeTable.Desktop.sln --locked-mode` EXIT 0；没有变更 lock 或依赖。日志位于 `build/qa/provisional-replica-admission/`。

- `admission-red.log`：仅生产文件使用当前 main 原码，运行新增 takeover 成功与 conflict 拒绝透传两项，2/2 FAIL；原码将两操作挡为 workspace.read_only。测试结束恢复候选源码。
- `admission-green.log`：`dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~WorkspaceProductControllerInterfaceTests|FullyQualifiedName~WorkspaceSessionEnvelopeFilterTests' --logger 'console;verbosity=normal'`：69/69 PASS，0.7016s；包括原有 epoch 生命周期测试及四项新拒绝边界。
- `solution.log`：`dotnet test desktop/VibeTable.Desktop.sln --configuration Release --no-restore --logger 'console;verbosity=minimal'`：1338 PASS、1 既有 skip。Desktop 1102 PASS/1 skip，Infrastructure 128、Contracts 51、Workspace 27、DocumentDiff 17、PreviewHost 13 PASS。此命令不声称覆盖率门禁通过。

本片仅证明 Host 准入及既有 Go 拒绝透传，没有证明 conflict.apply 成功后的双端恢复闭环。旧 S24 分支尚无当前产品资格，不能据本片将其标为完成。尚未执行本候选完整产品构建、实际产品 E2E、fresh CI、squash 或合并后 main CI/CD；这些状态保持待完成。
