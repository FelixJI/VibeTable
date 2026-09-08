# Workspace mutation 重放资格

本变更修复现有 REST mutation.apply 在 workspace v2 写门禁内的重放失败，独立于产品 RPC owner 迁移。基线为 main `38098da214a0fb33bb6df1fd0707b1ba0b4ac754`。

首次写入成功后，相同请求再次进入门禁，会准备新协调意图；旧实现尝试为同一 kind/key 再写 proof，违反已有唯一约束并返回 500。修复让已核验的只读重放返回仅匹配外层 kind/key 的内部信号，由既有协调器 abort 当前准备意图。Runtime 仅消费独立信号；abort 持久化失败形成的组合错误仍返回失败并保留恢复要求。嵌套不同操作、直接 v1 调用与正常新写入继续遵守原 proof 契约。

这里的无业务写入指业务行、审计、事件、幂等结果、proof 与已提交 mutation revision 不变；协调存储仍执行 prepare/abort，不声称完全没有文件写入。没有修改 owner、唯一约束、协调器核心状态机或 CI 门禁。

## 本地验证

复用已有 Go 1.27、C 编译器与模块/构建缓存，`CGO_ENABLED=1`、`GOFLAGS=-mod=readonly`；以下命令工作目录为 `sidecar`。

- 基线 `go test -race ./internal/app -run '^TestWorkspaceMutationReplay' -count=1 -timeout=5m`：RED，首次请求成功而重放返回 500，12.125 秒。
- 修复后相同命令：PASS，13.281 秒；覆盖完整 receipt 保留、业务/审计/事件/proof/revision 不变、后续新写入推进一次，以及 payload 冲突、取消和关闭门禁拒绝。
- 新增 `go test -race ./internal/app -run '^TestWorkspaceMutationReplaySerializesConcurrentSameKey$' -count=1 -timeout=5m`：PASS，7.267 秒；真实 REST handler 同键并发得到一次 applied、一次 replayed，之后重放不改变权威状态。首次命令路径错误导致 no tests to run，不计入通过证据。
- `go test -race ./internal/mutation ./internal/writecoordinator -count=1`：两包全部 PASS，1.823/2.231 秒。
- Runtime 独立信号、组合错误、执行中取消和既有 replica pending 保留的聚焦 race 测试：PASS，1.751 秒。
- Coordinator 真实 abort 持久化失败与重新打开恢复的聚焦 race 测试：PASS，1.836 秒；初次测试清理已主动关闭的 DB 报错已修正，仅修正测试清理。
- `go vet ./internal/app ./internal/mutation ./internal/workspacev2 ./internal/writecoordinator`：PASS。

日志保留于本工作树 `build/`，包括 baseline-red、green、concurrent-replay-corrected、runtime-signal、kernel-coordinator-race、business-replay-race-corrected 和 vet。

## 审查与证据边界

Standards 与 Spec 独立审查源码及原三份测试均为 0 个确定缺陷；并发测试及本文增量审查由两轴独立复核。尚未运行完整远端矩阵，fresh CI、squash merge 及 main CI/CD 均待完成。

真实 abort 失败在 coordinator 层注入，Runtime 层以组合错误验证不吞掉失败；未声称覆盖 REST 到真实 abort 失败再重启 Runtime 的完整链路。旧 token 排队语义未在新增用例直接重演，原 gate 实现未改变。

并发用例使用 httptest handler，并未覆盖网络传输；共同 start 只保证并发发起，不强制 gate 内重叠，不作为排队后旧 token、关闭或取消竞争的证明。
