# 查询通知完成边界验证

Content PR326 的 CI34436657321/core102746741794 在原主干继承的 `RequestQuery_RejectsGroupedPagesFromDifferentRevisions` 失败：350ms墙钟等待后通知数仍为0（预期1），位置GridStateCoordinatorTests.cs:301。其余1116项Host测试通过，Go/Python门禁通过。这份日志仅证明通知未在测试任意等待窗口内到达，不证明生产拒绝逻辑错误。

本完整意图覆盖同一测试类的五个查询通知契约：权威dataset替换、单次有界窗口、opaque cursor续页、缺失revision拒绝、跨revision拒绝。它们改用已有ManualTimeProvider推进原250ms debounce；fake gateway返回已完成Task，原通知回调与分页调用在该推进/调用中完成。全部业务断言保留，生产代码、debounce预算及超时不变。取消/迟到响应已有独立信号测试保持。

首次本地转换留下同步方法中的await，编译失败，日志build/qa/query-notification/first-compile.log保留。修正后：

- `dotnet restore desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --locked-mode --verbosity quiet`：PASS。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter FullyQualifiedName~GridStateCoordinatorTests --verbosity quiet`：20 PASS、0 SKIP，46ms，日志build/qa/query-notification/coordinator-tests.log。

未重复构建产品包，测试专属改变不冒生产修复。独立双轴、fresh CI和合并后main CI/CD尚待执行。旧CI失败保持，不用增加sleep或重跑旧run改写它。
