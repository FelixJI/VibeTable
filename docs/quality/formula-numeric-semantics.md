# CEL v1 数值边界资格

状态：独立本地修复已通过相关测试和双轴审查；真实打包验证已通过；fresh PR CI 和合并闭环待完成。

## 完整意图

旧实现只在最终数值结果检查非有限值，比较和字符串转换可掩盖中间溢出；只要表达式包含除法，又会把其他运算产生的 Infinity 误报为除零。另有 float32 非有限输入绕过检查，以及浮点数 2^63 转 int64 时边界判断错误。

在运算返回处拒绝非有限结果，double 零除产生既有 `formula.divide_by_zero`，其他非有限结果产生 `formula.overflow`；整数绑定保留有效 int64 区间。int/uint 除法仍消费原 Divider，短路、取消、预算和作者 source map 保持原契约。普通数仍为 binary64，没有引入 Decimal。

本 PR 只修改 compiler、plan 与相邻语义回归；共享编译缓存、事务失效和 owner 变更分别交付。

## 验证证据

基线 `main@3bf03bc6f47399bacbdb1305a9b110c8d3b11ae5`，复用 Go 1.27.0 与原 module/build 缓存，`GOFLAGS=-mod=readonly`；未改变依赖或门禁。以下 Go 命令在 `sidecar` 执行：

- 旧实现 `go test ./internal/formula -run '^TestCELV1' -count=1`：13 个数值子例 FAIL，0.707s，日志 `build/qa/formula-numeric-semantics/red.log`。
- 修复后 `go test ./internal/formula -count=1`：完整包 PASS1.047s，包含既有作者和位置映射测试，日志 `green.log`。
- `go vet ./internal/formula ./internal/fieldchange ./internal/schemaapi`：PASS，日志 `vet.log`。
- `go test -race ./internal/formula ./internal/fieldchange ./internal/schemaapi -count=1`：分别 PASS2.812s、21.185s、4.633s，日志 `race.log`。
- `go test -race ./internal/app ./tests/integration -run '^TestFormula|^TestFieldSettingsDescribeProductHTTPReadsRealAuthorityWithoutWrites$' -count=1`：app PASS5.135s、integration PASS83.240s，日志 `consumers-race.log`；覆盖真实公式应用、作者与重算消费者。
- Standards 与独立 Spec 审查未发现确定问题。golden 覆盖数值错误边界，并固定空值、Unicode、UTC、关联聚合、编译错误及 cost limit 的既有行为。

上述不代表完整发布矩阵通过；本源码的真实产品证据见下节；fresh required 待取得，不能用其他 PR 的候选证据替代。
## 当前源码的真实打包验证

源码提交 `0a4bb2dc` 执行 `uv run --frozen --no-sync python scripts/build_next.py` 完整构建 PASS，没有 skip 参数，复用已验证 lock 一致的环境。日志 `build/qa/formula-numeric-semantics/product-build.log`。

随后执行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 05-formula-lifecycle --scenario 12-backup-consistency`：run `20260908T234507Z`，2/2 PASS、0 skip。S05 5.661s/7断言，S12 17.729s/22断言；S12 包含真实公式字段计算及当前格式快照恢复。它们验证产品消费者回归，数值边界错误由前述 Go golden 直接证明，不能将这两场景称为完整 Formula 产品纵切。

四组件 fresh；实际连接桌面 WebView2，`browserLaunchCalled=false`。两个场景 Node/Host退出0，pageErrors、异常 bridge failures、pending均0；S12另有1条已确认的预期 `history.field_not_found`。进程成员/后代为空，端口释放、owner lease和最终清理全部通过。原始报告 `build/qa/product-e2e/20260908T234507Z/product-e2e-report.json`，入口日志 `build/qa/formula-numeric-semantics/product-e2e.log`。

完整发布矩阵、fresh required 和合并后 CI/CD 仍待完成。
