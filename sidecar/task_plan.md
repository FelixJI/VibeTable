# 附件历史版本闭环计划

## 目标

在托管附件被替换、清空或硬删除前归档历史二进制与 metadata；历史预览验证可恢复性，恢复应用通过同一 MutationKernel；完整性与备份覆盖历史版本引用及孤儿。

## 阶段

- [x] 恢复共享工作树上下文并确认边界
- [x] 盘点 attachment/audit/migration/backup 当前模型和事务 seam
- [x] 增加版本归档 collection、二进制命名空间和故障安全
- [x] 接通历史 preview/apply 与 MutationKernel
- [x] 扩展 integrity/backup 覆盖
- [x] 增加单元、真实 PB 集成与故障测试
- [x] 定向测试、全量 Go 测试、vet 与独立 diff 审查

## 约束

- 不修改 MainWindow、Python、scripts。
- 共享工作树中其他代理的改动必须保留。
- 恢复写入必须仍由 MutationKernel 提交。

## 错误记录

| 错误 | 次数 | 处理 |
|---|---:|---|
| 系统 PATH 不含 go/gofmt | 1 | 改用仓库 `.tools/go-full/go/bin` 的绝对路径 |
| 默认 Go build cache 位于不可写的 AppData | 1 | 后续显式设置仓库内 `.codex-go-cache` |
| 声明可写的 `C:/tmp` 实际 ACL 拒绝创建 cache | 1 | 改用仓库内 `.codex-go-cache`/`.codex-go-tmp` |
| 修改 migration 后 manifest checksum 失配 | 1 | 重算 2026072402 SHA-256 并更新 manifest |
| Windows/PB 临时目录清理偶发“directory is not empty” | 1 | 与断言无关；更新 checksum 后分包复跑并最终全量复核 |
| 全量测试后续稳定失败于 relation_junction 的既有 M2A allowlist 断言 | 1 | 单独复跑确认与附件无关并已通知主代理；不越权修改 relation |
