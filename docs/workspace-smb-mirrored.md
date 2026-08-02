# SMB 镜像工作区安全边界

## 用户可见行为

- 直接模式只允许 Windows 报告为 `Fixed` 且具备 strong coordination 的本机磁盘。
- SMB、WebDAV 等网络位置都属于非固定存储。若对这类位置请求直接模式，宿主返回
  `workspace.storage_requires_mirrored`，界面提示改用镜像模式，而不是按盘符或路径字符串拒绝。
- 镜像模式只把本机固定磁盘上的 activity root 作为可写工作区；所选 SMB 根仅承载可独立重开的
  恢复副本。
- WebDAV 和其他非 SMB 网络协议返回 `workspace.network_protocol_unsupported`，不会随 SMB 一起
  放开。

## 协议识别

路径分类不匹配 `C:\\`、UNC 前缀、盘符或 `DavWWWRoot` 字符串。探针在实际文件句柄上调用
Windows `GetFileInformationByHandleEx(FileRemoteProtocolInfo)`，仅将
`WNNC_NET_SMB (0x00020000)` 识别为 SMB。无法识别的远端协议一律 fail-closed。

## 发布门禁

`contracts/v2/provider-support.json` 必须为 network provider 显式声明 `protocol: smb`。只有
`creation` 被受保护的发布流程改为 `enabled` 时，Desktop 才向 Web 宣告
`workspace.storage.mirrored-create.v2`。

`hardware.smb-v1` evidence 必须绑定当前 commit、源码树、候选包哈希和受保护签名，并包含以下全部
通过阶段：

1. `protocol-identification`
2. `durable-write-rename-readback`
3. `immutable-no-replace-publish`
4. `disconnect-reconnect-recovery`
5. `independent-reopen-root-verification`

其中 immutable publish 必须覆盖 sidecar 使用的“完整临时文件 + 原子 no-replace hard link”语义，
确认并发不同内容只有一个发布者成功且最终文件不会被覆盖。实验还需在断线、重连和进程独立重开后，
仅依赖 SMB 根验证恢复闭包中的所有对象。

在这些 evidence 尚未生成并通过受保护签名之前，矩阵保持 `blockedPendingLab`，UI 不提供镜像创建
选项；这不是路径识别失败，而是发布资格尚未满足。
