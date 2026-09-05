# PdfPig 候选资格发现记录（A6，2026-09-06）

## 范围与状态

这是基于 `main@770fb96e` 的隔离原型观察，不是产品资格报告。未修改产品项目、锁文件、workflow 或
发布资产，未采纳适配器。[ADR 0014](../adr/0014-pdf-extraction-adapter-qualification.md)记录待接受的决策。

原型固定 PdfPig 0.1.16、SharpZipLib 1.4.2，使用 .NET 10；独立 Poppler 仅作为本地 oracle。
临时源码、生成样本和原始结果尚未纳入版本化 CI；以下实测不能替代采用前的可重放 corpus 与正式门禁。

## 主要观察

| 边界 | 原始或反面证据 | 隔离探针结果及限制 |
| --- | --- | --- |
| 普通文本、Flate、Tj/TJ、中文 | 基础生成 corpus 的 UTF-16 BOM 样本不证明字体映射 | 非恒等字码 ToUnicode 返回“数据工作”，与 Poppler 一致。 |
| 损坏 Flate | 默认 PdfPig 把一份坏流当空正文；BCL ZLibStream 接受缺最后四字节 trailer 的流 | `Inflater(false)` 完整结束判断拒绝缺尾 1–4 字节、错 Adler、坏 header；空合法流仍成功。 |
| zlib 窗口 | 固定 SharpZipLib 原始解码接受 CINFO=8 | 检查 CM=8、CINFO≤7 后拒绝；这是既有 zlib 协议校验，不增加源码或产物 hash。 |
| xref stream | PdfPig 将声明 Length=32480 的流连同边界外 LF 交给 filter，输入成为32481字节 | 仅第一 filter、Type XRef、直接整数 Length 且其外只有 LF/CRLF 时按 Length 取流；声明内尾随数据和错误 Length 仍拒绝。 |
| 字体映射缺口 | 少一个字码映射时返回 U+0004 并发出 warning；Poppler 只返回可映射文本 | 候选 warning 拒绝策略返回失败且正文为空。是否保守误拒仍待覆盖；不按 warning 文案分类。 |
| 无 ToUnicode | 不能仅按该字段缺失认定乱码 | 一份 Adobe 字体映射样本与 Poppler 可见 token 一致，仍需区分已知映射与缺口。 |
| 页面外文本 | 现扫描器不能按对象可达性界定正文 | 同文件含 metadata、附件、批注及 AP、未列入 Kids 的页面；两个 oracle 都只返回可见页正文。 |
| 单流/累计 | 代码存在不等于边界已验证 | 33 MiB 流拒绝；9页各32 MiB在第九流触发256 MiB累计限制，stdout为空。只证明 Flate 路径。 |

最后一次 warning 拒绝探针对27份发现样本逐项比对预注册进程退出结果，无意外变化；这包括有意拒绝的
损坏/超限样本，不等于27份产品状态契约全部通过。输出截断、其他 filter/predictor、加密、真实扫描件、
深层/循环对象、产品取消和 generation 事务仍未完成资格。

## 独立公开生产者

- [W3C Dummy PDF](https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf)：两者均返回
  `Dummy PDF file`，没有 warning。
