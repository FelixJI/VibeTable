# ADR 0004：对象仓库由自有深模块隔离

- 状态：已接受
- 日期：2026-07-28
- 决策者：VibeTable 产品与工程

## 背景

Snapshot、文件历史和副本需要共享去重对象，但业务层若直接依赖 Kopia pack、index 或对象类型，升级和恢复语义会被第三方实现细节绑死。

## 决策

- 业务调用者只使用 `WorkspaceRepository` 的 `Commit/Open/Verify/Pin/ReleasePin`。
- `ContentStore`、`ImmutableManifestStore`、`PublicationStore`、`RepositoryReplicator` 和 `RepositoryMaintenance` 是模块内部 seam。
- 生产 adapter 固定精确 Kopia 版本，只使用公共 API；测试使用 fault-injecting memory adapter。
- 公共契约只暴露 VibeTable object ID，不暴露 Kopia ID。
- publish 只有在对象 flush、reopen/verify 和 durable receipt 成功后发生。

## 加密语义

`none` 不宣称保密；`convenient` 固定公开口令 `password`；`protected` 使用随机密钥和 Windows Credential Manager，并要求用户保存标准恢复密钥。
