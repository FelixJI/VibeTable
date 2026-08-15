# 工作区全文搜索 P0 研究（截至 2026-08-12）

## 范围、方法与结论口径

本文件为 M7 P0 的技术取证，不实现搜索或引入依赖。仓库基线来自
`sidecar/go.mod`（`modernc.org/sqlite v1.54.0`）、`sidecar/go.sum` 与当前
`scripts/build_next.py` / `qa/next.py`；既有研究文档存于 `docs/research/`，本文件沿用
“事实—建议—未验证项”分离的写法。

“事实”只引用 SQLite、Unicode、Apache 或上游项目的官方文档、源代码和许可证；“建议”是对
VibeTable 的离线、Windows x64、PocketBase 为唯一业务权威这几个约束的工程取舍，并非来源的
原话或性能承诺。

## 1. 锁定 SQLite、FTS5 与 Windows

### 已核实的事实

- 仓库锁定 `modernc.org/sqlite v1.54.0`。该版本随模块缓存提供的
  `lib/sqlite_windows.go` 具有 `windows && (amd64 || arm64)` build constraint，且其生成命令明确
  包含 `-DSQLITE_ENABLE_FTS5`、`-DSQLITE_OS_WIN=1` 和 `--goarch amd64 --goos windows`。上游
  对应的 Windows 生成源应在实现前再与锁定 tag 逐字核对：[modernc/sqlite
  v1.54.0 Windows 源](https://gitlab.com/cznic/sqlite/-/blob/v1.54.0/lib/sqlite_windows.go)。
- SQLite 官方说明：编译 amalgamation 时定义 `SQLITE_ENABLE_FTS5` 即把 FTS5 编入 SQLite；FTS5
  是虚表模块，能够以 `MATCH` 查询并提供 `bm25` 排序。[FTS5 编译与查询](https://www.sqlite.org/fts5.html#compiling_and_using_fts5)、[BM25](https://www.sqlite.org/fts5.html#the_bm25_function)。
- FTS5 的内建 tokenizer 是 `unicode61`（默认）、`ascii`、`porter` 和 `trigram`；其中
  `unicode61` 基于 Unicode 6.1，`porter` 只适合英文词干，`trigram` 把连续三个字符作为 token。
  [SQLite FTS5 tokenizer](https://www.sqlite.org/fts5.html#tokenizers)。
- FTS5 的 C API 允许注册自定义 tokenizer；本仓当前未检出把它接到 sidecar 的 Go 注册代码。因此
  “可编入 FTS5”不等于“已有可调用的自定义 tokenizer”。[SQLite FTS5
  custom-tokenizer API](https://www.sqlite.org/fts5.html#custom_tokenizers)。

### P0 建议

1. **直接采用锁定 modernc 的 FTS5，不新增 cgo/SQLite DLL。** 对 Windows 10/11 x64，这避免了
   运行时加载外部 SQLite 扩展的打包、ABI 和 DLL 搜索路径问题；最终二进制仍应在发布包内执行
   `SELECT sqlite_compileoption_used('ENABLE_FTS5')` 和一次 `CREATE VIRTUAL TABLE ... USING fts5`
   的 smoke，而不是只信任源码生成参数。
2. **P0 不写自定义 FTS5 tokenizer。** 首版保留两个受版本控制的索引：Latin/通用字段用
   `unicode61 remove_diacritics 2`，CJK 子串召回用 `trigram case_sensitive 0`。这只使用已核实的
   内建模块；应用层负责同一归一化函数，避免索引与查询不一致。
3. FTS 内容应是 PocketBase 记录与附件抽取文本的**可再生派生索引**，不得变成业务数据权威。
   若采用 FTS5 external-content table，源表每次 insert/update/delete 必须同步索引；SQLite 明确
   指出这项一致性由应用负责，失配后可用 `rebuild` 重建。[external-content 的一致性与
   rebuild](https://www.sqlite.org/fts5.html#external_content_table_pitfalls)。

## 2. Unicode 与中西文分词

### 已核实的事实

- Unicode NFKC 属于兼容规范化：先兼容分解并规范排序，再重新组合；这会把兼容等价字符折叠，故
  不是“保留原始展示文本”的转换。[UAX #15，Normalization Forms](https://www.unicode.org/reports/tr15/)。
- Unicode 的完整 case folding 可改变字符串长度，且数据文件明确说明 case folding 本身不保持
  normalization form；例如完整映射能使 `FUSS` 与 `Fuß` 匹配。因此归一化与大小写折叠的顺序必须
  固定并测试。[Unicode CaseFolding.txt](https://www.unicode.org/Public/UCD/latest/ucd/CaseFolding.txt)。
- `unicode61` 以连续 Unicode 字母/数字/私用区字符构成 token，按 Unicode 6.1 做大小写无关处理；
  默认会移除 Latin 变音符。`remove_diacritics=2` 修正默认值 1 对少数多重变音符码位的已知缺口。
  [unicode61 行为与参数](https://www.sqlite.org/fts5.html#unicode61_tokenizer)。
- `trigram` 支持一般子串匹配，默认大小写无关；`case_sensitive=1` 时才区分大小写。它是按三个
  字符切片，不是中文、日文或韩文的语言学分词器。[trigram tokenizer](https://www.sqlite.org/fts5.html#trigram_tokenizer)。

### P0 建议

- 将可搜索副本定义为固定纯函数：`NFKC -> full case fold -> NFKC`，但保存原文用于显示、摘要与
  高亮。这一顺序是**建议**：第二次 NFKC 专门应对官方 Unicode 数据指出的“case folding 不保持
  normalization”的事实。归一化/Unicode 数据版本、函数实现 hash 与 tokenizer 配置必须写入
  `index_meta`；任一项变动即全量重建。
- Latin/混合字段由 `unicode61 remove_diacritics 2` 处理；不要把 `porter` 套到混合语种字段，因为
  SQLite 明确限定它为英文词干算法。查询前先以同一函数归一化，并把用户文本作为一个被安全引用的
  phrase，而不直接暴露完整 FTS 语法。
- 对包含 Han/Hiragana/Katakana/Hangul 的索引文本，另写入 trigram 表。三字符以下的 CJK 查询走
  明确的降级路径（受限 `LIKE` 或提示“至少 3 字符”），不要伪称 FTS 的 trigram 命中。首版不做
  词典分词、拼音、繁简转换或跨语言词干；它们需要语言资源和可解释的产品语义。

## 3. PDF 与 OOXML 抽取候选、许可与资源边界

### 候选结论

| 候选 | 格式与许可事实 | 加密/损坏输入与限制 | Windows 打包结论 |
| --- | --- | --- | --- |
| **Apache Tika 3.x（推荐作为跨格式隔离 worker 的 P0 候选）** | 官方项目声明可抽取 PDF、PPT、XLS 等千余格式；项目主体为 Apache-2.0，但发行包含有各自许可的子组件，须随发行版本审计 `LICENSE.txt`/`NOTICE`。[上游 README/许可](https://github.com/apache/tika) | 上游说明使用 Bouncy Castle 抽取加密 PDF 文本；批处理已有单文件上限、单次 parse 超时、watchdog 重启与 JVM `-Xmx` 控制。PDFBox 同时明确非可信 PDF 可能触发异常、无限循环或耗尽 CPU/内存，故隔离仍必要。[Tika 加密 PDF](https://github.com/apache/tika#readme)、[Tika 限制项](https://tika.apache.org/3.2.3/gettingstarted.html)、[PDFBox 安全模型](https://pdfbox.apache.org/security.html) | 需要 JRE 17+ 和 Tika/JAR 依赖树，不能把它悄悄塞进现有 Go sidecar。若选用，构建应锁定 Windows x64 JRE + Tika 精确版本、把许可/NOTICE/SBOM 纳入 `build_next.py` 的候选校验，并以受控子进程读取授权文件。 |
| **直接实现受限 OOXML 流式读取（推荐的 DOCX/PPTX 基线）** | DOCX/PPTX/XLSX 是 ZIP + XML 容器；只读取 `word/document.xml`、`ppt/slides/*.xml`、XLSX `sharedStrings.xml`/worksheets 的文本，不需引入第三方提取器。ECMA-376 为 OOXML 规范来源。[ECMA-376 标准入口](https://ecma-international.org/publications-and-standards/standards/ecma-376/) | 只接受未加密 ZIP-OOXML；遇 OLE/加密容器、损坏 ZIP/XML、超限条目即返回可诊断的“不可抽取”，绝不尝试绕过口令。ZIP 条目、解压字节和 XML token 数由本程序先计数后流式读取。 | 无额外 runtime 或许可清单，适合现有纯 Go win-x64 sidecar；代价是只抽文本、需自行维护 OOXML 边界测试，不承诺宏、嵌入对象、OCR 或完整版式。 |
| **Excelize（XLSX 专用的备用候选）** | Excelize 是纯 Go 的 XLAM/XLSM/XLSX/XLTM/XLTX 读写库，BSD-3-Clause。[上游 README 与许可证标识](https://github.com/qax-os/excelize) | `Options` 提供 `Password`、总解压上限、XML 解压上限和临时目录；源码示例显示可用口令打开加密 workbook，错误口令会返回错误。[Options 与 OpenFile](https://github.com/qax-os/excelize/blob/master/excelize.go) | 可随 Go sidecar 打包；但它仅覆盖表格，且临时目录必须指向 VibeTable 管理的工作目录，不能使用不可控系统临时路径。引入前须锁版本、复核完整依赖许可并做损坏/加密样本测试。 |
| **Unioffice（排除）** | 覆盖 DOCX/XLSX/PPTX，官方 README 同时说明二进制约 33 MB、需要商业 license code。[上游 README](https://github.com/unidoc/unioffice) | 本研究未核实其对恶意 ZIP、上限和取消的可执行契约。 | 即使功能覆盖面大，也会增加商业授权和包体；M7 P0 不选。 |

### 统一的 P0 资源与失败契约（建议）

这些数字是**待基准校准的初始 product limit**，不是上游默认值：容器文件 `<= 64 MiB`、ZIP 条目
`<= 2,000`、累计解压字节 `<= 256 MiB`、任一 XML/文本部件 `<= 32 MiB`、每附件归一化后的索引文本
`<= 2,000,000` Unicode code points、单附件 wall-clock `<= 30 s`、worker 内存上限 `512 MiB`。

1. 先用文件大小、魔数/容器类型、ZIP central directory 的条目数与宣称解压大小拒绝超限项；逐块复制时
   再累计实际解压字节。不要只依赖压缩比，避免 ZIP bomb。
2. 每个抽取任务接受 `context.Context`；Go 流式读取每个块检查 `ctx.Err()`，关闭 reader 并删除尚未
   promote 的临时产物。Tika 则必须运行在 job object 管理的子进程中：达到超时、内存或取消时终止整个
   进程树，标记 `cancelled` / `resource_limited`，不得把部分文本加入索引。
3. 加密、需要口令、格式错误、解压/字符/时间超限是**每附件的非致命状态**；保存文件元数据与失败码，
   但不索引正文。绝不记录口令，也不将未加密中间件写入 `%TEMP%`。这与现有 snapshot 导入先受限准备
   reader、逐条检查 `context`、验证后才提交的模式一致（仓库源码：
   `sidecar/internal/workspacev2/snapshot_package_secure_io.go` 与
   `sidecar/internal/snapshotpkg/import.go`）。
4. 不做 OCR；扫描型 PDF 的抽取结果为空时标记 `no_text_layer`，不把图片渲染或外部可执行程序纳入 P0。

## 4. 索引磁盘、build / verify / promote 与基准

### 已核实的事实

- 默认 FTS5 会保存内容；external-content 或 contentless 表通常省略 `%_content`。`columnsize=0` 能
  省去 `%_docsize`，但按需取得 token 数会变慢。[FTS5 content/docsize 存储](https://www.sqlite.org/fts5.html#the_table_contents_the_content_table)、[columnsize](https://www.sqlite.org/fts5.html#the_columnsize_option)。
- SQLite 的一个 1,636 MiB email 语料测试中，`detail=full` 索引 743 MiB、`detail=column` 340 MiB、
  `detail=none` 134 MiB；后两者会失去部分位置/列查询能力。因此这只是同一语料和设置下的案例，**不
  是 VibeTable 的磁盘倍率承诺**。[detail 选项与实测案例](https://www.sqlite.org/fts5.html#the_detail_option)。
- 每增加一个 prefix 索引，FTS5 最多为每个 token 新增一套索引条目；trigram 的目的则是一般子串匹配。
  [prefix 的磁盘含义](https://www.sqlite.org/fts5.html#prefix_indexes)、[trigram](https://www.sqlite.org/fts5.html#trigram_tokenizer)。
- 当前 `scripts/build_next.py --release` 产出 Windows x64 包，sidecar 以 `go build` 进入候选；
  `qa/next.py --ci --json-report build/qa/report.json` 是完整发布资格入口。它们是构建/发布资产校验，
  不是搜索质量基准。

### P0 推荐架构与磁盘预算

- 建两张**external-content** FTS5 表：`search_terms` 用 `detail=full`（结果高亮、短语和 BM25），
  `search_cjk3` 用 trigram，均只索引可检索字段与受限抽取文本；业务行 ID、workspace UUID、源 revision
  与 schema/tokenizer/normalizer 版本进入 `index_meta`。不配置 prefix 索引，直到基准证明输入式 Latin
  前缀查询需要它。
- 首版容量预算采用可观测阈值，而非伪精确倍率：每 workspace 记录
  `source_searchable_bytes`、两个 FTS shadow tables 的页数/字节、WAL 峰值、提取文本字节和总倍数。
  以 `index_bytes / source_searchable_bytes` 分维度报出 p50/p95/max；超过产品设定配额时停止后台建索引并
  提示用户。最终配额只在下面的固定语料基准后确定。

### build → verify → promote（建议）

```text
PocketBase 源 revision + index_meta(expected config)
            │
            ├─ build：写入同工作区、同卷的 generation-N staging FTS 数据
            │          （每批检查取消；业务源不写入）
            │
            ├─ verify：来源 revision/UUID/config 一致；行数、FTS integrity-check、
            │          固定查询集与结果签名通过
            │
            └─ promote：单个短事务更新 active generation / metadata；旧 generation
                       仅在无 reader 后回收。失败或取消保留 active generation。
```

- build 不触碰 active index；对每批读取源记录前后比较 revision。源变了即丢弃 staging generation 并按
  新 revision 重建或增量追赶，不能把新旧文本混入同一 generation。
- verify 至少执行 SQLite `PRAGMA integrity_check`、FTS5 `integrity-check`、源记录计数/抽取状态计数、
  `fts5vocab` token 计数，以及固定 golden queries 的 record ID 排序与高亮边界断言。FTS5 官方规定
  external-content 失配会造成直觉外结果，故验证必须包含源/索引绑定而不只检验数据库能打开。
- promote 只切换派生索引指针；PocketBase 继续是业务权威。Windows 上 staging 与 active 必须同卷，且
  活跃 reader 通过 generation 引用计数或短连接释放后再回收，避免文件句柄导致替换失败。`build_next.py`
  的发布包仍只打包程序；用户 workspace 中的可重建索引不是 release asset。

### 基准设计（建议）

1. **语料与可重复性**：固定 seed 生成 10k / 100k / 1M 记录，字段分别含 ASCII/带变音符 Latin、CJK、
   混合脚本、emoji、长字段；另固定 100 个 PDF、DOCX、PPTX、XLSX 样本，包含无文本 PDF、加密、损坏、
   高压缩比和超过限制项。记录每个样本 SHA-256、许可证/来源和预期失败码；不把受版权约束原件随产品
   发布。
2. **build**：冷启动与热启动各 5 次，记录 wall/CPU、峰值 RSS、源字节、各 FTS shadow table/WAL 字节、
   token 数、失败分布和每秒 records/tokens。分别跑 normal、CJK trigram、双索引与增量 1%/10% 修改。
3. **query**：每类至少 1,000 个确定性查询，分别测 exact Latin、大小写/兼容字符、前缀、中文 3+ 字符
   子串、混合语句、无匹配与高频词；记录 p50/p95/p99、前 20 条 record ID、BM25 排序稳定性和高亮正确性。
   截至 P0，不把单一机器的毫秒数字写成跨设备 SLA。
4. **韧性**：建索引中取消、worker 超时、坏 ZIP/XML、口令缺失/错误、磁盘配额不足、应用重启和源 revision
   变化后，断言 active generation 未改变或能完整重建；在 Windows 10/11 x64 的发布解压目录运行同一组
   smoke。把该基准作为新增专用 job；只有影响索引/提取器的变更才进入 `qa/next.py --ci` 的相关 lane，
   不把整套百万行基准塞入每次 PR 的 required gate。

## 未验证范围与下一步取证

### 2026-08-12 固定语料资格结果

实现后的 Windows x64 required harness 位于 `sidecar/cmd/workbench-qualification`，由
`qa/next.py --stage workbench-qualification` 执行。固定输入为 100,000 条结构化记录、10,000 个
FileDocument、20 GiB 逻辑文件规模与 7,590,000 个可搜索 UTF-8 源字节；每次运行都会重新建立独立
SQLite/FTS 数据库，随后执行 250 次 warm query 与 100 次增量提交。预算冻结如下：

- Peak RSS：`<= 1 GiB`（Windows `GetProcessMemoryInfo/PeakWorkingSetSize`，不是 Go heap 近似）；
- 完整重建：`<= 2 min`；
- index/source searchable bytes：`<= 32x`；该阈值来自本固定语料实测 `26.47x` 再留约 20% 余量，
  不能用 20 GiB 逻辑文件大小稀释；
- warm query p95 `< 150 ms`、首屏 `< 300 ms`、增量索引事务 p95 `< 2 s`。

本次当前 HEAD 的本地报告为：Peak RSS `106,270,720` bytes、重建 `17.402 s`、索引
`200,888,848` bytes / `26.468x`、首屏 `0.502 ms`、warm p95 `0.989 ms`、增量 p95
`1.509 ms`，失败项为空。报告位于忽略的 `build/qa/workbench-qualification.json`；数字只证明本次
Windows 环境和固定语料，不是跨设备性能承诺。完整 QA 通过时才构成发布资格。

- 本次未生成或运行 win-x64 release candidate，故尚未在最终 `vibetable-pb.exe` 上实测
  `sqlite_compileoption_used('ENABLE_FTS5')`、创建 FTS5 表或跑 `PRAGMA compile_options`；结论目前由锁定
  模块版本及其 Windows 生成源码支持。
- 尚未选择 Tika、直接 OOXML 或 Excelize，也未下载它们的精确版本；因此 Tika 完整依赖树、JRE 体积、
  CVE/NOTICE，及 Excelize 精确版本的 transitive license 仍待进入依赖变更评审后核验。
- 尚未以真实用户 workspace、百万行或独立 Windows 10/11 设备校准中文召回质量和性能；固定 harness
  已冻结 P0 release budget，但仍需 packaged candidate 和跨设备证据，不能把单机数字写成普适 SLA。

## 本次核对命令与来源

只读仓库核对使用：`git status -sb`、`git branch --show-current`、`git remote -v`、
`rg --files`、`rg -n -i 'modernc|sqlite|fts5|extract|search|pdf|docx|pptx|xlsx|zip'`，以及读取
`sidecar/go.mod`、`sidecar/go.sum`、`scripts/build_next.py`、`qa/next.py` 和已有
`docs/research/*.md`。模块核对使用 `go env GOMODCACHE` 并读取锁定模块的
`modernc.org/sqlite@v1.54.0/lib/sqlite_windows.go` 与 `LICENSE`；未修改锁文件、依赖或构建产物。

外部来源均为一手文档、规范或上游源码/许可证：
[SQLite FTS5](https://www.sqlite.org/fts5.html)、[Unicode UAX #15](https://www.unicode.org/reports/tr15/)、
[Unicode CaseFolding 数据](https://www.unicode.org/Public/UCD/latest/ucd/CaseFolding.txt)、
[Apache Tika 上游](https://github.com/apache/tika)、[Apache Tika CLI 文档](https://tika.apache.org/3.2.3/gettingstarted.html)、
[PDFBox 安全模型](https://pdfbox.apache.org/security.html)、[Excelize 上游源码](https://github.com/qax-os/excelize/blob/master/excelize.go)、
[Unioffice 上游 README](https://github.com/unidoc/unioffice) 与
[ECMA-376](https://ecma-international.org/publications-and-standards/standards/ecma-376/)。
