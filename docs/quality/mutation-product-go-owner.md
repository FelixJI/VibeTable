# mutation 产品 RPC Go owner 迁移资格

此变更将同一 mutation 契约下的 preview/apply 成对迁入 Go，避免预览与应用形成中间 owner 状态。保留已有 mutation Kernel 和 workspace 写门禁，Python import/export/plugin 使用的内部 client 不随产品路由删除。产品目录仍为 103 RPC、7 事件，当前 owner 为 21 Go / 80 Python / 2 Host。

前置交付为 Go 权威恢复流、Host 完整消费者和独立 workspace mutation 重放修复。该修复通过独立 PR #298 验收；后续迁移 PR 须同步其实际 main 合并结果，不能把重放修复重复包装为 owner 切换。

## 契约

Go 保留闭集根 DTO、Unicode scalar、通用 JSON 深度/数值/secret 与紧凑 UTF-8 1 MiB 限制，参数错误在 dispatcher 层拒绝；领域 DecodeStrict 和既有公共错误在 handler 层处理。apply 使用原 mutation.apply business gate，preview 不进入写门禁。Host 默认路由由生成的能力清单决定，Go 错误不回退 Python。

历史 Python oracle 保留 producer `38098da214a0fb33bb6df1fd0707b1ba0b4ac754` 与 32 条原件。capture/capture_case/--write 已退休；默认与 --check 仅检查历史结构和静态约定。包括完整 domain/transport error.data 与 JSON 类型，11 个破坏变体先失败后通过。原 JSON 相对 `9be34b05896dec4d348105d68216969306b22f21` 无 diff。历史 scripted transport 结果不是实际 mutation 领域验收；Go 直接消费两条完整 typed 结果，其余语义依赖对应领域与实际 handler 测试。

## 本地验证

复用缓存工具链和环境。工作树原有空 .venv 使固定 Pyright 路径无法解析依赖，使用 `uv sync --frozen --group dev --group build --offline` 从缓存补齐，未重建环境或修改 lock。

- sidecar：`go test -race ./internal/app -run '^(TestMutationProduct|TestMutationExistingREST)' -count=1 -timeout=5m` PASS19.544s。真实 PocketBase + workspace Runtime + REST/Product handler 覆盖 preview 无权威写入、insert、完整 receipt、幂等重放、stale CAS、旧 epoch 和关闭门禁拒绝。修复前新 Product 与旧 REST 重放均有 RED 日志。
- Go productcapabilities 与 productrpc 全包 race PASS；旧严格 HTTP fixture 补入两个不得执行的注册，继续验证完整清单而非放宽门禁。
- HostProductRpcCompositionTests：固定 SDK/locked restore，36 PASS、0 skip；两新方法默认 Go 及错误不回退 Python。
- Python 路由/owner 聚焦测试 48 PASS；历史 oracle 26 PASS，包括 11 个 error.data 破坏变体 RED→GREEN。
- `uv run --frozen --no-sync python scripts/automation_project.py python-quality`：Ruff/Pyright/mypy PASS；初次完整 pytest 1799 PASS、1 skip、1 fail，coverage91.36%。失败为旧精确 owner 集合未包含本次两个方法；修正后该契约文件 10 PASS。同一完整入口最终复验 PASS：1800 passed、1 skipped，70.73 秒，coverage91.36%；保留前述失败记录。

证据保留于 build/mutation-product-http-integrated-replay-fix.log、mutation-owner-go-capabilities-corrected.log、mutation-host-composition-tests.log、mutation-owner-python-focused-corrected.log、mutation-error-data-red.log、mutation-error-data-green.log、mutation-owner-python-quality-final.log、mutation-owner-inventory-final.log、mutation-owner-python-quality-verified.log。

## 审查和剩余资格

Standards 未发现确定违例，指出严格 fixture 装配多处重复的维护性建议；本意图保留现有装配，不混入独立重构。Spec 发现历史 error.data 漂移检查不足，已修复并独立增量复审为 0 新问题。新增 owner 集合与本文已完成两轴独立增量复审，均为 0 个确定问题。

当前尚未完成最新端点的实际打包 WebView2 场景、完整 fresh CI、squash merge 及 main CI/CD。既有 Host 构建的场景证据不能直接冒充本 owner 端点的实际运行。实际产品资格至少需覆盖普通编辑、过期冲突，以及仍经 Python 工作流进入原 Kernel 的相邻路径。
