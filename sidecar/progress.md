# 附件历史版本实施进度

## 2026-07-24

- 已向主代理回报 WP05 审查结论与最后一项黑盒测试状态。
- 已确认新任务边界和 Go 工具链绝对路径。
- 正在盘点现有 attachment/audit/backup/migration seam。
- 已完成 seam 盘点：选用内部 collection 的受保护 FileField 保存历史 blob，使 manifest、文件与 mutation 同事务。
- 已新增版本 collection 与归档/恢复基础方法；attachments 单元包通过编译。集成测试首次被默认 Go build cache ACL 阻断，已切换到可写 cache。
- 已接通 audit preview/apply，并在生产 app 注入同一 attachment manager。
- 真实 PB 集成已覆盖替换、清空、硬删后的恢复、归档故障回滚、restore 故障回滚、历史损坏拒绝、版本/文件孤儿、backup zip 包含版本 blob；该定向测试通过。
- 定向 attachment/audit/app/backup/migration/integration 全绿，`go vet ./...` 全绿。
- 全量 `go test ./...` 的附件相关包与命令黑盒全绿；唯一剩余失败为未修改的 relation_junction M2A allowlist 用例，已向主代理单独上报。
