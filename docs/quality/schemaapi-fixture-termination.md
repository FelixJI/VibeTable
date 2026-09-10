# SchemaAPI 测试夹具终止资格

当前 main cadf51533ed45c3793c8f75b21025375d4d3f633 的 TestListIncludesNewlyCreatedEmptyTable 仅调用 ResetBootstrapState。锁定 PocketBase v0.40.1 的该方法停止 Cron 并关闭数据库；日志 ticker 的停止和日志 flush 在 OnTerminate 钩子中，因此原夹具没有完成该生命周期契约。

仅将此测试的初始化/清理提为本文件 helper，按已合并 schemaProductStore 的模式同步触发 OnTerminate，并在链尾 ResetBootstrapState。保留业务断言与 TempDir 原清理；未引入睡眠、重试、忽略错误、生产变更或全仓夹具改造。

新增子测试在夹具 cleanup 完成后检查：终止 hook 确实调用、调用时数据库仍 bootstrap、结束后已经 reset。原清理逻辑确定 FAIL（called=false / bootstrapped=false），不依赖等待目录故障复现。

复用现有 Go 1.27.0 / CGO / 模块和构建缓存。在 sidecar 执行：

- `go test ./internal/schemaapi -run '^TestSchemaLifecycleStoreTerminatesBeforeReset$' -count=1 -v`：旧清理 RED，0.919s。
- `go test -race ./internal/schemaapi -count=1 -v`：修复后全包 PASS，9.056s；原业务测试2.93s，新终止测试4.42s。
- `go vet ./internal/schemaapi`：EXIT 0。gofmt 与 git diff --check 通过。

日志保留在 build/qa/schemaapi-fixture-termination/{lifecycle-red,lifecycle-green-race,vet}.log。Standards 与独立 Spec 审查无确定问题。此证据证明终止顺序补齐，不证明之前全部 Windows TempDir 目录非空失败均由它引起；各原 FAIL 记录仍有效。本片不涉及产品行为，未重建产品；当前 PR fresh CI 和合并后 main CI/CD 仍待完成。
