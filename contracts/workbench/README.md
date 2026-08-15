# Offline workbench contracts

`workbench.schema.json` 是 ViewQuery、DataBinding、Interface、Search、computed envelope 与 schema audit event 的唯一语言无关事实源。

生成器只接受 `x-vibetable-generate` 中列出的、全部字段 required 且 `additionalProperties: false` 的闭合对象；遇到开放对象、可选字段或不支持的 `$ref` 会失败。生成物不包含字段资格、查询规划、搜索排名等业务规则。

```powershell
uv run python contracts/workbench/generate_dtos.py
uv run python contracts/workbench/generate_dtos.py --check
```

正反 fixtures 是 consumer compatibility 事实源。Python 生成模型直接执行 closed validation；Go 测试先使用锁定的 draft-2020-12 validator（启用 format assertion）验证 Schema verdict，再独立验证 closed DTO decode。C# 与 TypeScript 生成物由各自项目的 warnings-as-errors/typecheck 编译门禁覆盖。
