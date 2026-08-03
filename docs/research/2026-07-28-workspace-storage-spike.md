# Workspace storage spike：Kopia、age、SQLite 与 provider

状态：代码级 façade 与可执行互操作门禁已落地；Kopia/age 已作为固定 Go 依赖安装，并已从源码构建官方 CLI 完成互操作验证。早期将 SMB、Cloud Files、可移动盘和双设备实验室作为 provider 启用门槛的假设，已被统一的“用户选择目录”边界取代。

## 固定依赖

精确版本记录在 `tools/workspace-storage-dependencies.json`：

- Kopia `v0.23.1`，Apache-2.0。只允许公共 CLI 和公共 `repo.RepositoryWriter` 表面，不导入 `internal/`。
- age `v1.3.1`，BSD-3-Clause。包加密使用官方 Go API；发布包同时携带官方 CLI，以官方文件格式作为互操作和恢复边界。
- modernc SQLite `v1.54.0`，BSD-3-Clause，已经在 sidecar 锁文件中。

## 威胁模型

- `none` 无保密能力。
- `convenient` 使用公开固定口令 `password`，只防格式误读，不能抵抗有意访问。
- `protected` 的仓库密码保存在 Windows Credential Manager，另发高熵恢复密钥；丢失两者将无法恢复。
- SHA-256 清单和 checkpoint digest 检测意外损坏、截断和部分写入，不证明发布者身份，也不防止人为伪造。
- 目录镜像只承诺所选目录中的恢复副本可验证、可重开；不承诺云端已经上传、可移动设备在线或同步软件按时运行。

## 已自动验证

- memory/fault 与 filesystem repository adapter 的内容去重、fencing、flush/reopen、publication 原子性、损坏/缺失检测和 root pin。
- SQLite 固定读事务：写入并发发生后，读事务仍看到 barrier 时的稳定视图；新事务看到新提交。
- Snapshot package 的 ZIP64、逐 entry SHA-256、路径穿越、重复 entry、符号链接和资源限制。
- direct/mirrored 布局、fixed/network/removable/user-marked/registered cloud nested root 分类，以及 storage move/release-cache plan 的 stale 校验。
- Kopia façade 写入的仓库可由固定 `kopia v0.23.1` CLI 重开并读取 manifest。
- age 原生 API 与固定 `age v1.3.1` CLI 双向加解密互操作。

## 专用环境验证的定位

SMB 断连、Cloud Files 占位符、可移动盘拔出和双设备长期离线仍适合做兼容性与故障注入测试，但不再作为独立 provider 或“远端真实性”门禁。失败应形成具体的文件系统兼容限制或恢复提示，而不是引入云 SDK、热插拔监听、硬件证据合同或分布式租约。
