# 稳定化台账

> 实施基线：`GitHub/main@bd06158e`（2026-08-08）；证据截止 2026-08-09。本台账只记录可复现
> 缺陷、已关闭根因和仍待证的明确边界，不以静态搜索把未知问题伪装为 bug。

## 使用规则

- S0/S1 只在有最小复现、失败证据或已确认用户影响时登记为已知缺陷。
- 无法复现、仅缺少产品级证据的项目进入“待证实”，不占用 S0/S1 结论。
- 每项必须有所属模块、测试 seam、状态和目标阶段。能力可见性决定见
  [能力闭环矩阵](capability-matrix.md)；当前 E2E 场景与 selector tag 声明见
  [产品 E2E 能力索引](product-e2e-capability-index.md)，实际通过状态以对应 `required` 报告为准。

## 已知可复现缺陷

当前无未处置且已复现的 S0/S1。

| ID | 现象与最小复现 | 影响/严重度 | 模块与 seam | 状态/阶段 |
|---|---|---|---|---|
| S-01 | Windows 深路径下 plugin retain 的临时包未使用 extended path，真实场景安装报 `FileNotFoundError`。 | plugin 安装不可用 / S1 | `plugin_package_lifecycle.py`；深路径 retain 回归 | **已修复**；临时包全流程统一 filesystem path，场景 11 通过。 |
| S-02 | workspace drain 的 `sourceEpoch` 在 Go 为 string、C# 解析为 `UInt64`，切换时旧 runtime 无法停止。 | workspace switch 不可用 / S1 | `WorkspaceV2HttpGateway` 四语言契约 | **已修复**；统一 string，场景 15 通过。 |
| S-03 | Dashboard commit 未经过 workspace v2 coordinated business write/receipt。 | v2 workspace 保存返回 423 / S1 | metadata route/service + process test | **已修复**；同事务 receipt，场景 16 通过。 |
| S-04 | Snapshot open-as-new 的 ACL、final-root probe、`trusted` shape 与空 history recovery 分别阻断真实导入。 | Snapshot package/open-as-new 不可用 / S1 | broker、provider policy、Go package/filehistory tests | **已修复**；损坏包拒绝及有效 open-as-new/import 均在场景 15 通过。 |
| S-05 | Document adapter 使用私有 sequence，且 stale error code 不符合公开协议命名。 | 文档导入/diff 被高水位拒绝或错误映射 / S1 | Host epoch lease、Go public envelope、scenario 14 | **已修复**；统一 lease sequence 与合法 `filehistory.*` code，场景 14 通过。 |
| S-06 | 缺失 workspace 最终根被 probe 预创建，Windows 删除可见性窗口导致 `create_target_not_empty`。 | 创建 workspace 偶发失败 / S1 | `WorkspaceProviderPolicy` missing-target probe | **已修复**；改在现存父目录探测，聚焦三轮及最终 16 场景通过。 |

## 待证实验收空白

| ID | 证据来源与待确认事项 | 影响判断 | 模块与建议测试 seam | 状态/阶段 |
|---|---|---|---|---|
| V-01 | workspace relink 需要可控 unhealthy root；本轮真实产品矩阵覆盖 create/open/switch，但未覆盖 relink。 | 待证实；不声明 S1。 | Workspace Center + test-mode root picker。 | **已收口为 Hidden**；UI 隐藏且 renderer raw request 拒绝，恢复开发后以新纵切重新公开。 |
| V-02 | mirrored workspace 的 `replica.synchronize` 未在打包产品中运行；direct 模式只证明该入口不会误显示。 | 待证实；不声明 S1。 | provider policy、protection UI、mirrored workspace 产品场景。 | **已收口为 Internal only**；仅保留 Host coordination consumer，UI/raw renderer 不公开。 |
| V-03 | Dashboard 生命周期已闭环，但双真实编辑器的 revision conflict 无确定性产品 seam。 | 待证实；不声明 S1。 | dashboard coordinated write + 第二 renderer/editor。 | 生命周期 Closed；conflict 仅相邻集成/组件证据。 |
| V-08 | 打包产品已证明正常退出及 sidecar 异常恢复，但未覆盖 BFF 异常、关闭到托盘、托盘退出和静默开机启动。 | 阶段 5 生命周期证据不完整。 | WPF test-mode lifecycle controls + packaged runner。 | **已关闭**；真实 Host 精确终止 sidecar/BFF 后分别完成自动恢复与 workspace 关闭/重开恢复；托盘/静默报告均 code 0、无后代或监听端口残留。 |
| V-06 | 本机 Go 1.25.8 全包门禁随机在已清空的 `t.TempDir` 返回 Windows `directory is not empty`；三轮 fresh-process 的失败测试/目录均变化，业务断言通过。 | 本地 release_smoke 被提前阻断；没有产品数据错误证据。 | Go runtime Windows delete-pending / 外部文件句柄；锁定复现输出见本次质量报告。 | **外部/基线阻断**；不增加 sleep/retry、不改生产关闭语义，交由 GitHub `required` 的干净 runner 判定。 |
| V-04 | 旧架构扫描遍历仓库根并依赖静态 ignored 列表；本地笔记可能触发 retired-provider 误报。 | S3 维护噪音；无产品影响。 | `tests/test_architecture.py`；受控根/非受控本地笔记回归。 | 已修复；阶段 0。 |
| V-05 | `docs/quality-gates.md` 曾称普通 PR 不运行 Go race，与 CI 的 `race-a`/`race-b` lanes 冲突。 | S3 文档漂移。 | `.ci/project.json`、`.github/workflows/ci.yml`、质量文档。 | 已修复；阶段 0。 |

## 阶段 0 决策记录

- Workspace create/open/switch、Snapshot、FileHistory/Conflict、Dashboard 生命周期与 Document Diff
  已由 2026-08-09 的打包产品报告闭环；Retention、plugin 与其他条件能力的精确子路径状态见能力矩阵。
- Preset/version、workspace relink 与 plugin 成功 lifecycle mutation 保持 Hidden；Replica synchronize
  保持 Internal only；Dashboard 双编辑器 conflict 不借相邻证据扩大结论。
- 阶段 0 不新增或移除 provider/data authority，不以未验证项触发功能重构。

## 历史候选验收基线（2026-08-09）

- `build/q/f2/20260809T152446Z/product-e2e-report.json` 为 16/16 passed；场景 10 由真实 Host
  精确终止 `vibetable-pb.exe` 后自动恢复，再精确终止 `vibetable-backend.exe`，通过 workspace
  关闭/重开取得新 epoch。旧 epoch 的 `retention.update` 返回 `workspace.session_stale` 且策略未改变，
  新 epoch 的 mutation 成功；全部场景 Host exit code 为 0、无后代进程且端口释放。
- 当前程序处于开发阶段，不再构建历史候选或把跨版本 workspace 升级作为发布门禁；发布资产继续
  沿用既有 checksum 契约。
