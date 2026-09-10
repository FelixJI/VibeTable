# A6 自有 PDF 决策语料 v1

`pdf_qualification_corpus.json` 冻结 32 项样本的 MUST / DISCOVERY 层级、目标状态及文本断言；
`generate_pdf_qualification_corpus.py` 用标准库构造结构样本并读取固定的自有生产者 fixtures，不复制字体或外部文档。

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
manifest 的 `remainingCoverage` 保留独立生产者、对象流/predictor、
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

## 普通与 Flate 文本运算符对照

`text-operators-plain.pdf` 与 `text-operators-flate.pdf` 是同一页面内容流的普通和压缩表示。
预注册 MUST token 为 `Operator (report) \ path`、`Array joined token` 和 `Octal ABC`，分别覆盖
Tj 的括号/反斜线转义、TJ 数组连接和八进制转义；不涉及版面恢复或复杂字距承诺。

独立 Poppler 对两个样本都返回相同三行正文、一页、无图片。沿用当前 Go 报告器与提取器，两个样本均为
indexed、53 code points、无错误码或 token 差距。完整 25 项仍有原 9 项不匹配，exit 1；这只补齐
该成对对照，不关闭其他 remainingCoverage 或 A6。原始本地证据分别为
`build/qa/pdf-qualification/text-operators-poppler.jsonl` 与 `build/qa/pdf-qualification/current-go-25.json`。

## 输入精确预算边界

`input-exact-64m.pdf` 预注册为 MUST：自有图片页以合法注释填充，重新生成对象偏移和 xref，文件精确为
67,108,864 字节。填充不引入额外解码流，预期仍为 `noTextLayer / extract.pdf_no_text`、零正文，
与已有超过预算后 `resourceLimited / extract.input_limit` 的样本形成边界对照。

独立 Poppler 确认一页、一个图片、空正文；当前 Go 报告同样匹配，单次本机耗时 7001ms，不作为性能承诺。
完整 26 项仍有原 9 项不匹配并 exit 1。该项只补充输入精确边界，输出精确值、deadline/取消、外部生产者
及其他 `remainingCoverage` 继续待完成。证据为 `build/qa/pdf-qualification/input-exact-poppler.jsonl`
和 `build/qa/pdf-qualification/current-go-26.json`，未改变生产提取器、预算或 ADR 提议状态。

## 隔离 PdfPig 候选与后续页验证

资格工具和复现命令见 [PDF 候选运行记录](../../docs/research/2026-09-10-pdf-adapter-runtime-qualification.md)。
新增 `output-limit-later-valid-page.pdf` / `output-limit-later-invalid-page.pdf` 共用含 2,000,500 个 B 的首页，
第二页只有 zlib Adler 校验字节不同。正常样本必须截断到精确 2,000,000 个 B；损坏样本必须拒绝且正文为空。
输出预算耗尽不能停止验证后续可达页。保留的旧原型在损坏样本上返回 truncated、无 warning，构成回归反证。

新增 `--observations <manifest> <observations>` 入口只替换观察来源，复用原报告器的状态、token、错误码、
字符及拒绝正文断言；要求精确样本集合、预算一致、有限非负测量和所有进程退出。原产品提取器入口不变。
2026-09-10 的隔离候选在显式 1 GiB Job commit 预算下，28 项比较零差异；该实验预算不是产品默认值，
也不关闭独立生产者、对象流/predictor、加密或产品 generation 等剩余资格。

## 独立生产者及 AES 加密对照

`pdf_producer_fixtures/` 的四份自有小文档由 `generate_pdf_producer_fixtures.py` 生成：
ReportLab 4.4.9 普通页和 ASCII85Decode + FlateDecode 压缩页，以及 pypdf 6.10.0 / cryptography 50.0.1
生成的两类 AES-256 加密页。正文只有自有 ASCII token，使用 Base14 字体，无嵌入字体或外部内容。
普通生成与 CI 不需要这些生产者包，只读取四份固定 fixtures；重新生成需要符合脚本版本检查的资格工具环境。
密文包含正常随机性，可重放的是语义与预期，不承诺重新生成后字节一致。

两个普通样本是 MUST indexed 且保留 token；两个加密样本必须 passwordProtected / extract.password_required、
零正文，包括空 user password 但非空 owner password。旧候选在后一项返回 indexed，修复后在页面读取前检查
PdfDocument.IsEncrypted。最终 fixtures 的普通正文由 PDFium 核对，加密正文通过已知测试密码验证。

2026-09-10 最终32项比较 failed=0、exit0，原28项预期未改；该结果仍不替代真实复杂生产者、其他security handler、
对象流/predictor及产品generation事务资格。