- [CTeX 官方手册](https://mirrors.ctan.org/language/chinese/ctex/ctex.pdf)：下载样本1,426,774字节、195页，
  文档版本2.6.5；“宏集手册”“中文排版”“标题文字汉化”“数字日期转换”四个预注册 token 在 Poppler、
  默认 PdfPig 和修正 xref 边界后的严格探针中均命中，严格探针无 warning。

这些 PDF 仅保留在本地，未提交副本。CTAN [包页](https://ctan.org/pkg/ctex)标明 LPPL 1.3c；
再分发前仍需核对完整文档许可资产。记录 URL、版本、页面和 token，不把再次下载或普通本地 hash 当资格。

## 资源与退出测量

前两行取自最后一轮27样本 warning 拒绝探针，原始记录另存为
`strict-admission-results-20260906-final-observations.json`；取消行取自独立的
`cancellation-discovery.json`。不同轮次的数值不混用，也不将波动解读为优化收益。

| 场景 | Wall time | CPU | 峰值工作集 | 结论范围 |
| --- | --- | --- | --- | --- |
| 9页累计 Flate 超限 | 1244 ms | 1328.125 ms | 244,465,664 B | 触发256 MiB累计拒绝，没有部分正文。 |
| CTeX 195页严格提取 | 1346 ms | 1515.625 ms | 110,067,712 B | 本机单次发现值，不作跨机器性能承诺。 |
| 提取开始后50/200/500 ms终止自有探针进程 | 取消到退出5.27/6.25/8.80 ms | 未测 | 未测 | 三次取消前均存活，退出后stdout空；不是产品进程树或任务结算验收。 |

8个候选依赖 DLL 的普通构建总量为5,979,648字节；不含最终worker/运行时/NOTICE，不能冒充发布ZIP差值。

## 维护与上游证据

- [PdfPig 0.1.16](https://www.nuget.org/packages/PdfPig/0.1.16)和
  [固定源码](https://github.com/UglyToad/PdfPig/tree/v0.1.16)：严格 parse 开关不保证所有 filter/字体失败
  都作为错误返回；公开 filter provider 和日志接口提供适配 seam，但必须保留版本升级回归。
- [SharpZipLib 1.4.2](https://www.nuget.org/packages/SharpZipLib/1.4.2)的
  [Inflater](https://github.com/icsharpcode/SharpZipLib/blob/v1.4.2/src/ICSharpCode.SharpZipLib/Zip/Compression/Inflater.cs)：
  需区分完整结束、缺输入、需字典和无进展；返回零字节本身不是成功 EOF。
- [RFC 1950 §2.2](https://www.rfc-editor.org/rfc/rfc1950.html#section-2.2)约束 zlib 的方法、窗口和 trailer。
  字典及声明内尾随数据在当前探针拒绝；正式 adapter 政策须在资格中明确。
- 既有[方案比较](2026-09-04-pdf-extraction-adapter-options.md)继续有效；纯 Go候选在已测 Type0 中文样本上的乱码
  不能用字面 BOM 样本覆盖，native/JVM/商业方案的离线和许可成本也不能被“更成熟”代替。

## 固定包的分发材料核对

本机 `net10.0` 探针的 assets 图只有 PdfPig 0.1.16 和 SharpZipLib 1.4.2 两个 NuGet 包，分别选择
`net9.0` 和 `net6.0` 资产；不能把旧目标框架的依赖组计入本次实际闭包。包元数据分别声明 Apache-2.0 和
MIT，并绑定下表固定源码。这里只核对来源和分发材料，不宣称许可、NOTICE 或正式 SBOM 已通过发布资格。

| 材料 | 已核事实 | 采用前的分发检查 |
| --- | --- | --- |
| PdfPig [LICENSE](https://github.com/UglyToad/PdfPig/blob/a7bb35662bbbf405efddad50aedc9bcdcf515afc/LICENSE) / [NOTICES.txt](https://github.com/UglyToad/PdfPig/blob/a7bb35662bbbf405efddad50aedc9bcdcf515afc/NOTICES.txt) | 固定源码保留 Apache、PDFBox/FontBox 和 Adobe 归属说明；NuGet 文件清单没有独立 LICENSE/NOTICES 文件。 | 在候选材料清单中明确保留位置，不能仅以 NuGet license expression 代替分发材料。 |
| Adobe 字形表 | 固定包 `UglyToad.PdfPig.Fonts.dll` 确有 `glyphlist` 和 `zapfdingbats` 嵌入资源；[原始头部](https://github.com/UglyToad/PdfPig/blob/a7bb35662bbbf405efddad50aedc9bcdcf515afc/src/UglyToad.PdfPig.Fonts/Resources/GlyphList/glyphlist)包含二进制再分发的归属、条件和免责声明要求。 | 核对两份原始声明在分发文档或材料中的保留；根目录 NOTICES 的归属摘要不等于完整声明。 |
| Adobe AFM | [Fonts 项目](https://github.com/UglyToad/PdfPig/blob/a7bb35662bbbf405efddad50aedc9bcdcf515afc/src/UglyToad.PdfPig.Fonts/UglyToad.PdfPig.Fonts.csproj)嵌入 AFM 与 `MustRead.html`；DLL 资源清单确认二者存在。 | [MustRead](https://github.com/UglyToad/PdfPig/blob/a7bb35662bbbf405efddad50aedc9bcdcf515afc/src/UglyToad.PdfPig.Fonts/Resources/AdobeFontMetrics/MustRead.html)要求保留归属、伴随说明及修改声明；不得因输出目录没有独立 HTML 就误报 DLL 缺少它。 |
| SharpZipLib [LICENSE.txt](https://github.com/icsharpcode/SharpZipLib/blob/33f64eb0f28cdd2b084cb822fcc224c7c5aba553/LICENSE.txt) | 固定源码提供 MIT 完整原文；NuGet 使用 license expression，未附独立文本。 | 保留该版本原文及归属，不手写替代其年份或文本。 |

原文和实际 DLL 资源清单保留为本地资格证据，未复制到产品或提交第三方资源。最终 worker 的资产集合、裁剪
结果和 SBOM 尚未确定；采用时仍需以实际离线发布候选核对，不能用当前两个包的图代表未来完整发布闭包。
下一步应将发现样本转为版本化、可独立核对的资格入口，补齐剩余边界后决定是否接受 ADR。
