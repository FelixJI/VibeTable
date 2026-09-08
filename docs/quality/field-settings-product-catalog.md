# 字段描述 Product catalog 准入资格

## 意图与边界

`field.settings.describe` 已是公开的只读 typed 入口，但原来依赖 Host 的 `Workspace` 分类固定走
Python，未纳入 Product 的封闭 owner/audience policy。Python 的历史排除集合也把它称作
Workspace catalog 项；实际 Workspace RPC registry、policy 和 59 方法 manifest 均不包含它。

本切片只把此方法纳入 Product catalog，新增 `workspace` scope、`rendererPublic` audience、
`schema.query` capabilityId 和 `read` effect，当前 owner 仍为 `pythonBff`。
`schema.query` 与既有 schema 描述/读取能力对应；这是新声明，不是从 Workspace manifest 复制的旧值。

Host 使用正常的 Product policy 准入，缺失 policy、非公开 audience 或不可用 transport owner 时拒绝。
Python 参数模型、业务 handler、现有 sidecar REST 及有效请求的返回语义保持原契约。
另外五个历史 Field typed 入口继续原路由；真实 Workspace manifest 无需扩展。

这项变更独立完成封闭策略覆盖。后续 Go owner 迁移必须另行完成 producer、Python 专属执行路径退役、
无 fallback 的 Host 接线、真实新构建字段助手场景和 fresh CI，不能引用本项作为 Go 执行资格。

## 验证记录

实施基线：`main@187390c7250a4c1896f7b2c78c83d5cdc80f91f4`。

- Product catalog 的新增 case 必须由生成器产生，并使用完整字段描述结果契约；其余 case 的输入与结果语义保持不变。
- 生成的 Product manifest、Python/Go/TypeScript 适配和 inventory 必须一致；Go 注册集合保持原样。
- 聚焦 Python、Host、Go、Web 验证结果见下表；保留首轮失败，不把定向复验写成全组重跑通过。
- 独立 Standards/Spec 的完整源码及最终测试/文档增量均无遗留发现；最终 fresh CI、squash 和合并后 CI/CD 尚待完成。

本项不改变 UI 表现，不声称新增 Go 端产品场景已完成。

## 本地结果

复用已有 uv 环境、固定 .NET 10.0.400、Node 24.19.0、Go 与依赖缓存。
新 worktree 只执行一次 `dotnet restore desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --locked-mode --artifacts-path build/dotnet`，通过；未改依赖或 lock。

| 范围 | 结果 |
| --- | --- |
| Python Product catalog/policy/inventory、Workspace manifest、PocketBase Product 适配 | 68 passed，2.23s |
| 三个正式生成器的新进程 `--check` | 全部通过 |
| 4 个相关 Python 文件 Ruff check/format | 通过 |
| CI 声明的 backend Pyright 范围 | 0 errors |
| Host 首组六类 | 109 passed / 1 failed / 0 skipped；657ms |
| 修正后的 Host invoker 类及新增覆盖的 Product manifest 类 | 20 passed / 0 failed/skipped；200ms |
| Go 首组 capability / dispatcher | capability 失败于旧总数102；dispatcher 通过0.703s |
| 修正后的 Go capability 包 | 通过0.210s，完整 Python descriptor 与原15个Go集合均成立 |
| Web generated capability 与 hostBridge | 2 files / 35 tests passed，1.64s |

Python 命令：

```text
uv run --frozen --no-sync python -m pytest tests/contract/test_product_contracts.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_workspace_rpc_capability_manifest.py tests/backend/adapters/test_pocketbase_product_rpc.py -q --no-cov
uv run --frozen --no-sync python -m contracts.v2.generate_product_rpc_catalog --check
uv run --frozen --no-sync python -m contracts.v2.product_rpc_capability_policy --check
uv run --frozen --no-sync python -m contracts.v2.generate_workspace_rpc_capability_manifest --check
```

Host 两次均使用 `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --artifacts-path build/dotnet`，
首轮 filter 为 `FullyQualifiedName~ProductDataRpcRegistryTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~WorkspaceSessionEnvelopeFilterTests|FullyQualifiedName~ProductDataRequestControllerTests|FullyQualifiedName~HostProductRpcInvokerTests`；
修正后 filter 为 `FullyQualifiedName~HostProductRpcInvokerTests|FullyQualifiedName~ProductRpcCapabilityManifestTests`。

Go 在 sidecar 执行 `go test ./internal/contracts/productcapabilities ./internal/productrpc`，修正后只执行
`go test ./internal/contracts/productcapabilities`。Web 在 desktop/web-grid 执行
`npm run test -- src/contracts/generated/productRpcCapabilities.test.ts src/bridge/hostBridge.test.ts`。

Host 首次失败来自旧测试仍以 describe 作为无 Product policy 的遗留入口：新实现正确拒绝，Python WriteCount 为0而旧预期为1。
修正将遗留入口改为仍支持的 recycleBin.list，保留 WriteCount=1，并新增 describe 缺 policy 不发送及完整有效结果成功的契约。
Go 首次失败来自 catalog 总数断言仍为102；修正为103并验证新增 descriptor 的全部字段，原 Go owner 集合不变。

额外将 Pyright 扩展到 catalog generator 时出现4处旧函数类型错误；对 HEAD 原文件执行同一检查也为相同4错。
这些不在本次新增 import/result spec 行，未混入独立类型修复，也未把该扩展检查写成通过。

catalog 首次生成存在旧派生 owner 集合与新排除集合的自举依赖；本地一次进程加载旧模块后调用原生成器完成准入，
随后正式 policy 生成和三个独立新进程检查通过。没有放宽生产差集守卫或手工编辑派生物。
解析比较证实其余102个 case 仅原生成器的信封编号顺延，嵌套 ID、输入、结果、schema及错误语义均不变。

本地日志保存在 `build/admit-*` 与 `build/field-settings-catalog-*`；不提交缓存或构建物。
本项未重建或运行完整产品包，完整构建/E2E 门禁由最终 PR fresh CI 执行，仍为待完成。