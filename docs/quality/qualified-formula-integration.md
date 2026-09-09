# Formula 数值语义与编译缓存整合资格

## 整合边界

基于实际 main `cadf51533ed45c3793c8f75b21025375d4d3f633`，按已授权的“各 PR 单独通过 CI 后整合、在最新端点重新验证”策略，普通合入以下固定候选：

- [#308](https://github.com/FelixJI/VibeTable/pull/308)，head `ff9cb6f993bc63f56e2524b2b22c03cc16058f21`，[CI 34294151244](https://github.com/FelixJI/VibeTable/actions/runs/34294151244) 完整成功：数值边界与错误分类。
- [#309](https://github.com/FelixJI/VibeTable/pull/309)，head `4fff7d052e13c54c2c0389bc53a5282f917da568`，[CI 34294583652](https://github.com/FelixJI/VibeTable/actions/runs/34294583652) 完整成功：共享有界编译计划缓存。

整合源码为 `e60305aa3b184301d0b8f5d294d3290810030f21`，无合并冲突。两份原资格文档保留各自历史证据和失败，不能把原 PR CI 代作整合端点的门禁。数值 decorator 无可变执行状态，共享缓存保留完整草稿键、LRU/singleflight 和提交后失效语义；Standards/Spec 组合审查无确定问题。

## 本地验证

复用锁定的 Go 1.27.0、Node 24.19.0、.NET 10.0.400 和 uv 环境；依赖锁与 #309 一致，没有重建环境。日志均在 `build/qa/qualified-formula-integration/`。

- `go test ./internal/formula ./internal/fieldchange ./internal/schemaapi -count=1`：全部 PASS，1.452s / 2.550s / 1.006s，`core-normal.log`。
- `go vet ./internal/formula ./internal/fieldchange ./internal/schemaapi`：EXIT 0，`vet.log`。
- `go test -race ./internal/formula ./internal/fieldchange ./internal/schemaapi -count=1`：整体 FAIL，`core-race.log`。Formula 4.113s、FieldChange 30.491s 通过；SchemaAPI 的 `TestListIncludesNewlyCreatedEmptyTable` 在 `TempDir RemoveAll` 报目录非空，包结果 FAIL（3.730s）。没有重试或放宽清理断言，其他通过结果不替代这次失败。
- `uv run --frozen --no-sync python scripts/build_next.py`：完整构建 EXIT 0，没有 skip，`product-build.log`。
- `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 05-formula-lifecycle --scenario 12-backup-consistency`：实际 WPF/WebView2 2/2 PASS、0 skip，`product-e2e.log`，run `20260909T011659Z`。S05 为 6264ms / 7 断言，S12 为 16904ms / 22 断言。四组件 fresh，未启动替代浏览器，Node/Host exit 0；页面错误、异常 bridge、pending 均为 0。S12 有 1 条已确认预期失败。两场景退出后成员/后代进程均为 0，端口、owner lease 和最终清理通过。

产品报告：`build/qa/product-e2e/20260909T011659Z/product-e2e-report.json`。S05/S12 验证整合后的实际消费者流程；数值极限与缓存失效/并发的具体契约由 Go 测试承担，不夸大为所有 Formula UI 行为都已覆盖。

## 交付门禁与回滚

整合端点 fresh required、squash merge 和合并后 main CI/CD 尚待完成。保留现有完整 CI 与不支持格式零写入拒绝；0.5.0/N-1 不纳入当前开发验收。只有整合 PR 合并后才将原 PR 标为已吸收。

整体回滚以整合 PR 的 squash 为单位，恢复此前 Formula 数值/编译行为；原两项完整意图与固定 head 保留，便于审查和定位。此次不改变数据 authority、RPC owner 或用户数据格式。
