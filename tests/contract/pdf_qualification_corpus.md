# A6 自有 PDF 决策语料 v1

`pdf_qualification_corpus.json` 冻结 20 项样本的 MUST / DISCOVERY 层级、目标状态及文本断言；
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

本入口尚不是产品资格 runner，也没有将候选进程 ExitCode 等同于产品状态。
manifest 的 `remainingCoverage` 保留独立生产者、普通 Tj/TJ、对象流/predictor、图片页、
输入/输出/取消、加密、深层对象与产品 generation 事务等缺口；已有本地发现记录不能替代这些门禁。
预算字段是原有输入/解码/输出/时间契约，不是实测通过声明，也不将累计解码预算冒充进程内存限制。

新增样本先声明目标输入与语义、再运行独立 oracle；保留负证据。A6 是能力决策，产品 adapter、
worker 生命周期、依赖接入与正式发布资格须在接受决策后分别完成。该语料不批准依赖切换。