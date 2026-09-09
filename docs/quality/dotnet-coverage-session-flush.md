# .NET 会话内覆盖率收集资格

基线为 `3cd6f83202ea0977d690ad71cf7dff25595e4f02`，本页记录该基线上的 collector 候选。下文先保留该基线上的范围化运行与历史失败，最后一节记录同步 main 后的完整 .NET 覆盖率通过；fresh PR CI 尚未执行。

## 问题与边界

main CI `34304265651` 的 core job `102320648572` 在 PreviewHost 测试全部通过后，Coverlet 6.0.4 `CalculateCoverage` 的首次 `BinaryReader.ReadInt32` 抛出 `EndOfStreamException`。该位置说明读取时不足一个完整头部；没有保存当时 hits 文件或退出 trace，不能确认该次故障的具体退出时序。

[Coverlet 官方已知问题](https://github.com/coverlet-coverage/coverlet/blob/v10.0.1/Documentation/KnownIssues.md)说明 VSTest 结束 testhost 可能早于 MSBuild 模式的退出阶段收集，并推荐 collector。[固定 10.0.1 的 in-process collector](https://github.com/coverlet-coverage/coverlet/blob/v10.0.1/src/legacy/coverlet.collector/InProcDataCollection/CoverletInProcDataCollector.cs)在 `TestSessionEnd` 同步调用 `UnloadModule`，然后关闭重复退出 flush。单独升级 MSBuild 包仍保留 `ProcessExit` 机制。本片采用同步会话路径，但不声称已复现本次 CI 根因。

六测试项目精确固定 `coverlet.collector` 为 `10.0.1`，移除 MSBuild instrument/阈值属性。SDK、Test.Sdk、MSTest、生产 C#、`.ci/project.json` 六组阈值与 Contracts 原生成文件排除均不变。仓库没有已跟踪 NuGet `packages.lock.json`；本次通过正常 restore 生成资产，未手写锁文件。只补所需 collector 包，未重建共享环境。

`qa.dotnet_coverage` 是项目专属入口，`qa/next.py --stage dotnet` 调用它，原 15 分钟阶段时限不变。它保留旧 source/test/solution 双向清单、直接 ProjectReference、生成文件精确排除校验。一次 solution 调用为各测试项目提供自己的 runsettings 和本次唯一结果目录；只接受该项目成功 TRX 所引用的 collector 附件。唯一预期 package 与唯一 Include 下，根的原始 covered/valid 整数就是该程序集计数，逐项交叉相乘比较门槛，不使用舍入 rate 或跨程序集平均。缺失、重复、错绑定、collector RunInfo 错误、非法计数与非零测试退出均失败；当前六程序集均有 line/branch，零分母拒绝不代表所有无分支程序集的通用规则。未添加 SkipAutoProps、ExcludeByAttribute 或 MergeWith。

## 精确验证

以下日志均在 `build/qa/dotnet-coverage-session-flush/`，原始 TRX/Cobertura 在每次 `build/qa/dotnet-coverage/run-*/`。Python 使用仓库锁定共享环境，通过 `uv run --frozen --no-sync python`；.NET 使用固定 SDK 10.0.400 与既有 NuGet 缓存。`--no-cov` 仅用于这些 QA 适配器测试的范围化验证，不替代完整 Python 85% 门禁。

| 命令/检查 | 结果与日志 |
|---|---|
| 旧入口运行新增同步收集契约 | RED：1 FAIL，`collector-route-red.log` |
| collector error TRX + 通过测试/完整附件反例 | RED：1 FAIL，`collector-error-red.log`；修复后纳入最终集合 |
| `dotnet restore desktop/VibeTable.Desktop.sln --locked-mode` | EXIT 0，`restore.log` |
| `python -m pytest tests/test_dotnet_coverage.py tests/test_next_gate.py --no-cov -q` | 126 PASS，4.59 秒，`contracts-final.log` |
| `python -m ruff format qa/dotnet_coverage.py qa/next.py tests/test_dotnet_coverage.py tests/test_next_gate.py`，对应 `ruff check` | 格式无变化、check EXIT 0，`ruff-final.log` |
| `python -m pyright --pythonpath <shared-venv>/Scripts/python.exe qa/dotnet_coverage.py tests/test_dotnet_coverage.py` | 0 errors、0 warnings，`pyright-final.log`；输出本地 `.venv` 路径提示，未新建环境 |
| `python -m mypy qa/dotnet_coverage.py tests/test_dotnet_coverage.py` | EXIT 1：9 个传递导入错误，位于 `qa/release_candidate.py`、`handoff.py`、`release_eligibility.py`、`next.py` 既有路径，新模块和测试无自身报错；保留 `mypy-final.log`，未扩修类型范围 |

原 `tests/test_next_gate.py` 的覆盖率配置测试迁到新 `tests/test_dotnet_coverage.py`，补 collector 条件、资产、双 instrument、报告/项目绑定、整数阈值舍入、缺分母及真实 TRX Deployment 路径反例；没有删除对应门禁或降低阈值。

实际覆盖率命令为：

```powershell
uv run --frozen --no-sync python -m qa.dotnet_coverage --dotnet <fixed-sdk>/dotnet.exe --project desktop/tests/VibeTable.PreviewHost.Tests/VibeTable.PreviewHost.Tests.csproj
uv run --frozen --no-sync python -m qa.dotnet_coverage --dotnet <fixed-sdk>/dotnet.exe
uv run --frozen --no-sync python -m qa.dotnet_coverage --dotnet <fixed-sdk>/dotnet.exe --project desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj
```

- 首次 PreviewHost `run-yilugs33`：13 PASS；消费者误将 TRX href 相对于结果目录解析，最终 EXIT 1，`previewhost-first.log`。按真实 `Deployment/In/<machine>` 附件结构修正并补 fixture；只回放同报告后发现真实 branch 27/56 低于原 50%，`previewhost-report-replay.log`。原失败保留。
- 补 PreviewHost 已注册 COM 对象不支持预览接口的 STA 拒绝/释放回归；不 Show、不改变生产语义。`run-phewx515`：14 PASS、159 ms，line 71/161、branch 30/56，通过原 41%/50% 门槛，`previewhost-com-boundary.log`。
- 首次完整 solution `run-mrfm4yid`：1334 PASS、1 个既有权限 skip，测试进程成功，但 Contracts branch 445/860 低于原 56%，最终 EXIT 1，`solution-first.log`。其他五程序集均达原门槛，逐份读取结果见 `solution-counts.log`。不能把这次称为完整覆盖率通过。
- 旧格式样本与新 Contracts 报告均为 line 850/1679、branch 分母 860，差异行 hits 一致，covered branch 从 496 变为 445，分散在 13 行条件表达式；未证明具体算法差异，旧样本也不算本轮资格。原报告的字段操作 wrapper 本就未覆盖。本片补 describe、plan、apply receipt、migration、recycle 的外层协议与源/反向嵌套字段验证，保留合法字段、删除空定义和错误原因契约。`run-54tjtx19`：61 PASS、154 ms，line 890/1679、branch 502/860，通过原 49%/56% 门槛，`contracts-field-boundaries.log`。

| 程序集 | 本轮通过报告 | line covered/valid | branch covered/valid | 原 line/branch 门槛 |
|---|---|---|---|---|
| Desktop | `run-mrfm4yid` | 18183/25556 | 6823/11564 | 63/53 |
| Contracts | `run-54tjtx19` | 890/1679 | 502/860 | 49/56 |
| PreviewHost | `run-mrfm4yid` | 71/161 | 30/56 | 41/50 |
| Workspace | `run-mrfm4yid` | 578/590 | 218/228 | 92/85 |
| Infrastructure | `run-mrfm4yid` | 2512/3132 | 788/1158 | 74/64 |
| DocumentDiff.OpenXml | `run-mrfm4yid` | 295/363 | 156/194 | 78/79 |

此表是同一生产候选的分程序集证据，Contracts 后补测试的运行独立列出，没有拼成一次完整 solution 成功。该阶段尚未执行同步 main 后完整 solution；其后验证见下节。完整 Python 质量入口、完整产品 build/E2E 和 fresh CI 尚未执行。本片没有修改共享 automation core 或其他程序集生产行为；实际 Windows CI 仍需验证 collector 会话链与全门禁。

## 同步 main 后的完整 .NET 资格

单意图提交 `8999f9d127534eb491e0f33e07ab1c68e7f9217b` 正常执行 Ruff format/check、版本与包契约四个 hooks，全部通过。随后正常无冲突合并实际 main `25b260394a0a01e8432d23fa3d1a6e8b9b65922f`，固定测试候选为 `4f5ba25f6043a44030399ca57510d375925f838a`，覆盖率入口及门槛没有合并改动。

在此候选运行一次无 `--project` 的上述完整 `qa.dotnet_coverage` 命令，终态 **EXIT 0：1349 PASS、1 个既有权限 skip**。Desktop 1102 PASS/1 skip（24 秒）、Contracts 61 PASS（173 ms）、PreviewHost 14 PASS（165 ms）、Workspace 27 PASS（159 ms）、Infrastructure 128 PASS（7 秒）、OpenXml 17 PASS（321 ms）。跳过项为既有 `ActivationPointerLinkIsRejectedAndRetained`，没有新增跳过。

本次唯一结果目录 `build/qa/dotnet-coverage/run-sjw42_dx/` 的六份项目 TRX/附件均通过校验，生成 `coverage-summary.json`。原始输出为 `build/qa/dotnet-coverage-session-flush/solution-main.log`：

| 程序集 | line covered/valid | branch covered/valid | 原 line/branch 门槛 |
|---|---|---|---|
| Desktop | 18191/25566 | 6839/11584 | 63/53 |
| Contracts | 890/1679 | 502/860 | 49/56 |
| PreviewHost | 71/161 | 30/56 | 41/50 |
| Workspace | 578/590 | 218/228 | 92/85 |
| Infrastructure | 2512/3132 | 788/1158 | 74/64 |
| DocumentDiff.OpenXml | 295/363 | 156/194 | 78/79 |

六组原门槛全部通过。本次验证没有重跑旧失败 CI，也不证明旧 CI 的具体 shutdown 时序；完整 Python 质量入口、完整产品 build/E2E、fresh PR CI 仍未执行。

## 接续完整 Python 质量与独立复核

在固定 `4f5ba25f` 上，独立 Standards / Spec 两轴审查均无确定问题。没有修改生产实现或降低任何程序集门槛。

首次执行 `uv run --frozen --no-sync python scripts/automation_project.py python-quality`，Ruff format/check、backend Pyright/mypy 通过；测试为 1829 PASS、1 个既有 skip、3 FAIL，后端覆盖率 91.43%。失败分别是 generator 子进程通过共享虚拟环境的 editable 安装导入另一工作区代码，以及两个 Node 入口找不到本 worktree 的 Web 依赖，详见 `python-quality-main.log`。这些失败保留，不记为产品断言通过。

确认共享来源的 Web package/lock 与当前源码一致后，通过本地 junction 复用已有 `node_modules`，命令级 `PYTHONPATH` 固定到当前 worktree；共享环境未重建或修改。三项失败复验 3 PASS、7.45s，`environment-correction.log`。随后同一完整 Python 质量入口 EXIT 0：Ruff format/check、backend Pyright/mypy 全部通过，1832 PASS、1 个既有 skip、78.70s，后端覆盖率 91.43%，满足原 85% 门槛；日志 `python-quality-correct-environment.log`。早期 QA 模块范围化 mypy 的传递导入错误记录仍保留，不由 backend-only 检查覆盖。

最新源码的完整产品构建/E2E 与 fresh PR CI 尚待执行。本片只改变质量收集路径和直接相关测试，不为此重复生成本地可执行包；完整 CI 的 release build/smoke 与产品分片仍是合并门禁。
