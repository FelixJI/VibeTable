# Relation write 的固定 Python producer

producer 固定为 `e90889c1cb2c10b3f3a7c69e6f8de46dbccbeef2`。生成器只归档该提交的 backend，
以隔离子进程调用真实旧注册函数、dispatcher、adapter 和 scripted authority。JSON 的请求与结果/错误来自实际执行，
不从当前 Go 实现反填。共 8 个案例：原 mixed 六步序列、原 single update、single 清空、create 全 values、
create falsy 默认值、公开错误、transport 错误、坏 authority target。

每个案例分别保留 `handler` 与 `dispatcher` 两次独立执行，含完整输入、原 authority responses、
逐步 authorityRequests、返回值或异常、未消费 responses。原 mixed 的四个非 Relation 调用只是原测试前置证据，
不是本次迁移范围。现有 mixed 测试的四个非 Relation 调用继续验证当前 adapter；两个旧 Relation 测试保留原输入和断言，只将退休 handler 调用绑定到本次实时重放的固定 producer。Version 不在此改动范围。

## 不能混淆的旧路径

- 原 mixed 的 apply 输入含 `updates=[]`：直接 ProductParams handler 成功，旧公开 DTO 则拒绝，零 authority 请求。
- 原 single 输入含 `expectedDigest`：直接 handler 成功并转成 adds/removes；旧公开 DTO 不接受此字段，拒绝且零 authority 请求。
- 新 falsy 案例显式使用宽 ProductParams 观察本地默认值；空 label 被公开 DTO 拒绝。full values 案例两路径均可达。
- 公开错误与 transport 错误另用符合旧 DTO 的输入观察，分别保留稳定 structured error；不把输入校验失败当作后端错误。

旧测试的所有成功断言都能从 `handler.steps[*]` 读取；原断言和输入保持，固定 producer 每次真实执行并与冻结结果核对。Go 迁移另建真实消费者契约，
不能将这些 handler 成功外推成旧公开入口成功，也不能为了对齐本文件隐式删除新 Web 请求字段。
需改变旧 DTO/公开行为时，由主任务明确记录根因、目标契约和新 Go 回归。

## 生成与验证

在命令级将 `UV_PROJECT_ENVIRONMENT` 指向主任务指定的现有 Host `.venv`，设置 `UV_NO_SYNC=1` 和
`PYTHONPATH` 为当前 Relation checkout；归档子进程使用 `-I` 并显式插入固定 producer 根目录。

```text
uv run --frozen --no-sync python -B contracts/v2/generate_relation_write_oracle.py --write
uv run --frozen --no-sync python -B contracts/v2/generate_relation_write_oracle.py --check
uv run --frozen --no-sync pytest tests/contract/test_relation_write_python_oracle.py --no-cov
```

`--write` 仅首次创建固定 JSON；文件已存在即拒绝，不能覆盖旧输出。`--check` 每次重放固定 producer 后逐值比较。
每轮 archive、命令及 stdout/stderr 保存在 `build/qa/relation-write-oracle/replay-*/`，不覆盖或清理旧记录。
前置失败日志 `generate.log` 记录 dispatcher-only 首次遇到未消费 mixed fixture；
`generate-dual-seam.log` 与 `generate-model-seams.log` 记录 falsy DTO 与采集标记定位问题，
均未生成或覆盖最终 oracle。最终首次生成日志为 `generate-final.log`。

本文件只是可审查 freeze，不是三 write 已迁移或完整质量通过声明；上述 `--no-cov` 为聚焦诊断验证。


复现中的唯一随机输入是原 mixed 前置附件请求的 uuid4。第一次冻结后精确重放发现该差异，
`check.log` 保留失败；随后只在旧 producer 运行时固定此随机源为第一次实际产生的两个 UUID，
分别对应 handler/dispatcher。不改 frozen JSON，不替换或归一化捕获的请求与结果。
三项 Relation write 使用调用者给定的幂等键，不使用此随机源；固定前置 UUID 不代表迁移附件行为。

最终验证：生成与 `--check` 精确重放成功，聚焦 `--no-cov` 7 passed（1.08s），原两个测试的所有既有断言
在新增契约测试中再次核对。记录为 `check-fixed-prelude.log` 与 `pytest.log`。完整质量由主任务另行运行。
