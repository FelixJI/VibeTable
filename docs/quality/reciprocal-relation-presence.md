# 反向关联存在标记资格

真实 WebView2 场景在创建关联后观察到：源端有目标链接，目标端物理值已写入源链接，但对应 presence companion 为 false，导致 QueryPage 与 ProductRow 正确按缺失值投影。旧集成测试仅检查 GetStringSlice，未暴露该错误。Python REST 与 Go Product 共用同一 mutation kernel，owner 切换不会修复此缺陷。

本修复从 main f7c0ad57 独立交付，在原反向同步事务内复用 fieldvalue.NormalizeWrite 的 PhysicalValues，同时写入主值与存在标记。many 移除最后链接保持显式空数组，one 清空为 null；遵循既有 required/selection 约束，失败仍回滚。未修改 owner、查询投影、迁移/恢复或 CI 门禁，也不自动修复既有历史坏 presence。

本地复用 Go 1.27、CGO 与模块/构建缓存，工作目录 sidecar：

- 既有关系 fixture 扩展 one/many，并检查 ProductRow、真实 QueryPage、receipt.ChangeSetID 关联审计 after 及 EmittedEvents 的目标变更通知。基线 RED：新增链接投影为 null、many 清空也错误为 null；修复后 GREEN 1.142s。
- `go test -race ./tests/integration -run '^TestRelation(DisplayFieldBlocksTargetLifecycleChange|Pair)' -count=1 -timeout=5m`：PASS，14.194s。
- `go test -race ./internal/mutation -count=1`：PASS，1.865s。
- `go vet ./internal/mutation ./tests/integration`：PASS；git diff --check：PASS。

日志为本工作树 build/reciprocal-presence-*.log。Standards / Spec 独立审查各 0 个确定问题。required/selection 拒绝未新增用例，回滚回归沿用既有物理链接断言；不将这些边界扩大为已完整验证。当前 fresh CI、squash merge、main CI/CD 尚待完成，关联编辑新界面的真实包资格由其功能分支继续验证。