# ADR 0012：文档差异采用结构化内置引擎与隔离 Provider

- 状态：已接受
- 日期：2026-09-05

## 背景

VibeTable 已有从 revision identity 到 sidecar 只读物化、一次性 path grant、比较后 effective-revision
CAS 和请求期已知文件清理的链路。当前 `OpenXmlDocumentDiffEngine` 仅用 BCL `ZipArchive` 和安全
`XmlReader` 提取可见文本，能够给 DOCX/XLSX/PPTX 提供粗粒度行数结果，但不能表达逻辑差异组、结构
位置、富文本片段、格式变化或 XLSX 的公式/类型/样式语义。

实现文档要求默认能力离线可用、绝不修改源文件，并给 Word/WPS 高保真模式和 XLSX 语义比较划定清晰
边界。在引入生产依赖前，先用 Microsoft Word/Excel 16.0 生成的真实 OOXML corpus 验证候选方案。语料位于
`desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification/`，`manifest.json` 声明覆盖项，
测试直接检查关键包结构与当前公开 diff seam 的结果；损坏文档是唯一的合成输入。

## 资格验证结论

### Clippit / DOCX

本地 spike 使用 Clippit CLI 0.9.0；其 `version --format json` 报告 Open XML SDK 3.5.1。当前
[Clippit 3.9.0 NuGet 包](https://www.nuget.org/packages/Clippit/3.9.0)面向 `net10.0`，传递依赖包括
`DocumentFormat.OpenXml`、`SkiaSharp` 和 Linux native assets，许可证为 MIT。CLI 的
[`word compare` 与 `word accept-revisions`](https://sergey-tihon.github.io/Clippit/cli-word.html)能够写入
tracked-revision DOCX 并接受已有修订；本次验证没有把 CLI、NuGet 包或生成结果加入产品。

观察结果如下：

| 场景 | 结果 | 结论 |
|---|---|---|
| `合同状态为甲。` → `合同状态为乙。` | 输出删除整句、插入整句，而不是只标记 `甲` → `乙` | 默认 tokenizer 不满足中文单字粒度 |
| 综合 DOCX（中英文、标点、段落、表格、脚注、文本框、图片） | CLI 报告 15 条修订；Word 16.0 只读打开后报告 14 条 | 原始修订数不是稳定的“逻辑差异数”，必须由 VibeTable 归一化和聚合 |
| 仅字体和段落缩进变化 | 输出 0 条修订 | Clippit 默认比较不提供所需格式覆盖 |
| 原文含已有修订 | 派生副本由 2 个修订元素归一化为 0，且通过 Microsoft365 schema verify | 可以复用“先接受修订再比较”，但只能操作隔离副本 |
| 综合比较结果 | Clippit verify 通过，Word 16.0 以只读方式打开 | 输出包可供 Word 查看；这不等于语义或计数已经满足产品契约 |

因此资格结论为：**Clippit 中文准确度需自定义 tokenizer/归一化聚合层**。中文单字替换已经证明默认
tokenizer 会扩大到整句，而且不能把 provider 的原子 `w:ins`/`w:del` 数量直接暴露为用户可见 change；
中文连续文本、标点、移动和表格必须
由真实 corpus 锁定逻辑分组。格式变化需由 VibeTable 的 OOXML 格式指纹比较实现；Word 原生比较仅作为
用户显式选择的高保真补充。

PR-3/4 的 managed Clippit 3.9.0 预检查进一步发现：`WordSeparators` 可令普通中文单字替换只产生
`甲`/`乙` 修订，也能将 `100万元 → 120万元` 限定为 `100`/`120`；但扩展汉字 `𠀀 → 𠀁` 和
emoji `👩🏽‍💻 → 👩🏻‍💻` 在默认及 CJK 分隔符配置下均产生零条修订。输出包含新文本且原始
`w:ins`/`w:del` 均为零，Microsoft365 schema 验证仍通过。这证明包结构合法不能证明比较语义完整，
也不能把 Clippit 的零修订结果作为“相同”的权威结论。样本从已有 Word 语料的内存副本替换正文构造，
没有修改语料或源文件。生产 provider 必须通过独立的 Unicode 文本元素对齐及原文/终态还原验证，
解决该遗漏后才可发布比较稿；调整分隔符不足以解除这一门禁。

同一 managed 预检查确认 `RevisionAccepter.AcceptRevisions` 可在派生副本上清除已有插入/删除修订，
结果通过 schema 验证，原始语料内容和修改时间均保持不变。该结果仅证明归一化路径，不解除上述
Unicode 比较门禁。

固定上游提交 `41a64150b5339524dc6555a7d07a99c06a188b0a` 的原因链为：
[`ComparisonUnits`](https://github.com/sergey-tihon/Clippit/blob/41a64150b5339524dc6555a7d07a99c06a188b0a/Clippit/Comparer/WmlComparer.Internal.Methods.ComparisonUnits.cs#L135)
逐 `char` 建 atom，
[`ComparisonUnitAtom`](https://github.com/sergey-tihon/Clippit/blob/41a64150b5339524dc6555a7d07a99c06a188b0a/Clippit/Comparer/ComparisonUnitAtom.cs#L70)
将孤立代理字符交给默认 UTF-8 编码，
[`WmlComparerUtil`](https://github.com/sergey-tihon/Clippit/blob/41a64150b5339524dc6555a7d07a99c06a188b0a/Clippit/Comparer/WmlComparerUtil.cs#L39)
的替代回退丢失了原字符差异，最终 Equal 分支采用 after 元素。这是比较键构造前的信息损失，
不是摘要算法的随机碰撞；修改分隔符或单独更换摘要算法不能解决。首期 Unicode 文本仍必须完整支持，
不能以“不支持扩展汉字/emoji”缩减产品范围。自有对齐和修订生成需补足该边界；比较稿同时验证
“接受修订得到 after、拒绝修订得到 before”的语义还原，且不能只在零修订时检查，因为正常修订和漏报
修改可能出现在同一文档中。

### Word / WPS

Word 16.0 可只读打开内置候选输出，也可作为后续可选原生比较 provider。它必须运行在独立 helper 进程，
只接收隔离副本和专用输出目录；关闭文档时不保存，不向最近列表写入，不复用交互式 Word 实例，取消或
超时后由 helper 自行退出。VibeTable 主进程不得通过 Office COM 直接持有源文档。

资格环境检测到按用户安装的 WPS Office 12.1.0.28488，但没有注册 `KWPS.Application`、
`WPS.Application` 或 `Kingsoft.WPS.Application` COM ProgID。隐藏 `/n` viewer probe 被 launcher 委派，
未能观察到文档标题；派生文件时间未变化。该结果是 `inconclusive`，不能证明本机 viewer 兼容，更不能
证明原生比较 API、自动化稳定性或安全关闭语义。因此 **WPS 首期仅保留用户触发的查看器集成方向**；
运行时必须先报告可用性并由 UI 验收，在独立真实 corpus 和隔离 helper 验证完成前，不注册 WPS 原生
比较 provider，也不宣称与 Word 等价。

### 格式覆盖矩阵

| 差异 | 内置 DOCX | Word 原生 |
|---|---|---|
| 字符/段落/表格结构变化 | 自定义 CJK tokenizer、逻辑聚合和结构定位后作为默认能力 | 可选高保真补充 |
| 直接 run/paragraph/table OOXML 属性 | 由格式指纹覆盖，并报告明确 coverage | format-only corpus 已观察到 2 条 Word revisions |
| 页眉页脚、脚注、文本框、图片关系 | 由结构化 part/relationship 定位覆盖 | 可选高保真补充 |
| theme/style cascade 后的最终显示、分页、浮动布局、字段结果、兼容模式及未知扩展 | 首期标记 partial/unsupported，不伪造精确结果 | 需要高保真时必须选择 Word |
| 原文已有 tracked revisions | 只在 `normalized/` 派生副本上接受后再比较 | 仍只接收已隔离/归一化副本 |

`qualification-results.json` 记录固定工具版本、相对输入、比较设置与机器可读观察；三个小型 Clippit
证据包随测试提交，综合 Clippit/Word/WPS 输出只存在于 `build/`，避免复制近 1 MB 的可再生派生物。
精确计数是一次资格证据，不是未来稳定的产品计数契约。

### XLSX

真实 Excel corpus 覆盖值、公式、样式、工作表增删、合并单元格、隐藏行列、稀疏大表和含宏容器。
现有可见文本提取不能区分值类型、公式文本、缓存结果和样式，也不能可靠表示 sheet/cell 定位，所以不作为
XLSX V2 的语义基础。XLSX 将使用独立内置 provider，在不打开 Excel、不重算公式、不更新外链的前提下
比较这些维度。宏样本包含一个惰性的 XLM `RETURN()` 宏表；生成、测试和未来 provider 都不得执行它。

## 决策

### 1. Provider 边界

- 默认 `builtIn`：离线、确定性的 DOCX 结构化 provider，输出 session、summary、分页 change、coverage
  和受控 rich-text AST；其 tokenizer、已有修订归一化、逻辑聚合、结构定位、格式指纹和预算均由
  VibeTable 契约约束。
- 可选 `wordNative`：独立进程中的 Word 高保真 provider，仅在本机资格检查通过且用户显式选择时可用；
  失败、取消或超时不改变默认 provider，也不写回源对象。
- `wpsViewer`：首期只打开已生成的只读派生结果，不参与比较。
- `xlsxBuiltIn`：独立的 XLSX 单元格语义 provider，不借用 DOCX 的文本行模型。

上游只依赖稳定的 diff session 接口，provider 私有 revision markup、COM 类型、Open XML SDK 类型和
CLI 输出不得穿透到 Web/bridge contract。生产是否采用 Clippit 3.9.0 留给实现 PR；在决定采用前必须补齐
managed NuGet 的许可证清单、SBOM、`net10.0-windows`/win-x64 self-contained publish 和传递 native asset
验证。PR-0 不引入生产依赖。

### 2. 源文件隔离

任何 provider 都不得得到 workspace repository 中的源路径或用户选择的原文件路径。现有 sidecar 先按
revision identity 物化只读副本；`ContentHash` 是 revision 的权威 `sha256:` 摘要，但当前 diff copy 只
校验声明长度。artifact broker PR 必须在复制时流式计算 SHA-256，并在比较开始前令副本摘要与
`revision.contentHash` 精确一致，否则 fail closed；随后再将已授权输入放入任务目录：

```text
task/
  input/
  normalized/
  output/
  index/
  manifest.json
```

`input` 只读；接受已有修订等正规化只能写 `normalized`；provider 结果只能写 `output`；分页索引只能写
`index`。Web 和插件只能获得 session/artifact handle，不能获得这些真实路径。比较完成后还必须确认源
revision 的 `contentHash`、`effectiveRevisionId` 与 workspace epoch 均未变化，任一不一致就返回 `stale`
并丢弃结果。现有 broker 已执行 materialized input 的 contentHash 验证、读取租约、任务级 manifest/TTL
和比较后 CAS 发布门禁。文本生产路径复用这些能力发布 V2 session 与派生索引，每次分页前后继续检查
effective-revision CAS 和 workspace epoch；Office 当前浅对比明确报告未覆盖范围。完成、失败、取消、超时和崩溃恢复都由 broker
依据 manifest 与 TTL 清理已知文件，不能扫描或删除任务目录之外的对象。

源隔离测试还必须记录并断言 workspace repository 对象和用户源文件的内容与 `LastWriteTime` 均未变化；
Word/WPS helper 只能证明其专用副本被正确关闭，不能用“源文件看起来还能打开”替代这个零写入门禁。

这里不为本地 corpus 或同一流水线中的临时文件新增重复 SHA-256。`ContentHash` 继续是 revision →
materialized leaf 的权威校验；若未来 artifact 跨进程交接需要摘要，producer、consumer、失败处理和防止的
故障必须在 broker 契约中同时定义，不能用额外 hash 代替 handle、路径约束、schema 或语义测试。

### 3. 派生结果只读

比较结果是可清理的派生工件，绝不自动导入为新 revision，也不覆盖、重命名或移动源文件。打开 Word/WPS
只消费 `output` 中的副本；所有显式“另存为”动作属于用户控制的后续功能，不是 diff session 的副作用。

## 实施顺序

1. 本 ADR 与真实 Office corpus 固化候选结论。
2. 引入 closed Diff V2 session/summary/change/coverage/page contract，并保持 C#/bridge/TypeScript 原子同步。
3. 建立 artifact broker 的目录隔离、opaque handle、manifest、TTL、取消和崩溃清理。
4. 实现 DOCX tokenizer/归一化/比较与逻辑聚合，再接入 Web 可视化。
5. 在独立 helper 中实现可选 Word provider；WPS 在新资格证据出现前保持 viewer-only。
6. 实现独立 XLSX 语义 provider 和表格可视化，最后完成 E2E、性能、取消、恢复与发布资格。

每个阶段独立通过相关测试、双轴审查、fresh PR CI、squash merge、精确 main CI/CD 哨兵和安全清理；
不得为了并行而复制 authority 或降低现有门禁。

## 后果

### Unicode 修订生成的局部资格证据

2026-09-05，自有 `DocxTextDiffer` 与 `DocxRevisionRuns` 在真实 DOCX 包副本中验证了
扩展汉字/emoji/金额混合替换、纯格式修改、组合字符、整段插入和删除五组场景。
固定 Clippit 3.9.0 的 `RevisionAccepter.AcceptRevisions` 与
`RevisionProcessor.RejectRevisions` 分别还原新版和旧版文字及粗体/斜体 run 属性；
比较结果与两种还原结果均通过 Open XML SDK schema 验证。

补充 CR/CRLF 回归后，确认 XML 默认写入会丢失原始换行字符；本方 part 写入使用
`NewLineHandling.Entitize`，并在序列化后重新断言文字与样式。Clippit 3.9.0 的
接受/拒绝处理仍会将文本中的 CR/CRLF 归一化为 LF，因此实际 provider 必须先按
本 ADR 的最终显示内容策略归一化输入，再进行比较；双向还原的基线是归一化输入，
不得声称第三方处理保留了原始 CR 字节。真实包补充场景分别记录原始、归一化和
接受/拒绝后的文本，归一化基线上的六组验证通过；输入源对象不参与写入。

这仅证明正规化纯文本 run 的修订写入边界，不证明整篇文档的段落、表格、关系、
页眉页脚、逻辑分组或 UI 已实现，也不解除 Clippit 原生 Unicode 比较的否决结论。
后续 provider 必须保留真实包双向语义还原测试，不能以 XML 合法或单向接受成功代替。

### 已有修订归一化的进程边界

固定 Clippit 3.9.0 提供 `RevisionAccepter.AcceptRevisions(WordprocessingDocument)`，
可直接处理 `normalized/` 中的派生文件，避免 `WmlDocument` API 的显式整包字节复制。
本地对已有修订样本及两份综合样本验证：直接重载与已验证 API 的所有 part 内容一致，
结果均通过 SDK schema 验证，源内容和修改时间不变。该观察不是峰值内存或超时资格。

上述重载没有取消注入点；仅在调用前后检查 token 或对 `Task.Run` 使用 `WaitAsync`
不能终止正在执行的库代码。生产归一化采用独立任务 worker，宿主拥有进程并监督整个
预检、SDK 打开、接受、保存及退出流程；确认退出后才能清理派生文件。worker 不接收
workspace authority 或 Office COM 对象。需先完成包含二进制 part 的流式展开预检，
再调用 SDK；不能把可见文本 part 的读取预算等同于整包验证，也不能承诺第三方零重复解析。
2026-09-30 迁移候选已包含独立 worker、输出复验和运行中取消/超时测试，宿主通过
Windows Job 设置 1 GiB 单进程内存上限。产品 provider/UI 接线、发布包交接与实际峰值内存
资格仍未完成；这组局部测试不代表可以放行生产。

整包预检已独立实现：按 OPC content type 识别 XML，而非 ZIP 后缀；所有 XML 共用
16 MiB 单 part、64 MiB 总展开量及 250,000 节点/属性预算。二进制 part 独立限制为
64 MiB，整包展开量限制为 256 MiB；每个实际读取到 EOF 的条目核对声明长度。
元数据仅解析一次，其他 XML 流式校验，二进制完整读取，均不访问外部关系资源。
这些限制约束预检输入，不等同于后续 SDK/Clippit DOM 的进程峰值内存保证。

段落序列按相等文本锚点对齐；未匹配区间保留双方范围，供后续结构与文字细化处理。
锚点不代表格式或其他内容相等；不同 story/container 必须分别调用，不能跨表格单元格
或正文/页眉边界强行合并。超过显式 LCS、文字或段落预算时保留范围并标记截断。

### 正向

DOCX 包读取与关系解析使用现有有预算的 ZIP/XML 入口；`System.IO.Packaging 10.0.2`
仅负责 OPC part URI 规范化，不用 `Package.Open` 绕过读取预算。包内条目和内部关系目标
共用规范化 key，编码别名冲突即拒绝；原始 ZIP 名称只用于返回实际条目。
外部关系仅保留元数据，既不解析资源也不发起网络或文件访问。
内部目标先验证 authority/query/转义，再分离 fragment，不能把 `ResolvePartUri`
会丢弃的信息当作已验证。依赖恢复生成 lock，并通过 `--locked-mode` 核对；
content-types 的 Default/Override 由独立映射解析，Override 优先，非法 MIME、重复映射和
非规范 part name 拒绝。类型声明而非文件后缀决定正文等 part 的种类，因此无 `.xml`
后缀的有效正文仍经相同安全 XML 读取。正文由唯一包根关系定位，不假设固定路径；
页眉页脚按 section/variant 保留继承绑定及显示开关，notes 单独选择。奇偶页设置单独保留，
以区分“无 even 引用且偶数页空白”和“所有页沿用 default”的差异。同一 part 不重复读取。
这仍不等于已完成整包归一化、Strict 命名空间转换或 provider/UI 集成。

- 默认能力不依赖本机 Office，离线且结果可由稳定 contract 消费。
- Word 高保真能力与主进程、workspace 源对象和默认结果模型隔离。
- DOCX 原子修订、用户可见逻辑差异与 XLSX 单元格语义不会混成一个脆弱模型。
- 真实 Office corpus 在不要求 CI 安装 Office 的情况下锁定包兼容性和高风险边界。

### 成本

- 内置 DOCX 仍需自定义 tokenizer、聚合和格式指纹；不能直接转发 Clippit 的 revision count。
- Word helper、artifact 生命周期和 managed 供应链支持必须分别完成，生产依赖不能在 spike 后直接落地。
- WPS 原生比较和 Office 交互式兼容性仍需有对应安装环境的后续资格验证。

## 被否决方案

- 让 Word/WPS 直接打开 workspace 源文件：违反源对象不可访问和零写回边界。
- 把 Clippit raw revisions 当作 V2 changes：中文单字会扩大成整句，不同消费者计数也不一致，且没有逻辑分组、结构与格式契约。
- 用当前可见文本 diff 充当 XLSX 语义 diff：会丢失公式、类型、样式和精确单元格定位。
- 因本机能打开结果就把 Word 设为默认依赖：破坏离线可用性，并把 COM 生命周期引入主进程。
- 在未安装 WPS 的环境中推断其 native provider 可用：没有可执行证据。
