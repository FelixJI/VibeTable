# 质量门禁

VibeTable 同时包含 Python、Vue/TypeScript、Go 和 Windows/.NET。门禁按反馈速度和运行环境分层，避免把耗时且依赖桌面会话的场景塞进每个 PR，也避免只测前端和 Python 就误判为可发布。

## PR 必需门禁

`.github/workflows/ci.yml` 在 `pull_request` 和 `main` push 上运行，仓库分支保护应要求以下检查全部通过：

| 检查 | 覆盖 |
|---|---|
| `Python / contracts` | 版本/包清单、Ruff format/lint、Pyright、mypy、pytest 与 85% 后端覆盖率 |
| `Web grid` | 锁定依赖、Vitest、Vue/TypeScript 类型检查、生产构建 |
| `Plugin / sdk` | 插件 SDK 类型契约 |
| `Plugin / data-overview` | 示例插件类型与离线测试 |
| `Plugin / normalize-text` | 示例插件类型与离线测试 |
| `Go sidecar` | gofmt、vet、全量普通测试、可发布入口构建 |
| `Windows / .NET` | Windows runner 上的 Release solution 测试与各项目覆盖率阈值 |

CI 使用最小 `contents: read` 权限、按 workflow/ref 取消过期运行、锁文件安装、明确超时，并保留 Python JUnit/coverage 报告 14 天。Ubuntu 的 Python job 只排除 `tests/e2e/test_next_readonly_smoke.py`：该测试会启动 WPF/WebView2，必须在完整 Windows 桌面会话下运行，并由下述重型门禁的 `smoke` 阶段继续强制执行；其余可移植 Python 测试仍由 PR 门禁自动发现和覆盖。

## 重型发布门禁

`python qa/next.py --ci --json-report build/qa/report.json` 是源码冻结后的完整本机门禁，额外包含 Go race、打包 sidecar 矩阵、升级 smoke、故障注入、WPF/WebView2 产品 E2E 和最终只读 smoke。它依赖完整 Windows 桌面环境及仓库文档中声明的工具链，不作为每个 PR 的 GitHub-hosted 必需检查。

出现以下变更时，合并前应额外运行完整门禁：

- PocketBase migration、mutation kernel、附件或备份/恢复；
- WPF/WebView2 生命周期、进程监督或数据目录迁移；
- 打包布局、升级、发布清单或 release identity；
- 权限边界、插件 worker、原生文件能力。

## CD

`.github/workflows/release.yml` 只有一个入口：推送与仓库版本一致的 `vX.Y.Z` tag。工作流会在 Windows 上重新执行关键 Python/契约、web、Go 和 .NET 验证，再构建并校验包，生成 ZIP、SHA-256 和包内 SBOM，最后通过受保护的 `release` Environment 发布 GitHub Release。

建议发布流程：

1. `main` 的全部必需检查通过，并完成适用的重型门禁。
2. 在干净工作树执行 `python scripts/release.py --bump patch --push`（或 `major`/`minor`）。
3. tag CD 校验 tag 与版本、重新验证、构建并发布。

发布工作流不会自行修改版本或推送 tag，避免一次操作触发两条相互竞争的发布链。重复运行同一 tag 时会覆盖同名资产，不创建第二个 Release。

## 当前阈值评价

- Python 85%：对核心 backend 合理，保持。
- .NET 45%/65%/80% 分项目阈值：按模块可测试性分层，比单一总阈值合理；新增数据目录事务逻辑必须有独立单测。
- Web：现阶段以全量 Vitest + typecheck + production build 为主；建议后续在覆盖率稳定后按核心 service/store 设置增量阈值，不宜立即用全局高阈值阻断 UI 重构。
- Go race：价值高但成本显著，放在源码冻结/高风险变更门禁，不放普通 PR。
- 产品 E2E：必须作为发布证据，但依赖真实 Windows/WebView2 桌面会话，不伪装成 Ubuntu 单元门禁。

## 产品 E2E 稳定性与性能预算

产品 E2E 结果会记录场景总耗时、每次 WebView2 bridge 往返、`requestType`、稳定错误码、未完成请求，以及历史抽屉从点击到首屏时间。以下规则用于源码冻结与发布门禁：

- 任意未预期的 `operation.failed` 或场景结束时仍 pending 的 bridge 请求，直接判定场景失败；不能再用“稍后轮询成功”掩盖中途错误。
- 普通历史查询以 p95 500ms 为告警线、单次 2s 为硬上限；历史抽屉首屏以 p95 750ms 为告警线、单次 2s 为硬上限。
- sidecar 故障注入后的读取属于恢复性能：允许在宿主内等待最多 3s，但只重试 `query.page` 和 `history.queryRequested` 这类幂等读取。写入、恢复应用等操作禁止自动重试。
- 场景的 180s 是防挂死超时，不是性能目标；场景总耗时包含建表、导入、截图和故障注入，应与同场景历史基线比较，不能拿它替代用户交互延迟。
- 在累计至少 10 次同规格 Windows runner 数据前，p95 预算先作为报告/告警项；零 bridge 失败与零 pending 立即作为硬门禁。连续三次 p95 超线或相对稳定基线上升 50% 时再升级为阻断项。

当前实测基线与解释见 [E2E 性能基线](e2e-performance.md)。

冲突、校验失败等预期业务拒绝也必须携带原始 `requestId`。测试可以接受预期的
`table.editRejected`，但不能接受无法与请求关联的响应；后者会被视为 pending，
用于防止异步桥接层悄悄丢失完成信号。
