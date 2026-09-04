# Offline workbench contracts

`workbench.schema.json` 是 ViewQuery、DataBinding、Interface、Search、Formula author document、
computed envelope 与 schema audit event 的唯一语言无关事实源。

生成器只接受 `x-vibetable-generate` 中列出的、全部字段 required 且 `additionalProperties: false` 的闭合对象；遇到开放对象、可选字段或不支持的 `$ref` 会失败。生成物不包含字段资格、查询规划、搜索排名等业务规则。

```powershell
uv run python contracts/workbench/generate_dtos.py
uv run python contracts/workbench/generate_dtos.py --check
```

正反 fixtures 是 consumer compatibility 事实源。Python 生成模型直接执行 closed validation；Go 测试先使用锁定的 draft-2020-12 validator（启用 format assertion）验证 Schema verdict，再独立验证 closed DTO decode。C# 与 TypeScript 生成物由各自项目的 warnings-as-errors/typecheck 编译门禁覆盖。

Formula author range 使用零基 UTF-16 `line`/`character`，与浏览器编辑器坐标一致；range 为左闭右开。
Schema 只冻结可跨语言严格解码的结构。token kind 与 ID 组合、range 相对 `displaySource` 的顺序和边界、
`documentRevision` 单调性由唯一 Formula author runtime owner 校验，不在生成 DTO 中复制业务语义。

`ComputedCellEnvelope.state` 的公开闭集为 `ready`、`updating`、`failed`、`cancelled`、`invalid` 和
`too_expensive`。旧的 `pending`、`stale`、`error` 不属于该契约；各状态对 value、diagnostic 与 freshness
版本的组合约束由计算 runtime owner 统一实施。
