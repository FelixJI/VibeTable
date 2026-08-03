# Workspace storage spike：Kopia、age、SQLite 与 provider

状态：代码级 façade 与可执行互操作门禁已落地；Kopia/age 已作为固定 Go 依赖安装，并已从源码构建官方 CLI 完成互操作验证。当前环境没有 SMB、Cloud Files、可移动盘和双设备实验室，因此这些硬件环境结论不得标记为已验证。

## 固定依赖

精确版本记录在 `tools/workspace-storage-dependencies.json`：

- Kopia `v0.23.1`，Apache-2.0。只允许公共 CLI 和公共 `repo.RepositoryWriter` 表面，不导入 `internal/`。
- age `v1.3.1`，BSD-3-Clause。包加密使用官方 Go API；发布包同时携带官方 CLI，以官方文件格式作为互操作和恢复边界。
- modernc SQLite `v1.54.0`，BSD-3-Clause，已经在 sidecar 锁文件中。

## 威胁模型

- `none` 无保密能力。
- `convenient` 使用公开固定口令 `password`，只防格式误读，不能抵抗有意访问。
- `protected` 的仓库密码保存在 Windows Credential Manager，另发高熵恢复密钥；丢失两者将无法恢复。
- 未加密 ZIP 的 SHA-256 清单只证明内容完整性，不证明来源。恢复原工作区还必须校验 workspace key MAC；第三方包始终是不受信输入。
- 本机 AuditLedger hash chain 只能发现已锚定范围内的篡改或缺失。尾部截断需要远端或导出 Snapshot anchor。

## 已自动验证

- memory/fault 与 filesystem repository adapter 的内容去重、fencing、flush/reopen、publication 原子性、损坏/缺失检测和 root pin。
- SQLite 固定读事务：写入并发发生后，读事务仍看到 barrier 时的稳定视图；新事务看到新提交。
- Snapshot package 的 ZIP64、逐 entry SHA-256、路径穿越、重复 entry、符号链接和资源限制。
- direct/mirrored 布局、fixed/network/removable/user-marked/registered cloud nested root 分类，以及 storage move/release-cache plan 的 stale 校验。
- Kopia façade 写入的仓库可由固定 `kopia v0.23.1` CLI 重开并读取 manifest。
- age 原生 API 与固定 `age v1.3.1` CLI 双向加解密互操作。

## 发布前必须在专用环境验证

| 环境 | 必须证明 | 失败处置 |
|---|---|---|
| SMB | rename/flush、断连、重连 UUID、advisory lease candidate 可发现 | 阻断该 provider |
| Cloud Files | 嵌套根识别、placeholder hydration、按需文件、冲突副本 | 阻断该 provider |
| 可移动盘 | 突然拔盘、本机保存不回滚、pending sync、重连核验 UUID | 阻断该 provider |
| 双设备 | strong fencing、advisory DAG、时钟漂移、长期离线、接管 | 阻断镜像默认开启 |

每条证据需要记录 commit、依赖版本、stage/oracle、超时、日志、sourceHash 和 artifactHashes；仅有单元测试不等同于硬件验证。
