# Mutation Product RPC 迁移前契约冻结

本切片只冻结仍由 Python 接收的 `mutation.preview` / `mutation.apply` 两个相邻公开方法，为 PR #140 L5 完整纵切保留独立输入与公开 wire。它不迁移 owner、不改变 Go mutation kernel，不声明数据写入迁移或产品资格完成。

固定生产者为 `38098da214a0fb33bb6df1fd0707b1ba0b4ac754`。该端点已包含完整 schema 只读迁移，两个 mutation 方法仍经真实 Python RpcDispatcher、Product DTO、PocketBaseProductRpc 与 PocketBaseClient 到既有 REST authority。捕获时只脚本化最末端 transport，不由未来 Go handler 手填 expected response。

## 已核实的生产边界

- Python 根参数闭集要求前七字段：contractVersion、requestId、idempotencyKey、tableId、schemaRevision、operations、actor；expectedRevision/expectedDigest 可以省略或显式 null。Python 层只在闭集/根类型和通用 JSON 边界拒绝，不能把脚本 transport 成功当作嵌套 operation 或 actor 已获 Go domain 接受。
- `PocketBaseClient.preview_mutation/apply_mutation` 经各自固定 REST 路径发送原参数，并要求返回对象；不另建一套业务结果投影。公开错误来自现有 dispatcher/error registry，不应照搬 REST HTTP 状态当作 Product JSON-RPC code。
- Go 已有 `mutationKernel.Preview/Apply` 和严格 `mutation.DecodeStrict`。现有 REST 请求预算为 1 MiB，multipart apply 另有 101 MiB 及 upload staging/Drop 生命周期；未来 Product JSON-RPC 迁移只处理其已有 JSON 请求，不意外接管 Host 附件上传路径。
- apply 必须沿用 `runBusinessWrite(ctx, gates, "mutation.apply", input.IdempotencyKey, ...)` 的当前 workspace 写入准入；preview 不进入这个写 gate。持久化、幂等、revision CAS、事件原子提交继续归 Go kernel，不在新 RPC handler 中重做。
- `InternalBypassMigrationFence` 仅内部迁移 adapter 使用，`json:"-"`；不得为了公共 RPC 接线公开或接受这一旁路。
- domain `mutation.ProductError`、formula error、context cancel/deadline 各有既有边界；迁移前须明确 Python/REST 的公开映射与 Go Product typed error对应，不能吞掉未知失败或无依据重试。

## 使用与验收边界

生成器、原件和相邻测试与现有 oracle 放在 `contracts/v2/`、`tests/contract/`。冻结写入只能来自固定 producer 且不覆盖已有原件；check 为只读重放比较。后续 owner 切换时关闭捕获入口并保留历史 wire，不能改原件去追认新实现。

独立 Standards / Spec 审查提出两项缺口（缺少正常 typed 成功形状、CI 未绑定保留原件），修正后最终两轴各 0 未解决问题。最终 fresh CI、squash/post验证仍未完成。后续真正 owner 迁移还必须有实际 Go authority 成功/失败/幂等/CAS/取消、Host composition 和产品写入场景；此脚本捕获不替代这些资格。
## 实际冻结与验证

原件 `mutation-product-python-oracle.json` 共32例，每方法16例。保留现有 Python 对动态 JSON 对象的宽度，也各含一例Go严格DTO可表达的完整正常输入与PreviewResult/Receipt形状；仍未执行真实domain。初稿30例保存在本地 `build/mutation-oracle-initial-30.json`；审查后仅末尾追加两例，主代理执行逐项语义比较确认原30与producer/boundary元数据完全不变，没有改expected去追认新实现。

`uv run --frozen --no-sync python contracts/v2/generate_mutation_product_oracle.py --write` 首次独占生成，以及随后 `--check` 全量重放均EXIT0，日志 `build/mutation-oracle-capture-32.log`。非对象响应负例会记录真实dispatcher的handler_failed诊断，原件保存的是脱敏公开wire；这不是捕获失败。

`uv run --frozen --no-sync python -m pytest tests/contract/test_mutation_product_oracle.py --no-cov -q`：8 PASS、6.72s。实际提交JSON与完整capture文本直接比较，因此以后原件被改动会在普通测试中失败；另外覆盖协议错误不能冻结、生产者变更/不可核实拒绝、已有原件不覆盖和check失败不写入。Ruff与捕获器mypy通过。

完整 `uv run --frozen --no-sync python scripts/automation_project.py python-quality` 仍不能记为PASS：

- 首次1790 PASS、1 skipped、2 FAIL，两个失败均为新工作树尚未接入Web依赖。核对lock一致后复用已有node_modules，无npm安装；两个原失败入口2/2 PASS、7.55s，日志 `build/mutation-oracle-node-contracts-restored.log`。
- 最终32例入口1793 PASS、1 skipped、1 FAIL，coverage90.89%已过85%门槛；失败为既有 `test_repeated_launch_and_close_does_not_leak_parent_handles` 第二批句柄172对baseline171。完整日志 `build/mutation-oracle-final-python-quality.log` 保留。
- 同端点对此测试隔离核对一次PASS，日志 `build/mutation-oracle-handle-isolated.log`；不因此抹去完整入口失败，也不据此确定根因。没有修改句柄断言、重试策略或CI。

最初两次聚焦预期修正分别来自真实nonobject params的-32600（非typed字段的-32602）及领域错误data的kind/message字段；最终原件由真实dispatcher重新捕获，并非手写wire。Python环境复用已有uv venv；仓库入口在新工作树准备其固定Node，Web依赖随后复用既有目录。没有修改lock、registry或生产路由。纯测试范围不要求重新构建产品，fresh CI仍按完整既有门禁执行。