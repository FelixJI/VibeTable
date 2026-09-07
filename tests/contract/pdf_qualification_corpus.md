# A6 自有 PDF 决策语料 v1

`pdf_qualification_corpus.json` 冻结 23 项样本的 MUST / DISCOVERY 层级、目标状态及文本断言；
`generate_pdf_qualification_corpus.py` 仅用标准库构造 PDF 对象，不复制字体或第三方文件。

```text
uv run --frozen --no-sync python tests/contract/generate_pdf_qualification_corpus.py
```

输出固定在当前 checkout 的 `build/qa/pdf-qualification/v1/`。生成器核对 manifest 的精确文件集合，
不会清理目录；发现额外 PDF 时停止，保留现场。生成成功只证明样本可生成，不证明任何提取器通过。

这些是已有隔离发现样本的版本化来源：非恒等 ToUnicode 中文、缺字映射、页面外文本、
zlib 完整结束与预算、xref 声明 Length 和流外分隔符。正负样本采用同一对象生产者，
错误形态由生成步骤明示，不靠手改二进制、重新下载或文件 hash 建立预期。

预期来自 [支持策略](../../docs/plans/2026-09-05-pdf-support-qualification.md)，不能从候选输出反推。
独立 Poppler 已核对中文、页面可达性和已知字体映射样本；损坏流不能以容错阅读器返回正文作为成功 oracle。
实际运行 adapter 时须记录真实状态、正文和资源测量，逐项比较预期；拒绝、取消时正文必须为空，
indexed 路径必须保留 token 并排除 forbiddenTokens。DISCOVERY 允许显式 unsupported 的条目不等于 MUST。

## 当前 Go 实现的差距报告

```text
go -C sidecar build -o ../build/qa/pdf-qualification/pdf-qualification.exe ./cmd/pdf-qualification
build/qa/pdf-qualification/pdf-qualification.exe tests/contract/pdf_qualification_corpus.json build/qa/pdf-qualification/v1 > build/qa/pdf-qualification/current-go.json
```

命令调用当前实际 `workspacesearch.Extract`，先核对 manifest 预算与实际默认值一致，再逐项输出状态、
错误码、正文 code points、耗时及语义差距。没有完整正文输出；状态/token/页面外文本或拒绝正文断言不符时，
仍写出完整报告并以 exit 1 结束。样本缺失、损坏 manifest 或预算漂移同样非零退出，不产生伪成功报告。
这是手动决策入口，不新增或替换现有 CI 门禁，也不将候选进程 ExitCode 等同于产品状态。

在 main1553ca 的当前提取器上，20 项中 8 项不匹配：非恒等中文、缺字映射、无 ToUnicode 映射、
孤立对象、Flate 尾随数据、xref 声明内尾随数据、xref 短 Length、页面边界排除。前者、孤立对象、
Flate 尾随数据和页面边界属于 MUST。该 RED 是保留的能力差距，不能修改 manifest 或标记 xfail 来刷绿。
报告器自身的测试验证真实损坏 PDF 状态的接纳/错误预期拒绝，以及预算不一致时先于提取拒绝。

本入口尚不是包含 worker、宿主和派生 generation 的完整产品资格 runner。
manifest 的 `remainingCoverage` 保留独立生产者、普通 Tj/TJ、对象流/predictor、
输入/输出精确临界值、deadline/取消、加密、深层对象与产品 generation 事务等缺口；已有本地发现记录不能替代这些门禁。
预算字段是原有输入/解码/输出/时间契约，不是实测通过声明，也不将累计解码预算冒充进程内存限制。

新增样本先声明目标输入与语义、再运行独立 oracle；保留负证据。A6 是能力决策，产品 adapter、
worker 生命周期、依赖接入与正式发布资格须在接受决策后分别完成。该语料不批准依赖切换。
## 图片页、输入和输出超限补充

新增三项 MUST，生成器与 manifest 在观察提取结果前共同声明：

- `image-only-rgb.pdf`：仅绘制自有 2×2 RGB 像素，无字体或文本运算符。独立 Poppler 确認一页、一个图像、正文为空；不是外部扫描件或 OCR 验收。
- `input-over-64m.pdf`：合法图片页后增加未引用的 64 MiB 流，整个输入超过既有预算。预期 `resourceLimited / extract.input_limit`，正文长度 0。
- `output-over-2m.pdf`：可达页面内 4,001 个各含 500 个 B 的 Tj 运算符，每个字符串均小于 32 KiB。预期 `truncated / extract.text_limit`，输出精确 2,000,000 code points，并以 `expectedRepeatedCharacter` 断言每个字符均为 B。固定 PdfPig 独立读出全部 2,000,500 个 B，无 warning；原型尚未执行输出上限，该观察不算候选通过。

可选 `expectedCodePoints` 与 `expectedErrorCode` 由报告器直接核对实际结果；零长度也必须检查。报告器回归证明旧版会漏报错误长度与错误码，新增检查后拒绝该错误预期。另以同长度的错误正文预期验证字符断言，防止只有长度正确便通过。

在 `main@fa2e3f83` 的当前提取器上，图片页和输入超限两项匹配。输出样本虽为 `truncated / extract.text_limit` 且长度 2,000,000，但全文字符断言失败：当前 accumulator 在相邻 Tj token 间增加分隔空格，独立 PdfPig 读取该连续正文则全部为 B。补强断言前的长度检查曾通过，不能覆盖此最终反证；23 项整体保留原 8 项并新增该 MUST 差距，共 9 项不匹配、exit 1。生成器/报告器源码变化不修改产品提取器、预算或拒绝策略。
