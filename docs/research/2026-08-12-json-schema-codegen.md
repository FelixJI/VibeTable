# JSON Schema 2020-12 多语言 closed DTO codegen 研究（M0，2026-08-12）

## 结论

**建议采用“Schema 仍是唯一真相 + 各语言最小生成器/验证器”的组合，不采用单一跨四种语言的生成器。** 目前没有查到一个一手文档足以证明同时正确覆盖本仓 JSON Schema 2020-12、`additionalProperties: false`、精确 `oneOf`、运行时拒绝未知字段，以及四种目标语言的稳定输出的工具。

建议先只对一个具有显式 `kind` 常量的闭合对象子集做 PoC，再决定是否推广：

| 目标 | 建议的生成与运行时边界 | 许可证与打包结论 |
| --- | --- | --- |
| TypeScript | `json-schema-to-typescript` 只生成静态类型；用 Ajv 的 draft-2020 实例 + `ajv-formats` 作输入验证，并以 Ajv standalone 产出验证函数。 | 两者 MIT；生成器和 Ajv 编译工具为 Node 开发依赖，不进入 Web 发布包，standalone 验证器是否随包由 PoC 决定。 |
| C# | 优先 PoC [Corvus.JsonSchema](https://github.com/corvus-dotnet/Corvus.JsonSchema) source generator；若其调用方式不适合现有 DTO，则保留手写 System.Text.Json DTO，并以 Schema 验证作为边界。 | Apache-2.0；source generator 是构建期依赖，Corvus 运行时包若被引用会进入 Windows 桌面包，须进入现有 SBOM/许可证流程。 |
| Python | [datamodel-code-generator](https://github.com/koxudaxi/datamodel-code-generator) 生成 Pydantic v2 模型；将 `extra='forbid'` 纳入生成结果契约测试。需要完整 JSON Schema 语义时，再评估 `jsonschema` 验证器。 | 两个项目均 MIT；本仓已锁 Pydantic 2.13.4，但未锁 datamodel-code-generator 或 jsonschema。前者只应为开发依赖；后者若运行时使用会被 Python 打包带入。 |
| Go | **不推荐直接采用通用 Go 生成器作为闭合 DTO 的生产方案。** 小型受控模板生成 struct、`UnmarshalJSON`/入口解码和测试；在 wire 边界复用已锁定的 `github.com/santhosh-tekuri/jsonschema/v6` 进行 draft 2020-12 Schema 验证。 | 当前验证器 Apache-2.0，已在 `sidecar/go.mod` 锁为 v6.0.2，会进入 sidecar；生成代码是仓库源文件。 |

这里的 “closed DTO” 指**接受 JSON 时**拒绝 Schema 不允许的字段，而不是仅在编译期让对象类型没有索引签名。TypeScript 类型本身不能完成这个运行时要求。

## 仓库事实与约束

以下是从工作树可执行配置和现有合约直接核实的事实：

- `contracts/v2/product-contracts.schema.json` 在根部声明 2020-12 meta-schema；合约广泛使用 `$defs`、`oneOf`、`const`、`enum`、`format: date-time`、`type: ["string", "null"]` 与 `additionalProperties: false`。`JsonObject` 等定义刻意开放，不能被生成器一概闭合。
- 根 `oneOf` 并不全部有共享的 tag；但若干变体以 `kind`/`topic` 的 `const` 表达区分。现有源 Schema 未使用 OpenAPI 的 `nullable` 或 `discriminator`。
- 现有实现已经有三种拒绝未知字段的边界：Python 使用 `ConfigDict(extra="forbid")`，C# 使用 `JsonUnmappedMemberHandling.Disallow`，Go 使用 `json.Decoder.DisallowUnknownFields()`。这证明“闭合”是既有契约，不应因引入 codegen 降级。
- 锁定环境为 Web Node `>=24 <25`，Python 侧锁 Pydantic 2.13.4，sidecar 锁 `github.com/santhosh-tekuri/jsonschema/v6 v6.0.2`，C# contracts 为 `net10.0`。本次检查未发现已锁的 `json-schema-to-typescript`、Ajv、Corvus、datamodel-code-generator、quicktype 或 Go DTO codegen 工具。

## 语义基线：事实与实施建议

| 主题 | 一手来源事实 | 对 M0 的建议 |
| --- | --- | --- |
| `additionalProperties: false` | 该关键字约束未由 `properties` 或 `patternProperties` 覆盖的成员；Ajv 的 JSON Schema 文档给出了拒绝额外成员的例子。[Ajv JSON Schema](https://ajv.js.org/json-schema.html#additional-properties) | 每种语言必须以 JSON 输入的负例证明拒绝未知字段；不得把“生成出的 interface/struct 无索引签名”当作证明。刻意开放的 `JsonObject` 单独列白名单。 |
| `oneOf` 与 discriminator | 2020-12 将 `oneOf` 定义为 applicator；核心规范没有 `discriminator` 关键字。[2020-12 Core](https://json-schema.org/draft/2020-12/json-schema-core) Ajv 把 `discriminator` 列为 OpenAPI 支持而非 JSON Schema 关键字。[Ajv OpenAPI 扩展](https://ajv.js.org/json-schema.html#openapi-support) | 源 Schema 保持 `oneOf`；可生成的辨别联合须让每个分支都 `required` 一个同名 tag，且用互异 `const` 固定值。没有共同 tag 的 root union 不强制映射为某语言的 discriminated union，应保留命名入口或先重构 Schema。 |
| nullable / required | 2020-12 的 `type` 可为唯一数组，内含 `null`；`required` 仅要求成员出现，和可否为 null 是两件事。[2020-12 Validation §6.1.1、§6.5.3](https://json-schema.org/draft/2020-12/json-schema-validation) | 以 `type: [T, "null"]` 表示 nullable；禁止为迁就生成器把 Schema 改为 OpenAPI `nullable`。测试必须区分“缺失”“显式 null”“非 null”。 |
| enum | `enum` 可包含任意 JSON 值（包括 null）。[2020-12 Validation §6.1.2](https://json-schema.org/draft/2020-12/json-schema-validation) | 仅纯字符串枚举才映射为各语言 enum；混合/null enum 用语言联合/受限值模型，并用反例验证。 |
| format | 2020-12 的 Format-Annotation 是必备词汇；Format-Assertion 是可选词汇，默认 meta-schema 不要求把 format 当作断言。[2020-12 Validation §7](https://json-schema.org/draft/2020-12/json-schema-validation) | `date-time` 等 format 的通过/失败不能默认跨语言相同。将“启用 format assertion”的确切开关、格式实现版本和负例语料作为 PoC 通过条件；否则把它明确视为注释而非拒绝条件。 |

## 候选评估

### 推荐组合的组成部分

#### TypeScript：json-schema-to-typescript + Ajv

**事实。** [json-schema-to-typescript](https://github.com/bcherny/json-schema-to-typescript) 可从 JSON Schema 生成 TypeScript 定义，许可证为 MIT；但其上游 README 明确说明 `oneOf` 按 `anyOf` 处理。因此它不能单独证明“恰好一个分支”或运行时 unknown-field rejection。Ajv 支持 draft 2020-12（需使用相应的 2020 实例，且不能与旧 draft schema 混用）；format 需要额外加载 `ajv-formats`。[Ajv draft 支持与 format](https://ajv.js.org/json-schema.html#draft-2020-12) Ajv CLI 可以 `compile` 输出 standalone 验证函数。[Ajv standalone](https://ajv.js.org/standalone.html) Ajv 的上游 LICENSE 是 MIT。[LICENSE](https://github.com/ajv-validator/ajv/blob/master/LICENSE)

**建议。** 将生成的 TypeScript 仅作为开发者类型体验；在任何 JSON 跨进程/插件/文件边界先运行 Ajv 2020 验证器。固定 npm 版本、显式注册 formats，并为生成结果运行 Prettier/现有 formatter。不要让生成器把 `oneOf` 的宽松静态表示误认为运行时行为。

#### C#：Corvus.JsonSchema 作为 PoC

**事实。** [Corvus.JsonSchema 上游 README](https://github.com/corvus-dotnet/Corvus.JsonSchema) 声明它通过 source generator 从 JSON Schema 生成强类型 C# 模型，并支持包含 2020-12 在内的多个 draft 的验证；示例以 `JsonSchemaTypeGenerator` 和 `EvaluateSchema()` 使用。项目为 Apache-2.0。[LICENSE](https://github.com/corvus-dotnet/Corvus.JsonSchema/blob/main/LICENSE) .NET 本身可用 `[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]` 使 unmapped JSON 成员抛出 `JsonException`。[Microsoft 文档](https://learn.microsoft.com/en-us/dotnet/standard/serialization/system-text-json/missing-members)

**建议。** 仅把 Corvus 放进 M0 小样本，评价它是否能在 `net10.0`、现有 System.Text.Json 边界和 Windows 发布流程中保留 `oneOf`、null、enum、format 与闭合对象语义。它生成的是 Schema value model，不是可无缝替换现有普通 POCO 的承诺；若 PoC 的调用/序列化模型侵入性过大，继续使用现有 DTO + `[JsonUnmappedMemberHandling]`，并将 Schema 验证作为 wire guard。

#### Python：datamodel-code-generator + Pydantic v2

**事实。** [datamodel-code-generator 上游 README](https://github.com/koxudaxi/datamodel-code-generator) 声明可由 JSON Schema 生成 Pydantic v2 模型，并列出 `$ref`、allOf、oneOf、anyOf 与 enum 支持；其 LICENSE 为 MIT。[LICENSE](https://github.com/koxudaxi/datamodel-code-generator/blob/main/LICENSE) Pydantic 的 `ConfigDict(extra='forbid')` 会对额外输入抛出 `ValidationError`。[Pydantic Config API](https://docs.pydantic.dev/latest/api/config/#pydantic.config.ConfigDict.extra) [python-jsonschema](https://github.com/python-jsonschema/jsonschema) 声明支持 Draft 2020-12、MIT 许可，并提醒 format validation 需显式启用。

**建议。** 生成 Pydantic v2；对每一个 `additionalProperties: false` 模型检查生成结果或公共基类确实配置 `extra='forbid'`。不要凭 README 推断该映射已经正确：它是 M0 必测项。仅 Pydantic 的字段模型不等同于完整 JSON Schema evaluator；若 Python 接受外部 JSON 且要承诺全部 2020-12 语义，评估新增并锁定 python-jsonschema，且显式选择 format checker。

#### Go：已锁 validator + 小型受控模板

**事实。** 本仓已锁的 [santhosh-tekuri/jsonschema](https://github.com/santhosh-tekuri/jsonschema) 支持 draft 2020-12，项目为 Apache-2.0，并文档化 format assertions 的开关。Go 标准库 `Decoder.DisallowUnknownFields` 在目标为 struct 时会令未知 key 报错。[encoding/json](https://pkg.go.dev/encoding/json#Decoder.DisallowUnknownFields)

**建议。** 为闭合 DTO 生成小、可审计的 struct 和入口解码辅助代码，而不是引入一套未经本仓 Schema 证明的 Go 全量 codegen。先以已锁 validator 判断原始 JSON 对 2020-12 Schema 的有效性，再解码到 struct；若效率要求相反顺序，也必须两步均有负例覆盖。生成器只需覆盖已批准的受限 Schema 子集，遇到开放对象、无 tag 的 `oneOf`、未支持关键词时 fail closed。

### 明确拒绝项

| 候选 | 拒绝原因（均为当前 M0 决策，不是对项目的全面否定） |
| --- | --- |
| [quicktype](https://github.com/glideapps/quicktype) 作为唯一方案 | **事实：**它支持 JSON Schema 输入和 TypeScript、Python、C#、Go 输出，CLI 要求 Node 20+，许可证 Apache-2.0。[上游 README](https://github.com/glideapps/quicktype) [LICENSE](https://github.com/glideapps/quicktype/blob/master/LICENSE) **判断：**上游材料不足以证明本仓 2020-12 `additionalProperties: false`、精确 `oneOf`、format assertion 与 unknown rejection 的端到端等价，也没有可依赖的原生 `--check` 契约；不应因“一条命令四种语言”绕过 PoC。 |
| json-schema-to-typescript 单独使用 | **事实：**上游明确把 `oneOf` 当 `anyOf`。**判断：**静态类型不能拒绝运行时未知字段，也不能证明精确 union；必须搭配 Ajv，故不能独立采用。 |
| [go-jsonschema](https://github.com/omissis/go-jsonschema) 直接生产采用 | **事实：**上游描述为“not finished”，虽宣称生成 Go 类型与验证 unmarshalling，支持清单没有将 `additionalProperties` 列为对象验证项，且没有文档化原生 `--check`。[上游 README](https://github.com/omissis/go-jsonschema) 许可证 MIT。[LICENSE](https://github.com/omissis/go-jsonschema/blob/main/LICENSE) **判断：**在闭合对象是硬契约时，不能把未列出的行为当作已验证能力。 |
| 以 OpenAPI `nullable`/`discriminator` 为输入前提的生成器 | **事实：**两关键字不是 JSON Schema 2020-12 核心关键字。**判断：**不得为适配器把规范 Schema 改写成另一规范；若未来另建 OpenAPI 派生产物，须作为显式转换步骤并独立验证。 |

## 最小 PoC 与可重复检查方案（建议，尚未执行）

### 1. 固定小 Schema 和 JSON 语料

新增一个独立 M0 fixture（不修改现有业务 Schema），至少含：

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "state", "note", "at"],
  "properties": {
    "kind": { "const": "create" },
    "state": { "enum": ["draft", "ready"] },
    "note": { "type": ["string", "null"] },
    "at": { "type": "string", "format": "date-time" }
  }
}
```

再建立一个由两个这种对象组成、每个分支有同名 `kind` + 互异 `const` 的 `oneOf`。语料必须覆盖：正常值、可选字段缺失、required 字段缺失、显式 null、错误 enum、未知字段、错误 tag、同时/不命中 `oneOf` 的值、无效 date-time。每种语言均需先断言 Schema verdict，再断言 DTO decode verdict；两者不一致即 PoC 失败。format 用例只有在目标 validator 的 assertion 开关明确开启后才计入失败预期。

### 2. 生成与 CI check

建议将工具版本锁入各自既有包管理器，不使用浮动 `npx`、`go run module@latest` 或全局 dotnet tool：

1. Node：将 jstt、Ajv、ajv-formats 锁进相应 `package.json`/lock，`npm ci` 后调用本地 CLI；Ajv standalone 输出固定路径的验证器。
2. Python：通过 `uv` 锁 datamodel-code-generator；只经 `uv run` 调用。Pydantic 已有锁，但任何新增 `jsonschema` 也必须进入 `uv.lock`。
3. C#：将 Corvus 包版本固定在项目引用或本地 tool manifest；`dotnet restore`/`dotnet build` 触发 source generator，禁止依赖用户全局安装。
4. Go：继续用 `go.mod`/`go.sum` 锁 validator；受控生成器需要固定版本并对输出运行 `gofmt`。

所有候选材料均未证明统一的原生 `--check`，因此由仓库 wrapper 提供：干净 worktree 中按固定顺序重新生成、运行语言 formatter，然后 `git diff --exit-code -- <受管输出路径>`。wrapper 还应拒绝非固定输出、时间戳、绝对路径和网络下载。CI 最少执行：生成 check、四语言编译、共享 JSON 语料测试、现有 `scripts/build_next.py --release` 的 Windows win-x64 构建/烟测；开发工具不得被复制进 Web 资产、Python runtime 或 Go sidecar。

### 3. 推广门槛

- 只有 fixture 与从现有 `contracts/v2` 选出的**有显式 tag 的闭合定义**四语言结果一致后，才扩展覆盖面。
- 对无共享 tag 的 root `oneOf`、刻意开放的 `JsonObject`、混合类型 enum 与复杂递归 `$ref`，生成器必须给出明确诊断并停止，而非静默降级。
- 将锁版本、许可证、生成命令、输出清单加入现有 Windows 发布 SBOM/许可证证据；新增 C#/Python 运行时依赖时尤其如此。

## 复现命令

从仓库根目录执行：

```powershell
git status -sb
rg --files -g AGENTS.md -g "*lock*" -g "*.schema.json" -g "package.json" -g "go.mod" -g "*.csproj"
rg -n 'additionalProperties|oneOf|const|nullable|discriminator|JsonUnmappedMemberHandling|DisallowUnknownFields|extra=.forbid.' contracts backend desktop sidecar
Get-Content contracts/v2/product-contracts.schema.json
Get-Content desktop/web-grid/package.json
Get-Content pyproject.toml; Get-Content sidecar/go.mod
```

来源均为规范、官方产品文档或上游源码/许可证，链接已紧随相应事实；没有以二手博客、包索引摘要或记忆代替取证。

## 未验证范围

- 本研究没有安装、下载或运行任一新增工具，也没有生成或编译 DTO；所有推荐在纳入锁文件前仍需按上述 PoC 在 Windows CI 与本机实际执行。
- 尚未证明 datamodel-code-generator 会把本仓每个 `additionalProperties: false` 可靠转为 `extra='forbid'`，也未证明 Corvus 对该 Schema 子集的反序列化/unknown-field 行为；这是首要 PoC gate。
- 未验证各 validator 对 `date-time` 等 format 的精确一致性、`oneOf` 中复杂 `$ref`/递归/未标记 root union 的输出、字段命名和序列化 round-trip。
- 未核对新增包在当前精确 Windows/Node 24/Python 3.11/Go/.NET 10 锁版本下的依赖树、离线安装、发布包体积、SBOM 归集和第三方通知；许可证结论仅针对上游项目 LICENSE，不涵盖未来解析出的传递依赖。
