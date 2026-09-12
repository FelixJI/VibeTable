# A6 PDF 候选的隔离运行资格

当前结论：固定 PdfPig 0.1.16 与 SharpZipLib 1.4.2 的实验工具在 38 项自有语料（含独立生产者和有限结构 DISCOVERY）上与冻结预期一致。
这使候选可重复观察，不批准 ADR 0014，也不替换产品 Go adapter 或改变现有发布门禁。

## 接口与边界

`qa/pdf-adapter` 是独立资格 executable，依赖使用 NuGet lock。worker 只解析指定 PDF 并返回结果；
supervisor 持有 Windows Job、截止时间、取消、输出和清理责任。Python 入口逐项收集原始观察，
Go `pdf-qualification --observations` 使用既有语义比较器验收，不能以收集命令 exit 0 代替比较通过。

输入 64 MiB、单流解码 32 MiB、累计解码 256 MiB、输出 2,000,000 Unicode code points 和 30 秒约束不变。
本次显式传入 1 GiB Job commit 限额；这只是实验参数，不是产品内存预算或以失败后提高预算取得的通过。
输出截断后仍遍历后续可达页；损坏流、无法支持的 DecodeParams 和候选 warning 均返回空正文拒绝。
warning 全拒绝是待校准的保守候选策略，不代表生产文档支持范围已经确定。

Win10/11 x64 创建进程时通过 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 直接加入 Job，避免先运行再绑定的空隙。
Job 限制 commit 和 user CPU；另采样总 CPU，并使用墙钟 deadline。stdout/stderr 有界，非成功结果丢弃 stdout，
结束后确认 Job 活跃进程为零且根进程退出。普通 Job 消息可能丢失，未知崩溃保持 WorkerFailed，不能仅凭退出码
声称内存门禁触发。报告分别记录 Job peak commit 与根 worker peak working set，二者不能互相替代。

