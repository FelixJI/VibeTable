# PDF 支持子集与资格策略（A6，准备决策）

## 状态与目标

本策略落实 [完成计划 §14.3](2026-08-12-offline-data-workbench-completion.md#143-内容格式) 和
[A6](2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md#a6pdf-提取能力决策)。它定义下一次
实现/资格的可审查入口；不是 adapter 选型、ADR、产品发布承诺或测试通过声明。

PDF 保持“原生文本 PDF”承诺，不能缩写成仅支持本项目生成器。当前实现是有限扫描器；在以下子集内，正确
提取优先于尽力而为：不接受乱码、不可达对象文本或把可知的解码缺口伪装成无文本。OCR、手写识别、表格/版面
恢复、Portfolio、XFA、JavaScript、签名验证和用户密码输入仍为当前范围外。

## 支持、拒绝与状态

| 类别 | 预注册结果 | 语义与稳定码 |
| --- | --- | --- |
| 普通直接页内容中的文本 | `indexed` | 返回规范化文本；代表样本必须保留预期 token。 |
| Flate 内容流、literal/hex、`Tj`/`TJ`/转义 | `indexed` | 与普通文本同等为 MUST，不得因压缩而降级。 |
| Type0 + `ToUnicode` 的中文 | `indexed` | 以 Unicode token 断言；乱码或原始字码均为失败。 |
| 真正无可提取文本的扫描/空白页 | `noTextLayer` / `extract.pdf_no_text` | 仅在已完成页面可达性与文本解码判定后使用。 |
| 已识别但当前 adapter 不支持的 filter、对象结构或字体映射 | `unsupported` / `extract.unsupported` | 无正文；不得返回 `noTextLayer`。该 status 和码已由 `extractor.go` 定义。 |
| 损坏 PDF、损坏 stream/filter 或无法分类的解析错误 | `failed` | 无部分正文，保留既有稳定错误码或经实现评审新增的专用码。 |
| 加密/需要密码 | `passwordProtected` / `extract.password_required` | 当前不接收密码，不能静默绕过。 |
| 输入、单流/累计解码、输出、deadline 超限 | `resourceLimited` 或 `truncated` | 输入/解码/时间为 `resourceLimited`；仅输出上限截断为 `truncated`。 |
| 调用方取消 | `cancelled` | 不接纳部分正文。 |

现有扫描器会在零 rune 时直接返回 `noTextLayer/extract.pdf_no_text`；这不是足以证明“无文本”的判据。
实现须在无法解码但已知有可达文本时选择 `unsupported` 或 `failed`，不得用该状态掩盖 gap。

## 页面边界与代表性 corpus

资格样本须可再分发或由测试生成，并预注册最小 token、预期状态和预算。MUST 样本至少包括：普通文本、Flate
和 `TJ`/转义、Type0 + `ToUnicode` 中文、真实无文本页、损坏 Flate、输入/解码/输出/取消边界；以及页面可达性
负样本，断言 unreferenced object、metadata、embedded file、annotation 和不可达 page 的文本绝不进入索引。

对象流、xref stream、Form XObject、多 `/Contents`、filter chain、无 `ToUnicode`、RTL/ligature、security
handler 与深层/循环对象先列为 DISCOVERY：要么正确提取，要么按上表明确拒绝，绝不以乱码或假 `noTextLayer`
通过。升级为 MUST 前须写明用户承诺与 oracle，避免 corpus 反向扩大范围。

已有独立证据显示：当前实现会索引页面外对象的文本，并把可由 oracle 解码的 ASCIIHex 页面文本报为
`noTextLayer`；这已满足“进入决策”的条件，但尚无合格引擎或采纳结论。当前基础合同还验证了普通文本、Flate
与 Type0 中文成功路径；候选探针的 Type0 乱码是该候选不合格的功能观察，不是产品能力结论。

## 资源、事务与下一验收

沿用现有边界：64 MiB 输入、32 MiB 单解码流、256 MiB 累计解码、2,000,000 code points 与默认 30 秒。
候选或扩展实现必须测量 wall time、CPU、Peak RSS、输出量和取消/终止延迟；没有可证明及时停止的库应放入每任务
worker，并以进程边界实施预算。未实测前这些是验收项，不是已达成指标。

单个 source 的 `unsupported`、`failed`、资源受限或密码状态可以作为该 source 的新派生结果写入成功 rebuild；
它们不写回 PocketBase 权威数据。只有 rebuild 级失败或取消才回滚 staging writes，使读者继续看到旧 generation。
因此“单文档拒绝”与“rebuild 事务失败”不得混为同一语义。

下一实现必须以同一 corpus 验收：上述 MUST 的精确状态/token、页面外文本零召回、无乱码/假无文本、既有预算与
取消语义、以及成功/失败 rebuild 的 generation 行为。对任何拟采用 adapter，另行完成正确性、资源、许可、离线
分发、Windows x64 release candidate、NOTICE/SBOM 和包体差值门禁；本策略不预选 PDFium、收费方案或新增依赖。
