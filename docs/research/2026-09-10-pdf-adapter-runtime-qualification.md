# A6 PDF 候选的隔离运行资格

当前结论：固定 PdfPig 0.1.16 与 SharpZipLib 1.4.2 的实验工具在 28 项自有语料上与冻结预期一致。
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
uv run --frozen --no-sync python qa/pdf_adapter_qualification.py tests/contract/pdf_qualification_corpus.json build/qa/pdf-qualification/v1 build/qa/pdf-adapter/artifacts/bin/PdfAdapterQualification/release/PdfAdapterQualification.exe --memory-mib 1024 --output build/qa/pdf-adapter/observations-final-1g.json
go -C sidecar build -o ../build/qa/pdf-adapter/pdf-qualification.exe ./cmd/pdf-qualification
build/qa/pdf-adapter/pdf-qualification.exe --observations tests/contract/pdf_qualification_corpus.json build/qa/pdf-adapter/observations-final-1g.json
```

12 项进程检查包含成功、deadline、取消、创建前取消、CPU、托管/原生内存、两类输出上限、后代进程、
部分输出后失败及创建失败。原生分配检查确实遇到 commit 拒绝；不以 peak 小于配置限额冒充内存限额证据。
持久 pytest 入口为 `tests/contract/test_pdf_adapter_qualification.py`，同时覆盖后续页正负语料。

## 本次证据与限制

- .NET 10.0.401 构建零 warning/error；12 项进程检查全部通过且均清理，证据 `build/qa/pdf-adapter-runner/persistent-checks.json`。
- 28 项原始观察 `build/qa/pdf-adapter/observations-final-1g.json`；比较 `comparison-final-1g.json` 为 failed=0、exit 0。
- 旧原型在损坏后续页上仍返回 truncated、warning 空；`old-probe-later-invalid.json` 保留该反证。候选返回 failed / extract.pdf_stream_invalid、空正文。
- 本次最高根 worker working set 为 754,130,944 字节；对应 Job peak commit 734,937,088 字节。仅是当前机器单次测量，不是性能承诺。
- 初始比较器将毫秒声明为整数，拒绝实际 296.875ms；现接受有限非负小数，保留原始测量，增加小数和非法测量回归。
- 单独运行首次进程 pytest 是 1 passed，但命令因未运行 backend 覆盖率为 0 而 exit 1；不记作完整质量通过。

对象流/predictor、独立可再分发生产者、加密和深层循环、warning 误拒绝、完整 generation/授权/发布集成、
NOTICE/SBOM 与产品 adapter 决策仍未完成。此工具的包引用仅用于资格 executable，不进入当前产品包。

完整质量补充：`uv run --frozen --no-sync python scripts/automation_project.py python-quality`
首次 1897 passed / 1 skipped / 2 failed，覆盖率 91.88%；两个失败均为该新 worktree 缺少 Web 依赖。
确认锁文件一致并复用本机既有 Web 环境后，重跑 exit 0，1899 passed / 1 skipped、覆盖率 91.88%，
其中新增三项 PDF 集成回归全部通过。唯一 skip 为既有 Windows symlink 特权不足场景。
日志分别为 `build/qa/pdf-adapter/python-quality.log` 和 `python-quality-reused-web.log`。
`go test ./cmd/pdf-qualification` 通过；相关 Python Ruff 与 Pyright 通过。
