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
`creation: enabled` 时，Desktop 才向 Web 宣告 `workspace.storage.mirrored-create.v2`；当前矩阵已
正式启用 SMB advisory 镜像。registered cloud、用户标记同步目录和 removable provider 仍保持
阻断，不能借网络路径分类绕过。

SMB 镜像的有效性由实际数据路径和自动化回归约束，而不是由本地认证文件证明：

1. 仅接受句柄探测为 SMB 的 network provider；
2. checkpoint 先完整写入临时文件、同步，再以 no-replace hard link 发布；
3. 同一路径的不同并发内容最多一个发布成功，既有内容绝不覆盖；
4. publication 使用规范载荷 SHA-256、父节点存在性和 checkpoint digest 发现自然损坏/部分写入；
5. 断线只留下本地 pending queue，重连后幂等继续，不回滚已提交的本地工作；
6. 多设备并发发布保留多个 DAG heads，进入显式冲突流程，不伪装成排他锁；
7. 验证和恢复必须重新打开 SMB 根并读取完整恢复闭包，不能依赖 activity root 或本地 DAG 数据库。

这些约束防御的是本地办公软件实际面对的断线、部分写入、并发发布和自然损坏。镜像内容本身不加密，
也不把同机密钥包装成远端发布来源认证；需要机密性时应依赖受支持的工作区加密模式和组织存储策略。
