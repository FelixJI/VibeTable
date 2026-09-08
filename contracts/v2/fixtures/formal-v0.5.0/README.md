# v0.5.0 正式发布包产生的兼容性输入

这些文件是旧版 producer 输入，不是当前版本兼容性通过证明。当前 policy 继续保持
`pending/unverified` 和 disabled verification gate。Producer PR #246 已合并，policy revision 2 通过独立后续变更将 anchor 前移到其 squash commit，冻结本组输入。
`cases.expected` 是后续 consumer 必须实现和验证的目标，其中 format 1 工作区的 `migrate`
目前尚未实现。冻结不代表执行资格通过，后续仍需独立实现、验证 reader/import/零写入，再进行 promotion。

## 来源与采集

- 官方 [v0.5.0 Release](https://github.com/FelixJI/VibeTable/releases/tag/v0.5.0)，正式发布时间
  2026-08-07T01:48:10Z；采集日期 2026-09-06。
- 资产 `VibeTable-v0.5.0-win-x64.zip`，GitHub asset ID `504459521`，140658634 字节。
  复用已经下载和校验过的官方包；没有重建旧源码或重新下载发布附件。
- 官方 `build-identity.json` 绑定 source commit
  `9a3077b019eb94fba30761f21b6442fc7034fea8`。其 archive checksum 与 GitHub Release asset digest
  同为 `b4c865a8a4891958b49d43d029de531c892a828563bb9e5eaca05f85664d06bb`。
- 直接启动该包中的 `VibeTable.Next.exe`，通过其实际 WebView2 页面创建工作区、表、文本字段和记录。
  不使用开发页面或当前 writer 生成旧资产。原生保存框只用于选择导出目标。
- 使用临时、专用的 `V:` 驱动器映射作为旧版 workspace 路径，避免把个人目录写进持久化数据。
  归档中的 Kopia config 保留原始路径
  `V:\build\a3-producer\v050-public-path\host\local-data\workspaces\acb519c1-192b-49c8-8a53-bdf60de1eeaf`。
  consumer 必须处理真实旧路径契约；不能删 config、丢弃 WAL 或改写格式号冒充迁移。
  采集完成且宿主正常退出后已释放该映射。

工作区 UUID 为 `acb519c1-192b-49c8-8a53-bdf60de1eeaf`，表 `tbl_868b1d09f53e4e17a40b`
显示名为“历史中文数据”，字段 `fld_gvggy01mpgcrn8gn83k6`（物理列 `f_q6kpy06qxv3pp41hw6n2`）
显示名为“中文备注”。唯一记录 ID 为 `bxgn2yj9m5wdg31`，文本内容如下：

> 历史版本原始数据：中文、0、false、空白不混同。

这里只含一条文本记录，文字里的 0、false 不代表数值或布尔类型覆盖。

## 精确输入

| 文件 | 产生方式与用途 |
| --- | --- |
| `workspace.zip` | 在任何 snapshot.export 之前正常关闭旧宿主后，逐文件归档原始工作区；61 项，保留所有原始文件字节，包括 coordination 和 repository config；manifest format 1。 |
| `plain.vtsnapshot` | 重新打开同一旧工作区并核对记录，创建并选择手动快照，由旧 UI 明文导出。 |
| `passphrase.vtsnapshot` | 同一手动快照由旧 UI 使用口令导出。公开测试口令为 `vibetable-corpus-v050`。 |
| `truncated.vtsnapshot` | 唯一派生负样本：脚本去掉明文包末尾 22 字节 ZIP end-of-central-directory；不是旧版正常导出物。 |

两个正常快照对应 `8d848b54-b098-4b9d-a968-c53bcfa8f21e`；package 和内部 snapshot manifest
格式均为 2。旧导出 metadata 的 `writerVersion` 与 `minimumAppVersion` 均为 `2.0.0`，这是
旧协议原值，不能改成应用版本 `0.5.0`。应用来源由上面的正式 Release/source/asset 绑定。

生成器只派生负样本和 corpus 清单，不重写三个原始输入。沿用 corpus 既有 SHA-256 字节完整性
契约：producer 清单记录资产摘要，contract/package consumer 校验实际字节，不一致就失败；
历史不可原位改写另由独立 remote-main Git anchor 约束。

## 验证边界与复现

采集时旧宿主正常退出、所属进程组退出、端口及 lease 释放。工作区归档经逐文件字节比较和 ZIP
CRC 检查；工作区和明文包解压内容的 UTF-8/UTF-16 个人目录扫描未发现个人路径。
相邻 Go contract 检查两个旧快照的 metadata、解密后的逐项内容一致性及截断输入被 reader 拒绝。
这些检查不表示完整 workspace.open 迁移、snapshot.import、落盘零写入或打包运行时测试已通过。

```powershell
uv run python contracts/v2/generate_v050_corpus.py --check
uv run pytest tests/contract/test_workspace_compatibility_corpus.py tests/contract/test_workspace_version_policy.py --no-cov
# 在 sidecar 目录
 go test ./internal/snapshotpkg
```

若需重新采集，按上述旧版 UI 流程创建新的独立样本并追加新证据，禁止替换本组历史资产。
