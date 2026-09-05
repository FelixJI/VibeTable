# VibeTable 工作区恢复指南

VibeTable 2 的工作区以稳定 UUID 标识。不要通过创建同名目录来“修复”离线工作区；请在 Workspace Center 中选择“重新定位”，并让应用核对 `.vibetable/workspace.json` 中的 UUID。

## 恢复优先顺序

1. 停止使用疑似损坏的工作区，避免覆盖健康副本。
2. 在“设置 → 版本与恢复”检查 Snapshot 的完整性状态。
3. 优先“打开为新工作区”验证内容；确认无误后再决定是否恢复原工作区。
4. 恢复原工作区前会创建保护 Snapshot、关闭写入并写入 durable restore journal。中断后重新启动 VibeTable，应用会继续完成或回滚，不应手工删除 journal/staging。
5. 镜像工作区先确认副本 UUID、lease/fencing 状态与 pending sync。provisional 会话不能直接覆盖远端。

## `.vtsnapshot` 包

- 未加密包是公开 ZIP64 容器，可用普通 ZIP 工具列出；`manifest.json` 的 SHA-256 只证明内容完整性，不证明来源。
- age 外封装包使用官方 age CLI 格式。请将解密结果写入受控临时目录，完成后安全删除明文。
- “恢复原工作区”还要求 workspace key MAC。没有正确密钥的第三方包只能作为不受信输入检查或导入为新工作区。
- 导入会在写入工作区前拒绝路径穿越、重复 entry、符号链接、超资源包、缺失对象和清单篡改。
- 导入为新工作区时保留业务内容、源审计记录和已完成后台批次，但使用新的工作区身份与写入会话。业务单元格中保存的旧 UUID 文本不会被替换。源包仍有未归档审计或无法证明已提交的写入时，导入会失败；请从原工作区重新导出完整快照，不要手工删除包内回执。

## 密钥模式

- 不加密：无保密能力。
- 便捷加密：固定公开口令 `password`，仅防格式误读，不能抵抗有意访问。
- 受保护：密钥保存在 Windows 凭据库；请离线保存恢复密钥。丢失凭据与恢复密钥后无法解密。

## 随包恢复工具

离线发布包已包含由仓库内 `tools/recovery-tools/go.mod`/`go.sum` 固定源码构建的 `sidecar/tools/kopia.exe`、`sidecar/tools/age.exe` 和 `sidecar/tools/age-keygen.exe`，无需恢复时再联网安装。每个工具旁都有 `.sha256` 文件；`sidecar/recovery-tools.provenance.json` 还记录模块校验和、Go 工具链、Windows amd64/CGO 目标和二进制 SHA-256，`sidecar/sbom.cdx.json` 以产物 component 再次绑定这些信息。使用前应先核对校验和。工具不会写入系统 `PATH`，也不会替换应用内的恢复编排。

例如，可在发布包根目录执行以下命令，将 age 外封装解密为标准 ZIP64 `.vtsnapshot`；口令模式会在终端提示输入口令：

```powershell
.\sidecar\tools\age.exe --decrypt --output recovered.vtsnapshot exported.vtsnapshot.age
```

受保护模式使用恢复密钥时：

```powershell
.\sidecar\tools\age.exe --decrypt --identity recovery-key.txt --output recovered.vtsnapshot exported.vtsnapshot.age
```

Kopia CLI 仅用于高级仓库检查/恢复。请先复制整个工作区或在只读副本上操作，使用与 `.vibetable/workspace.json` 对应的仓库配置和密钥；不要直接对仍在运行的工作区执行 maintenance。

## 完整性与审计

发现 repository index、对象或 Snapshot 损坏时，VibeTable 会停止 GC 和覆盖性同步，并优先尝试从健康副本修复。AuditLedger 不随业务数据恢复而倒退；本机 hash chain 可发现锚定范围内的篡改或缺失，尾部截断需要远端或导出 Snapshot anchor 才能发现。

需要人工处理时，请保留 `.vibetable/audit`、`.vibetable/snapshots`、`.vibetable/coordination`、restore journal、应用版本和 QA 报告，不要直接改写它们。