依据：[Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)、
[Job 限额字段](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_limit_information)、
[创建时分配 Job](https://devblogs.microsoft.com/oldnewthing/20230209-00/?p=107812)。

## 复现

在仓库锁定的 Python、.NET 与 Go 环境中运行：

```text
dotnet build qa/pdf-adapter/PdfAdapterQualification.csproj --configuration Release --artifacts-path build/qa/pdf-adapter/artifacts -p:RestoreLockedMode=true
build/qa/pdf-adapter/artifacts/bin/PdfAdapterQualification/release/PdfAdapterQualification.exe --check-process-boundary
uv run --frozen --no-sync python tests/contract/generate_pdf_qualification_corpus.py
uv run --frozen --no-sync python qa/pdf_adapter_qualification.py tests/contract/pdf_qualification_corpus.json build/qa/pdf-qualification/v1 build/qa/pdf-adapter/artifacts/bin/PdfAdapterQualification/release/PdfAdapterQualification.exe --memory-mib 1024 --output build/qa/pdf-adapter/observations-structure-38.json
go -C sidecar build -o ../build/qa/pdf-adapter/pdf-qualification.exe ./cmd/pdf-qualification
build/qa/pdf-adapter/pdf-qualification.exe --observations tests/contract/pdf_qualification_corpus.json build/qa/pdf-adapter/observations-structure-38.json
```

12 项进程检查包含成功、deadline、取消、创建前取消、CPU、托管/原生内存、两类输出上限、后代进程、
部分输出后失败及创建失败。原生分配检查确实遇到 commit 拒绝；不以 peak 小于配置限额冒充内存限额证据。
持久 pytest 入口为 `tests/contract/test_pdf_adapter_qualification.py`，同时覆盖后续页正负语料。

## 本次证据与限制

- .NET 10.0.401 构建零 warning/error；12 项进程检查全部通过且均清理，证据 `build/qa/pdf-adapter-runner/persistent-checks.json`。
- 28 项原始观察 `build/qa/pdf-adapter/observations-final-1g.json`；比较 `comparison-final-1g.json` 为 failed=0、exit 0。
- 旧原型在损坏后续页上仍返回 truncated、warning 空；`old-probe-later-invalid.json` 保留该反证。候选返回 failed / extract.pdf_stream_invalid、空正文。
- 28 项那次测量的最高根 worker working set 为 754,130,944 字节；对应 Job peak commit 734,937,088 字节。仅是当前机器单次测量，不是性能承诺。
- 初始比较器将毫秒声明为整数，拒绝实际 296.875ms；现接受有限非负小数，保留原始测量，增加小数和非法测量回归。
- 单独运行首次进程 pytest 是 1 passed，但命令因未运行 backend 覆盖率为 0 而 exit 1；不记作完整质量通过。

真实复杂对象流/predictor、独立可再分发生产者、加密和深层循环、warning 校准、完整 generation/授权/发布集成、
NOTICE/SBOM 与产品 adapter 决策仍未完成。此工具的包引用仅用于资格 executable，不进入当前产品包。

完整质量历史记录：`uv run --frozen --no-sync python scripts/automation_project.py python-quality` 首次为
1897 passed / 1 skipped / 2 failed、覆盖率 91.88%；两个失败来自该新 worktree 缺少 Web 依赖。确认 lock 一致并复用
既有 Web 环境后，重跑为 1899 passed / 1 skipped、覆盖率 91.88%，日志分别为
`build/qa/pdf-adapter/python-quality.log` 和 `python-quality-reused-web.log`。本 worktree 后续首次 Pyright 曾报告
49 个缺失依赖；以 frozen/offline 同步已有 `.venv` 后恢复质量入口，不变更 lock 或依赖来源。当前结构增量的最终
质量入口 exit 0：1913 passed / 1 skipped、覆盖率 91.88%，并含 13 项 PDF 集成测试及 12 项进程检查；唯一 skip 为
既有 Windows symlink 特权不足场景，日志为 `build/qa/pdf-adapter/python-quality-structure-restored-venv.log`。
`go test ./cmd/pdf-qualification` 通过；相关 Python Ruff 与 Pyright 通过。

## 独立生产者与加密发现

工具 bundle 中的 ReportLab 4.4.9 生成自有 Base14 ASCII 内容，分别使用普通和压缩页流；
PDFium 独立核对两个 token。pypdf 6.10.0 / cryptography 50.0.1 再生成 AES 用户密码和空用户密码对照，
通过已知测试密码确认两份正文确实存在。工具包版本不属于仓库 Python lock，不进入产品依赖。

原候选四次观察均确认进程退出：普通与压缩两项 indexed、用户密码一项 passwordProtected；
空用户密码但非空 owner 密码一项却 indexed 并返回正文，违反既定加密拒绝策略。原始证据位于
`build/qa/a6-independent-producers/summary.json`、`assessment.json` 和逐项 `adapter-*.json`。
修复采用 PdfPig 的 `PdfDocument.IsEncrypted`，在读取页面前明确拒绝，包括库自动接受空密码的情形。
新的四项固定回归已通过；不得以库打开成功代替策略验收。

最终增量验证：完整 `python-quality` exit0、1907 passed / 1 skipped、91.88%，包含7项PDF真实集成回归；
`observations-independent-32.json` 对应 `comparison-independent-32.json` 为32项、failed0、exit0。
最终四个固定fixtures另经独立oracle核验，报告 `build/qa/a6-independent-producers/final-fixture-independent-oracles.json`。
旧候选对最终fixture的空密码RED保留在同目录 `final-fixture-old-candidate/`；未改原观察或预期。

普通CI只复制已提交的四个小PDF，不加载生产者依赖。重新生成时，调用者将 `A6_PRODUCER_PYTHON` 指向
已具备脚本要求版本的资格工具解释器，通过现有 uv 入口启动：

```powershell
uv run --frozen --no-sync -- $env:A6_PRODUCER_PYTHON -B tests/contract/generate_pdf_producer_fixtures.py
```

生成器在导入时不加载工具包；执行时核对三个版本，不匹配即失败，只写四个固定路径且不清理目录。
本次使用现有工具bundle，不安装额外依赖或将bundle路径写入产品配置。ReportLab压缩样本采用ASCII85与Flate链，
不能将其当作单一Flate filter或一般复杂filter链已通过的证据。

## 结构 DISCOVERY 的 38 项观察

原 28 项和独立生产者增量后的 32 项证据保留不改。随后只用标准库在现有语料生成器中加入六份自有结构样本：
ObjStm/type-2 xref、合法与坏 filter 的 Predictor 12、Predictor 1 identity、64 层有限页树及回边 cycle。它们是
预先声明的 DISCOVERY 构造，不代表一般结构支持；合法样本只允许 `indexed` 或 `unsupported`，坏结构只允许
`failed`、`unsupported` 或 `resourceLimited`，并且所有拒绝都必须为空正文且不得是 `noTextLayer`。

当前复现产物为 `build/qa/pdf-adapter/observations-structure-38.json`，Go 比较报告为
`build/qa/pdf-adapter/comparison-structure-38.json`：exit 0、failed=0。六项的实测状态如下（毫秒保留收集器原值）：

- ObjStm：`indexed`、20 code points，291ms wall / 250ms CPU，Job peak 34,607,104、worker working set 62,189,568 bytes。
- Predictor 12 合法、坏 filter 和 Predictor 1：均为 `unsupported / extract.unsupported`、零正文；wall 分别为 140、143、144ms，CPU 为 109.375、93.75、93.75ms。
- 64 层有限页树：`indexed`、20 code points，287ms wall / 250ms CPU。
- 64 节点 cycle：`failed / extract.pdf_invalid`、零正文，240ms wall / 218.75ms CPU。

全部六项均为 `Succeeded`，且 `allProcessesExited=true`；记录的 process limits 是 1 GiB Job commit、30 秒 CPU 和
30 秒 deadline。每项也保留独立的 Job peak commit 与 root worker peak working set，不能将任一数值外推为覆盖所有
资源路径。四个合法样本的 PDFium+pypdf oracle 位于
`build/qa/a6-structure-discovery/final-structured-oracles.json`，均 PASS：一页、可见 token、无 poison；
旧候选对两份合法 predictor 样本的预期不匹配记录仍保留为反证。

诊断证据 `build/qa/a6-structure-discovery/diagnostic` 显示本地 `StrictFlate` 的 `NotSupported` 可被 PdfPig 吞掉，
随后 `Open` 抛 `PdfDocumentFormatException` 且 `InnerException` 为 null。资格 provider 因此按显式结构证据将每份
此类文档标记为 `unsupported` 拒绝，保留 typed failure 的优先顺序和全部既有预算；这不是把 warning 当作成功或扩大
PdfPig 的 Predictor 支持声明。

这一轮只完成有限 DISCOVERY 构造的可复现观察。真实复杂生产者、一般 Predictor 支持、warning 分类校准，以及产品
adapter 接入、generation 事务、授权和发布资格仍开放；本报告不采纳候选或关闭 A6。
