# Go 粘贴计划与单次消费

`table.previewPaste` / `table.applyPaste` 由 Go Product RPC owner 承担。Host 的 typed table gateway 经生成 owner policy 直达 Go；Python composition 不注册这两个方法，也不实例化粘贴状态机。PocketBase 仍是唯一业务数据权威。

## 计划与写入

预览读取当前 schema、字段可写性和目标行 revision，保留原始单元格文本，复用 `importvalue` 与 MutationKernel 预检。服务端保存计划，客户端只持不透明的 `pst1.` token。10,000 单元格上限、五分钟有效期、selection/anchor、insert/update/skip 和原公开 DTO 保持不变。

`pasteOwner.plans` 属于当前 workspace sidecar 实例；Python 进程退出不会删除计划。短期计划不另建持久数据库。sidecar 或 workspace session 重建后旧 token 不可继续使用，客户端需重新预览。已有 mutation revision/digest 和幂等协议负责业务写入，不给计划叠加独立摘要。

Apply 串行核对 token、collection、schema、过期与消费状态，首次提交绑定幂等键；未知结果只允许沿用同一键重试。只有确认 committed 才消费 token，再次提交返回 `paste_token_consumed`。批量写入复用现有 MutationKernel 和 business write gate，禁止逐行部分提交。原粘贴 `-32040 / paste_error` 错误域保留；其他 Product 方法不能使用该投影。

## 迁移对账

原 Python 实现移至 `tests/backend/application/paste_oracle.py`，仅作为迁移期 oracle；生产模块只保留文件导入仍使用的共享 mutation 端口与错误类型。

- `tests/contract/fixtures/paste-owner-params-parity.json` 由旧 Pydantic DTO 冻结代表性参数接受/拒绝与归一化结果，覆盖别名、必填、null、数字坐标和 Unicode 长度；通过 `uv run python tests/contract/generate_paste_owner_parity.py` 生成，不从 Go 输出修改预期。
- `tests/integration/test_paste_go_owner.py` 用真实 sidecar 比较完整公开预览计划（排除随机 token 和时钟），并检查 Unicode、零、JSON 持久化和单次消费。
- Go 相邻测试覆盖未知提交重试、过期/schema/collection、并发唯一提交和 revision 冲突。
- 产品 S10 在同一真实候选中，经公开 Host 粘贴桥预览，终止准确的 Python 子进程后消费原计划并再次预览，再核对 Go 权威行值；保留原 workspace 关闭/重开及旧 epoch 拒绝验证。

此纵切不迁移 import/export/task/plugin，也不声称首次打开工作区已完全摆脱 Python。完整 required、独立审阅与合并后资格以对应 PR 的当前 SHA 和报告为准，局部测试不替代发布资格。