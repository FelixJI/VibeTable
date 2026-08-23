# 质量门禁

VibeTable 是仅支持 Windows 10/11 的桌面产品，同时包含 Python、Vue/TypeScript、Go 和 Windows/.NET。所有 GitHub 门禁统一在 Windows runner 上执行；单一必需检查覆盖从静态质量到真实发布构建/产品 smoke 的完整资格链，避免只测前端和 Python 就误判为可发布。

## PR 与 main 必需门禁

`.github/workflows/ci.yml` 在 `pull_request` 和 `main` push 上运行单一 `required` job。workflow 只负责固定工具链和编排入口；项目阶段由 `.ci/project.json` 与 `scripts/automation_project.py` 定义，并依次 fail closed：

| 阶段 | 覆盖 |
|---|---|
| `bootstrap` | 锁定 Python、Node、Go 与 .NET 依赖及项目所需工具 |
| `quality` | 版本/包契约、Ruff、Pyright、mypy、Python 覆盖率、四个 Node 项目、Go format/vet/test/build、.NET tests |
| `e2e` | 当前无独立命令；产品级 E2E 由 release smoke 统一执行 |
| `release_build` | 构建真实 Windows x64 离线发布包及 identity/checksum/SBOM |
| `release_smoke` | 执行 `qa/next.py --ci`，覆盖 Go race、sidecar、升级/故障注入、WPF/WebView2 E2E、包契约和最终只读 smoke |

CI 使用 `windows-latest` 与最小 `contents: read` 权限。PR 的同编号陈旧运行可以取消，`main` 运行使用唯一 run id，绝不互相取消。PR 会完整执行 release build/smoke，但不会上传正式候选；只有 `main` push 会把与 source SHA 绑定的固定名 `release-candidate` 保留 14 天。完整矩阵以严格分支保护要求的 `required` check 为权威。

本地改动先运行最小相关测试；涉及 PocketBase migration、mutation kernel、WPF/WebView2 生命周期、打包布局、升级、发布 identity、权限边界或原生文件能力时，合并前还应本地执行 `uv run python qa/next.py --ci --json-report build/qa/report.json`，并保留 QA 证据。

## CD

`.github/workflows/cd.yml` 有两个入口，但不重建候选：

1. 手动运行并选择 `patch`、`minor` 或 `major`，创建或刷新唯一的 `automation/release` 版本/changelog PR。
2. 该 PR 合并后，成功的 `main` CI 触发 publish job；它只下载触发运行上传的同一 source SHA 候选，验证 release plan 与资产身份，生成 provenance/SBOM attestation，然后直接发布正式、非草稿 Release 并同步镜像。

普通 `main` push 没有匹配 release plan 时，CD 作为成功哨兵退出而不发布。稳定版本基线同时考虑当前版本、稳定 tag 和已发布正式 Release；若已有稳定 tag 但没有正式 Release，则继续升到下一语义化版本，不删除或复用受保护 tag。不得手工提升版本、打正式 tag 或创建 Release。

## 当前阈值评价

- Python 85%：对核心 backend 合理，保持。
- .NET 覆盖率由 `.ci/project.json` 的 `quality.dotnet_coverage.projects` 集中管理。Desktop、PreviewHost、Workspace 与 Infrastructure 分别使用独立程序集 Include 以及 line/branch total 门槛，测试项目不得持有数值、合并程序集总量或排除手写代码；新增 Coverlet 测试项目若未进入该 inventory，质量入口会 fail closed。
- Web：现阶段以全量 Vitest + typecheck + production build 为主；建议后续在覆盖率稳定后按核心 service/store 设置增量阈值，不宜立即用全局高阈值阻断 UI 重构。
- Go race：价值高且成本显著。当前 GitHub PR 的完整 release smoke 会执行 `race-a` 与
  `race-b` lanes；本地最小反馈可按改动风险只运行相关 Go 测试。门禁按包复用 race 编译、以三个
  package worker 执行，但每个包内仍逐测试独立进程串行；不减少测试、不关闭 race detector。
- 产品 E2E：必须作为发布证据，但依赖真实 Windows/WebView2 桌面会话，不伪装成 Ubuntu 单元门禁。

2026-07-31 的同机全量基线中，优化后的 Go race 覆盖 46 个有测试包、582 个命名
测试和 3 个无命名测试包，耗时 16.57 分钟；此前逐测试重复编译的远端基线为
69.12 分钟。若同规格 runner 连续三次超过 25 分钟，应先检查 package worker、
Go build cache 与临时二进制清理，不得通过跳过测试或放宽 race 门禁恢复速度。

2026-08-04 在同机启用三个 package worker，并让 QA 继承 Go 默认 build cache 后，
完整 race 阶段为 815.219 秒（13.59 分钟），较两个 worker 报告的 994.422 秒下降
18.02%。该次覆盖 46 个有测试包、575 个当前源码命名测试和 3 个无命名测试包；
两个报告对应不同源码提交，数量只用于证明当次完整枚举，不能作为测试删减比较。

## 产品 E2E 稳定性与性能预算

产品 E2E 结果会记录场景总耗时、每次 WebView2 bridge 往返、`requestType`、稳定错误码、未完成请求，以及历史抽屉从点击到首屏时间。以下规则用于源码冻结与发布门禁：

当前 manifest 声明的场景、E2E selector tag 与双向映射由
[产品 E2E 能力索引](quality/product-e2e-capability-index.md)确定性生成。该索引不等同于
Host/runtime capability，也不表示场景已通过；只有与候选 source SHA 绑定的 `required` 产品报告
才是本次运行的通过证据。

- 任意未预期的 `operation.failed` 或场景结束时仍 pending 的 bridge 请求，直接判定场景失败；不能再用“稍后轮询成功”掩盖中途错误。
- 普通历史查询以 p95 500ms 为告警线、单次 2s 为硬上限；历史抽屉首屏以 p95 750ms 为告警线、单次 2s 为硬上限。
- sidecar 故障注入后的读取属于恢复性能：允许在宿主内等待最多 3s，但只重试 `query.page` 和 `history.queryRequested` 这类幂等读取。写入、恢复应用等操作禁止自动重试。
- 场景的 180s 是防挂死超时，不是性能目标；场景总耗时包含建表、导入、截图和故障注入，应与同场景历史基线比较，不能拿它替代用户交互延迟。
- 在累计至少 10 次同规格 Windows runner 数据前，p95 预算先作为报告/告警项；零 bridge 失败与零 pending 立即作为硬门禁。连续三次 p95 超线或相对稳定基线上升 50% 时再升级为阻断项。

当前实测基线与解释见 [E2E 性能基线](e2e-performance.md)。

冲突、校验失败等预期业务拒绝也必须携带原始 `requestId`。测试可以接受预期的
`table.editRejected`，但不能接受无法与请求关联的响应；后者会被视为 pending，
用于防止异步桥接层悄悄丢失完成信号。
