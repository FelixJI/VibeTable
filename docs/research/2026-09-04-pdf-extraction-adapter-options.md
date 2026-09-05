# PDF 文本提取 adapter 方案取证（A6.4，截至 2026-09-04）

## 1. 目的、边界与结论口径

本文件落实
[`A6：PDF 提取能力决策`](../plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md#a6pdf-提取能力决策)
中的方案取证，只比较当前有限 parser 与可完全离线、Windows 10/11 x64 可交付的成熟引擎；不选型、不引入
依赖，也不把“上游支持 PDF”推导成“已经满足 VibeTable 的产品契约”。

结论先行：

1. 当前实现是有明确资源边界的**有限内容流扫描器**，不是通用 PDF parser；零依赖、取消及时和失败码稳定
   是真实优势，但字体映射、对象流和 filter chain 不在其结构模型内。
2. MuPDF、PDFium、Poppler、PDFBox 都有页级文本 API，理论覆盖面明显更宽；但许可证、原生/JVM 打包、
   硬取消和包体都不是免费收益，必须以同一 corpus 和 release candidate 实测。
3. 更贴合 Go sidecar 的官方维护方案中，UniPDF 是纯 Go 且有离线许可路径，但属于商业 EULA 产品，公开示例
   未给出 `context.Context`/硬资源上限契约；pdfcpu 为 Apache-2.0 纯 Go parser，却没有公开的成品纯文本提取
   API。其官方当前声明兼容所有 PDF 版本，但 PDF 2.0 validation 仍是 basic/持续改进，不能当作即插即用替代。
4. 遵循 PR140 的停止条件：**只有 MUST corpus 以可复现证据证明当前实现无法满足已声明支持范围，才启动
   替换/扩展的 proposed ADR**。本研究不授权替换；支持子集与拒绝语义见
   [资格策略](../plans/2026-09-05-pdf-support-qualification.md)。

外部事实只引用 PDF 规范、项目官方文档、官方源码/发行页和许可证原文。工程判断均显式写成“判断”或
“待验证”。许可证段落不是法律意见；真正分发前仍需基于锁定版本、精确构建配置和完整 NOTICE/SBOM 评审。

## 2. PDF 规范给 corpus 的最低维度

PDF 1.7 已足以证明“搜索 `Tj`/`TJ` 和单层 Flate”不是通用解析模型：

- `/Filter` 可以是单个名称或数组，数组表示依次执行的解码 pipeline；规范示例直接组合
  `ASCII85Decode` 与 `LZWDecode`。[Adobe PDF Reference 1.7 §3.3](https://opensource.adobe.com/dc-acrobat-sdk-docs/pdfstandards/pdfreference1.7old.pdf)
- PDF 1.5 起可把非流对象放进 `/ObjStm`，对象位置由 cross-reference stream 描述；只扫描顶层字节无法可靠
  还原这类间接对象。[Adobe PDF Reference 1.7 §3.4.6–3.4.7](https://opensource.adobe.com/dc-acrobat-sdk-docs/pdfstandards/pdfreference1.7old.pdf)
- 字符码到 Unicode 不是 UTF-8 猜测：simple font 的 Encoding/Differences、Type 0/CIDFont 的 CMap，及
  `/ToUnicode` CMap 都会影响文本语义。最新 PDF 2.0 还包含对 PDF 1.7 共通条款的澄清，因此 corpus 设计
  应同时参考公开的 ISO 32000-2:2020 与 errata。
  [PDF 规范档案](https://pdfa.org/resource/pdf-specification-archive/)、
  [ISO 32000-2:2020 入口](https://pdfa.org/resource/iso-32000-2/)
- encryption dictionary、security handler 和 crypt filter 决定字符串/流能否解密，不能靠发现字面量
  `/Encrypt` 判定全部语义。[Adobe PDF Reference 1.7 §3.5](https://opensource.adobe.com/dc-acrobat-sdk-docs/pdfstandards/pdfreference1.7old.pdf)

因此 A6 corpus 至少要把“文本层存在”与“当前实现恰好能看见明文字节”分开，并独立覆盖字体编码、对象流、
多级 filter、加密/权限、损坏、资源超限和纯图像页。

## 3. 当前实现的可执行事实

权威代码为 `sidecar/internal/workspacesearch/extractor.go`：

- 入口先把输入限制为 64 MiB，默认 30 秒 deadline；PDF 单个解码流 32 MiB、累计解码 256 MiB、输出
  2,000,000 Unicode code points。状态已经区分 `indexed`、`unsupported`、`failed`、`truncated`、
  `passwordProtected`、`noTextLayer`、`resourceLimited` 与 `cancelled`。
- PDF 路径以 regexp 找直接出现的 `Tj`/`TJ` 字符串，并只发现字典中出现 `/FlateDecode` 的流；zlib 解码
  有单流/累计上限，扫描与读取过程中检查 context，输出 accumulator 最多保留 `limit + 1` 个 rune。
- 它不解析 trailer/xref/page tree、对象流、page resources、Form XObject、字体字典/CMap/`ToUnicode`，也
  不执行 filter 数组。literal/hex string 只接受 UTF-8 或 UTF-16BE BOM；其他编码会被跳过。
- `bytes.Contains(payload, "/Encrypt")` 是保守拒绝，不是 encryption dictionary 解析。类似地，找不到
  可解码 token 就返回 `noTextLayer`，当前证据无法区分“真正纯图像”与“存在文本但编码/对象/filter 不受支持”。

判断：现实现只能在 corpus 证明的原生文本 PDF 子集内作出承诺，不应把“生成器可控”误写成产品范围。
它的最强资产是稳定失败语义和原生 `context` 检查；任何成熟 adapter 都必须保留这两项，不得只比较召回率。

## 4. 候选引擎矩阵

表中“完整 parser 路径”表示候选会解释页面对象/内容流，而非 VibeTable 当前 regexp；它仍是**待 corpus 验证**，
不是对所有 PDF、字体或阅读顺序的保证。

| 方案 | 文本层、字体、对象/filter | 加密与无文本 | 取消与资源预算 | Windows x64、包体、许可与维护判断 |
| --- | --- | --- | --- | --- |
| **当前有限 parser** | 直接 `Tj/TJ`、literal/hex、单层 Flate；不理解 CMap、ObjStm、page resources、filter 数组 | 字节命中 `/Encrypt` 即拒绝；空结果可能误报 `noTextLayer` | 输入/单流/总解码/输出/deadline 都已有界；Go context 检查可协作取消 | 纯 Go、零新增包体/许可面；扩展到通用 PDF 会把 sidecar 变成自研解析器 |
| **MuPDF 1.28.x** | `fz_run_page` 把解释后的页送入 structured-text device；选项包含 ligature、ActualText、未知 Unicode 用 CID/GID 等策略，适合验证复杂字体与阅读结构。[C API 概览](https://mupdf.readthedocs.io/en/1.28.3/reference/c/introduction.html)、[Structured Text options](https://mupdf.readthedocs.io/en/1.28.3/reference/common/stext-options.html) | 有 `fz_needs_password`/`fz_authenticate_password`；基础 text-only 配置应禁用可选 OCR。空 structured text 本身不足以区分纯图像、解析遗漏或 Unicode mapping 失败，须按预注册 policy 结合诊断证据分类。[Document API](https://mupdf.readthedocs.io/en/1.28.3/_static/generated/c/html/fitz_2document_8h.html) | `fz_cookie.abort` 可中止 page run，但官方明确“不保证多久后停止”；`mutool draw` 有内存 limit/low-memory 模式。**判断**：该证据不足以证明满足 VibeTable 的硬 deadline，候选原型仍需隔离进程并实测终止延迟。[cookie](https://mupdf.readthedocs.io/en/1.28.3/_static/generated/c/html/structfz__cookie.html)、[mutool draw](https://mupdf.readthedocs.io/en/1.28.3/tools/mutool-draw.html) | 官方提供 VS solution，工具可独立运行；必需依赖含 FreeType/HarfBuzz/JPEG/OpenJPEG/zlib 等。上游为 AGPL 或商业许可，与本仓 MIT 分发不能未经许可直接嵌入。包体增量待精确裁剪构建实测。[安装](https://mupdf.readthedocs.io/en/1.28.3/guide/install.html)、[第三方库](https://mupdf.readthedocs.io/en/1.28.3/other/third-party.html)、[许可](https://mupdf.readthedocs.io/en/1.28.3/license.html)、[发行历史](https://mupdf.com/releases/history) |
| **PDFium** | embedder public API 按页加载并输出 UCS-2/Unicode，能逐字符报告 Unicode map error；但 `FPDFText_HasUnicodeMapError` 在锁定 header 中仍标为 experimental，不能成为稳定错误分类的唯一依据。原型必须锁定 header、加兼容性探针，并保留由 corpus 结果归类的替代路径。完整 parser 路径仍需用对象流/filter/font corpus 验证。[`fpdf_text.h`（`a043bed`）](https://pdfium.googlesource.com/pdfium/+/a043bed4a0d729e474494a5f28c3d1457e06af8e/public/fpdf_text.h) | load API 接受可选密码，错误码区分 format/password/unsupported security；零字符与 mapping error 须分别保留诊断证据，最终状态按预注册 policy 分类。[`fpdfview.h`（`a043bed`）](https://pdfium.googlesource.com/pdfium/+/a043bed4a0d729e474494a5f28c3d1457e06af8e/public/fpdfview.h) | 自定义 random-access callback 可限制输入读取，但 text API 没有等价于 Go context 的硬终止契约；API 非 thread-safe。判断：若原型，单任务隔离进程比在 sidecar 内加全局锁更可控 | 官方默认支持 Windows x64，但使用 Chromium 的 depot_tools + GN/Ninja + Clang，V8/XFA 默认开启、可关闭；上游提供源码而非面向本产品的固定 DLL。包体和 third-party NOTICE 必须由锁定构建产物实测/枚举。顶层许可含 BSD-style 与 Apache-2.0 文本。[构建/嵌入说明（`a043bed`）](https://pdfium.googlesource.com/pdfium/+/a043bed4a0d729e474494a5f28c3d1457e06af8e/README.md)、[LICENSE（`a043bed`）](https://pdfium.googlesource.com/pdfium/+/a043bed4a0d729e474494a5f28c3d1457e06af8e/LICENSE) |
| **Poppler** | C++ API 有 page `text()`/`text_list()` 和 font 信息，`pdftotext` 是成熟完整 parser 工具；但官方特别警告 `text_list()` 未在 Asian scripts 测试，复杂 CJK 必须进 corpus。[page API](https://poppler.freedesktop.org/api/cpp/classpoppler_1_1page.html)、[font API](https://poppler.freedesktop.org/api/cpp/classpoppler_1_1text__box.html) | document API 能报告 encrypted/locked 并接受 owner/user password；不提供 OCR，空文本仍需产品侧归类。[document API](https://poppler.freedesktop.org/api/cpp/classpoppler_1_1document.html) | 公共 C++ page text API 没有 deadline/memory budget 参数；判断：只能把 `pdftotext`/窄 worker 当作可终止进程，并限制 stdin/stdout/工作集 | 上游发行 source tarball 和单独 `poppler-data`，官方 CI 主路径是 Linux/Fedora MinGW，Windows AppVeyor 被标作 non-official；VibeTable 要自有 Windows 构建、DLL/data 清单与更新链。源码/许可核对基点是已存在的 `poppler-26.08.0` tag（peeled commit `d663f8fe0237073fadf43e4e19acc46b3ba3e80a`）；该版本 README/COPYING 表明项目采用 GPL，并对链接/调用其库提出 GPL 要求，因此嵌入或链接的许可风险很高。分发并启动独立 `pdftotext` 进程是否改变产品侧义务，本文不作法律结论，必须按锁定版本的 EXE/DLL/data/NOTICE/源码提供方式及进程边界交法律评审。包体待实测。[官网/发行/CI](https://poppler.freedesktop.org/)、[README（`poppler-26.08.0`）](https://gitlab.freedesktop.org/poppler/poppler/-/blob/d663f8fe0237073fadf43e4e19acc46b3ba3e80a/README.md)、[COPYING（`poppler-26.08.0`）](https://gitlab.freedesktop.org/poppler/poppler/-/blob/d663f8fe0237073fadf43e4e19acc46b3ba3e80a/COPYING) |
| **Apache PDFBox 3.0.8** | `PDFTextStripper` 解释内容流与 TextPosition；PDF 是图形格式，默认内容顺序不等于阅读顺序。PDFBox 3 按需解析并可用 file-backed random access，适合对象流/filter/font corpus 对照。源码证据锁定官方 `3.0.8` tag，并以官方下载页核对当前 3.0.x 版本为 3.0.8。[PDFTextStripper 3.0.8](https://github.com/apache/pdfbox/blob/3.0.8/pdfbox/src/main/java/org/apache/pdfbox/text/PDFTextStripper.java)、[3.0 migration](https://pdfbox.apache.org/3.0/migration.html)、[官方下载](https://pdfbox.apache.org/download.html) | CLI 支持 password；官方 FAQ 明确扫描页可能只有图像，自定义编码也可能得到乱码，不能把非空当作正确 Unicode。[CLI](https://pdfbox.apache.org/3.0/commandline.html)、[FAQ](https://pdfbox.apache.org/3.0/faq.html) | 官方安全模型明确畸形 PDF 可造成无限循环或过量 CPU/内存，要求调用方设置 timeout、memory/resource controls 和 sandbox。判断：JVM `-Xmx` + 可杀 worker，而非同进程 future cancel，才是硬边界。[Security](https://pdfbox.apache.org/security.html) | `3.0.8` tag 的许可证为 Apache-2.0；3.0.x 最低 Java 8，核心还需 FontBox、pdfbox-io、commons-logging，某些图像/公钥能力有额外可选依赖。Windows 无原生 ABI，但要随产品交付 JRE/JAR；真实压缩包增量和启动开销待 release build 实测。[依赖](https://pdfbox.apache.org/3.0/dependencies.html)、[LICENSE 3.0.8](https://github.com/apache/pdfbox/blob/3.0.8/LICENSE.txt) |
| **UniPDF（Go 商业候选）** | 官方提供纯 Go parser 与多种文本提取模式，支持文本/字体/位置提取、xref repair、encoding 与加密；是否覆盖本 corpus 的 CMap/ObjStm/filter 组合仍须黑盒验证。[README（`v4.10.0`）](https://github.com/unidoc/unipdf/blob/v4.10.0/README.md)、[v4 extraction modes](https://docs.unidoc.io/unipdf/v4-migration-guide/)、[core 源码（`v4.10.0`）](https://github.com/unidoc/unipdf/blob/v4.10.0/core/core.go) | 可打开/解锁加密 PDF；OCR 是另行配置的网络 client，本路线应禁用。使用 offline key 时解析和许可校验都可零外联。[离线行为](https://docs.unidoc.io/unipdf/faq/security/does-unipdf-send-my-documents-anywhere/) | `v4.0.0` 官方示例是同步逐页 `ExtractText()`，当前公开接口证据没有 context/deadline/硬内存上限。判断：即使纯 Go，仍需 worker 隔离或供应商提供可验证的中止契约。[提取示例（`v4.0.0`）](https://github.com/unidoc/unipdf-examples/blob/v4.0.0/extract/pdf_simple_extraction.go) | 无 C/C++/JVM，最容易链接进 Go sidecar，包体可能低于三种 native/JVM 方案但必须实测。它是商业 EULA 产品，运行前需 license key；offline key 可 air-gapped，源码可见性随许可层级变化。[安装](https://docs.unidoc.io/unipdf/faq/setup/installation/)、[许可加载](https://docs.unidoc.io/unipdf/faq/setup/how-to-load-unipdf-license-key/)、[许可层级](https://unidoc.io/pricing/)、[EULA](https://unidoc.io/eula/) |

### 不作为成品 adapter 的 Go 对照

[`pdfcpu` README（v0.14.0）](https://github.com/pdfcpu/pdfcpu/blob/v0.14.0/README.md) 声明它是
Apache-2.0、纯 Go PDF processor，兼容所有 PDF
版本，并明确 PDF 2.0 validation 仍是 basic/持续改进。它能解析、校验、解密并导出原始 page
content/font，但官方命令/API 清单没有“输出阅读文本”的成品能力，因此可作 parser/validation 事实对照，
不能替代字体映射与阅读顺序提取器。`v0.14.0` 改为标准库错误链并调整 extraction 的错误/清理策略；若把它
用于原型，错误分类必须使用 `errors.Is`/`errors.As` 或 adapter 自有映射，不能比较完整错误字符串。
[`pdfcpu` API（v0.14.0）](https://github.com/pdfcpu/pdfcpu/blob/v0.14.0/pkg/api/api.go)、
[`extract` 命令定义（v0.14.0）](https://github.com/pdfcpu/pdfcpu/blob/v0.14.0/cmd/pdfcpu/usage.go)、
[`v0.14.0` 发行页](https://github.com/pdfcpu/pdfcpu/releases/tag/v0.14.0)

**判断**：第三方 Go binding（例如给 MuPDF/PDFium 加 cgo wrapper）不会消除底层引擎的许可、C/C++ 更新、
DLL/静态库来源、NOTICE、崩溃隔离和包体风险；具体影响仍须按 binding 与锁定 native 产物共同评审。本轮
不把非上游 binding 的便利性当作引擎成熟度证据。

## 5. 统一原型契约：先固定 seam，再比较引擎

若 corpus 触发 ADR，所有原型必须实现同一窄 adapter，而不是让上游错误/文本格式泄漏到 WorkspaceSearch：

```text
ExtractPDF(ctx, boundedInput, limits)
    -> {status, normalizedText, stableErrorCode, diagnostics-for-test-only}
```

### 5.1 当前已实现或已声明的不变量

1. 输入先由 VibeTable 限为 64 MiB；输出最多 2,000,000 code points；默认 30 秒。引擎的“能打开”不能
   越过产品上限。
2. 当前 extractor 已有 `indexed`、`unsupported`、`failed`、`truncated`、`passwordProtected`、`noTextLayer`、
   `resourceLimited`、`cancelled` 状态和稳定错误码；达到输出上限为 `truncated`，deadline 为
   `resourceLimited`，用户取消为 `cancelled`。
3. 当前产品没有 PDF 密码输入，现 parser 只要字节命中 `/Encrypt` 就返回 `passwordProtected`；adapter
   不得在未修改公开契约前静默扩大为接收密码。A6 当前范围不引入 OCR。
4. WorkspaceSearch 的 Rebuild 在单一事务内替换派生 corpus；验证完成并 commit 前读者保留旧 generation，
   取消或失败会回滚 staging writes，不提交部分 rebuild。

### 5.2 支持范围 policy / ADR 待决

- 加密 PDF 是否支持 user/owner password、是否执行 copy permission，以及空口令行为，尚未冻结。
- 真正无文本、混合页、存在 text object 但 Unicode mapping 失败、局部页失败各自映射为
  `noTextLayer`、既有 `unsupported` 或 `failed`；精确分类与是否允许部分成功须由 policy/ADR 预注册，不能由
  候选默认行为决定。
- reading order、连字/空白/换行规范化和损坏 PDF 的容错边界同样待 corpus 与 ADR 决定。

### 5.3 候选原型必须提供的工程边界

1. 对外状态与稳定错误码由 VibeTable adapter 统一生成，上游异常或工具输出不得直接泄漏到
   WorkspaceSearch。
2. 协作取消不是硬上限。任何未提供可证明及时中止的库都放入一次一任务的子进程；Windows 以进程/Job
   Object 约束 wall time、CPU、working set 和进程树，stdout 使用 bounded reader。单文档 worker 超限时
   终止该任务，返回稳定 `resourceLimited` 且不接纳部分正文；一次成功的 rebuild 仍以该状态替换对应 source。
   只有 rebuild 级失败或取消才回滚 staging writes，使 active generation 整体保持不变。
3. 每个候选都从**锁定版本的官方源码/发行物**构建；记录精确 DLL/JAR/data/NOTICE/SBOM 清单。包体只报告
   `release ZIP(candidate with adapter) - same-commit baseline ZIP` 的实际差值，不用仓库大小或下载包猜测。

## 6. Corpus 与 ADR 触发/停止条件

### 6.1 Corpus 分层与预注册

每个样本必须可再分发或由测试生成，记录构造来源、预期状态、最小必要 token/顺序断言和资源预算；普通
源码/fixture 不新增重复 hash。外部跨系统样本仅在仓库已有 checksum 契约要求时沿用该契约。以下是
**corpus 必须覆盖的能力轴，不表示每个轴都由本文宣布为产品 MUST**；支持范围 policy 必须在看到候选结果前，
逐样本预注册 `MUST`、`MAY` 或 `OUT-OF-SCOPE`。新增复杂样本先作为 `DISCOVERY`，除非策略明确升级为 MUST，
不得反过来制造替换触发器。

**能力轴**：

- xref table 与 xref stream；直接对象与 `/ObjStm`；单/多 content stream、Form XObject；
- page tree、页面 `/Contents` 与允许的 Form 可达性；未引用对象、注释/metadata 和非页面 stream 中伪造文本的
  负样本必须断言不进入索引，防止只扫描整个 payload 的实现误召回；
- literal/hex、嵌套括号/escape；`Tj`、`TJ`、`'`、`"`；
- WinAnsi/MacRoman、Differences、Type0/CIDFont，带/不带 `ToUnicode` 的 Latin、CJK、RTL、ligature；
- Flate、ASCIIHex、ASCII85、LZW、RunLength，以及至少两种 filter chain；
- 未加密、需要密码、unsupported security handler、损坏 xref/stream/filter；
- 真正纯图像页、混合图像+文本页、零页/空白页；
- 接近输入/单流/总解码/输出限制，解压放大、深对象/循环引用、取消和 deadline。

其中 OCR、手写、版面/表格结构恢复、PDF Portfolio 内嵌文件、XFA、JavaScript、数字签名验证、用户提供
密码在当前计划下默认是 `OUT-OF-SCOPE`。它们可以验证安全拒绝，但不能因“不提取”判定当前实现失格。
其余能力是否为 MUST，由单独的支持范围 policy 决定；本文不借研究扩大产品承诺。

### 6.2 触发 ADR 的充分证据

以下证据足以让 A6 从研究进入**决策**：当前实现对一个已预注册 MUST 样本稳定地失败、误分类或超预算，且根因落在声明范围；独立 oracle/生成器能够复现预期，排除 fixture 或阅读顺序歧义。若修复需要新增通用 xref/object/font/filter/security 子系统，决策输入必须说明其接口、测试和长期维护面，不能以“几行 patch”低估。

这不是采用候选的门槛。候选的逐样本正确性、CPU/RSS/取消延迟、崩溃隔离、锁定版本许可/NOTICE/SBOM、Windows x64 构建、release ZIP 差值、冷启动、离线运行与更新责任，均是**采用及发布前**必须完成的资格门禁；商业候选另须说明可分发范围、offline key 生命周期和失效处理。不要求候选先完成全量 build 才能记录架构决定：当前实现已不满足声明范围时，可形成 proposed ADR，决定局部修复、保留有限 parser 或授权受控原型。

进入决策不表示“应替换”，更不表示任何候选已合格。proposed ADR 可在资格完成前记录架构取舍；只有通过全部资格门禁的方案才可被采用或随产品发布。改变既有“原生文本 PDF”承诺或缩窄产品范围须获单独用户批准，当前路线不采用该分支。

### 6.3 明确停止条件

- 若当前实现通过全部 MUST corpus 和预算：停止替换路线，保留有限 parser，冻结支持范围与拒绝语义；不得
  为“更成熟”引入引擎。
- 若失败仅属于 MAY/OUT-OF-SCOPE：记录产品不支持，不触发 ADR。
- 若成熟候选没有在同一 corpus 上形成实质正确性收益，或无法满足许可、全离线、硬资源边界、Windows 打包
  任一门禁：停止该候选，不以重试/放宽限制换取通过。
- 若证据不足以区分 parser 缺陷、fixture 缺陷和预期文本歧义：先最小化样本并补 oracle，不进入选型。

## 7. 下一步证据清单（不属于本研究实施范围）

1. **已完成基础层**：合并提交 `73e0ad91` 已加入生成的合法 PDF corpus，并由独立 review 及外部工具核对
   xref/Length/endstream/endobj；后续不得让被测 parser 同时充当新增 fixture 的唯一 oracle。
2. 扩展不可达文本、page tree/Form 可达性及其他能力轴；从 PDF 规范可再分发样例和各上游 test corpus 中
   只挑最小、许可清晰样本；不能提交的真实 PDF 只记录
   生成配方或在受控外部资格集运行。
3. 先跑当前实现生成逐项 gap ledger；没有稳定 MUST RED 就不构建任何候选。
4. 若 proposed ADR 授权原型，按其待验证风险选择最少必要的 throwaway worker；原型不是采用前提，任何候选仍须
   在采用/发布前通过本节所列资格门禁。
5. ADR 输入齐全前，不修改 `go.mod`/lock/workflow/release assets，不把临时 binary 或 runtime 纳入产品包。

## 8. 本次核对记录

仓库只读核对：`git status -sb`、`git remote -v`、`rg --files`、A6 计划、既有 WorkspaceSearch P0 研究、
`sidecar/internal/workspacesearch/extractor.go`、测试与 `sidecar/go.mod`。外部只查看本文件已链接的一手网页；
未下载二进制，未运行候选，未测包体，也未改代码、依赖、lock 或 workflow。
