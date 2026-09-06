# 旧 Workspace 离线迁移副本验证

`vibetable-pb --verify-legacy-workspace-migration` 是供 Host 迁移事务调用的 one-shot 命令。
它验证调用方已准备并独占的离线副本，在所有存储句柄关闭成功后输出目标 WorkspaceManifest JSON。
这不是产品 `workspace.open` 的迁移入口，也不证明 v0.5.0 已达到 N-1 verified。

## 调用责任

调用方负责在同 UUID 原工作区上持有 maintenance intent 和 writer fence，保留完整原件，
将数据库及其 WAL/SHM、repository、快照、审计和文件树完整复制到独立 staging。
传入的 dataDir 必须指向该副本的 `.vibetable/data`；不能传入正在使用的原目录或不受调用方控制的链接树。
本命令不建立副本，不获取 Host 的原目录互斥，不修改 registry，不发布目标格式。

输入沿用 sidecar 的可信环境契约：`VIBETABLE_SIDECAR_DATA_DIR`、`VIBETABLE_WORKSPACE_ID`、
`VIBETABLE_WORKSPACE_SESSION_EPOCH`、`VIBETABLE_WORKSPACE_FENCE_EPOCH`、`VIBETABLE_WORKSPACE_CLAIM_ID`
和会话凭据 `VIBETABLE_SIDECAR_SESSION_SECRET`。身份必须与副本内的原 authority 一致。
命令行 dataDir 覆盖、其他 one-shot 模式以及 replica/activity root 均拒绝。

## 验证与结果

当前实现接受 manifest format 1、direct/convenient、topology/business schema 1 的离线副本。
repository 格式和已有存储布局由 objectrepo 校验。缺失必需历史数据库时，在初始化前拒绝，
防止 runtime 将历史状态补成空库后误报成功。

目标 format 2 仅在内存中构造。副本执行正式 migrations 和完整 runtime 恢复，后台 worker 保持停止；
随后验证审计链、repository inventory、全部对象，以及每条快照的完整 bundle。
ordinary startup 的 format 2 准入检查不变，普通 replica 路径在 migrations 后仍重新读取 binding。

- 退出码 0：stdout 恰为一个通过校验的目标 WorkspaceManifest；所有 runtime、audit、PocketBase 关闭操作已成功。
- 退出码 1：执行或关闭失败；stderr 提供错误，不能消费 stdout 作为成功结果。
- 退出码 2：启动配置无效。

验证本身可能迁移、恢复或重定位副本内部存储，但始终保留磁盘 manifest 的 format 1。
收到成功结果后也不能直接把 staging 注册为当前工作区。Host 必须在独立的、可恢复的发布事务中处理
原件保留、目标 manifest 写入、目录安装、registry 落盘和中断恢复；只有该事务完成才能开放产品会话。
失败或进程中断后的副本由调用方保留诊断或丢弃，不能猜测其可用状态。

## 验证入口与范围

```text
go test -race ./internal/config ./cmd/vibetable-pb -run 'TestLegacyMigration|TestReplicaOneShotFailureKeepsStdoutEmpty' -count=1
go test -race ./internal/workspacev2 -run 'TestLegacyMigrationCopy|TestReplicaOneShotRevalidatesBindingAfterMigrationPreflight|TestWorkspaceRepositoryBoundaryHasNoConcreteEngineSurface' -count=1
```

CLI 测试启动实际子进程并执行与发布命令相同的 `run` 入口，使用已冻结正式 v0.5.0 ZIP 的完整副本。
它检查成功输出、失败退出、连续运行、原始文件集合与字节不变、磁盘 manifest 不发布，以及退出后的目录句柄释放。
库级回归另外覆盖中途 binding 变化、取消和必需历史存储缺失的零写入拒绝。

这些证据不覆盖 Host 的复制与发布崩溃恢复、产品业务写回、mirrored/protected 模式或 packaged `workspace.open`。
完整 A3 consumer 和 policy promotion 仍按 ADR 0011 及 PR #140 方案后续验收。