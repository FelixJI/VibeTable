# ADR 0011：Workspace 版本采用 N-1 验证策略

- 状态：已接受
- 日期：2026-08-27
- 当前 writer：VibeTable 0.5.1

## 背景

Workspace manifest、可交换的 SnapshotPackage 和 Snapshot 内部对象各自携带版本字段，但它们不是
同一个格式。现有运行时精确读写 workspace manifest format 2；SnapshotPackage 的 `Export` 与
`Inspect` 也精确要求 package `formatVersion: 2`。内部 snapshot manifest 当前格式也是 2：sidecar
coordinator 以 format 2 构造它，bundle parser 也只接受 format 2，Export path 将该 payload 放入
`snapshot/manifest.json`。内部 snapshot manifest 与 SnapshotPackage 是独立版本轴；当前数值相同不代表
二者以后必须同步升级，也不能相互替代。

WorkspaceManifest 还携带独立的 `topologySchemaVersion` 与 `businessSchemaVersion`。当前 C# writer 均写入
1；C# 与 Go manifest reader 要求两者为正值，Go snapshot database reader 另以
`SupportedBusinessSchemaVersion = 1` 精确限制 business schema。policy 因此冻结 current writer 的两个
值为 1，但不把 topology 的“当前 writer 值”误写成 reader 仅支持 1 的承诺。

仓库已有 `compatibility-corpus.json`，其中的 checksum 能发现当前 fixture 与声明不一致，却不能证明
历史条目没有与 fixture/checksum 在同一次提交中一起改写。历史上这类同步更新已经发生过，因此
`appendOnly: true` 和自带 checksum 不能单独充当 append-only 证据。

当前应用版本是 0.5.1，上一正式版本是 v0.5.0。仓库没有由 v0.5.0 正式发布包产生并经过当前 reader
验证的独立 Workspace/SnapshotPackage corpus，所以不能宣称 v0.5.0 已兼容。

## 决策

### 1. 单一公开 policy seam

`contracts/v2/workspace-version-policy.json` 是版本支持声明的 machine-readable interface；
`workspace-version-policy.schema.json` 以 closed schema 约束其形状和状态词汇。调用方只需要读取这一份
policy，不从 release tag、运行时代码或 fixture 猜测支持范围。

发布包中的 policy、schema 与 corpus 必须分别和构建该包的 source tree 权威文件结构化一致；其中
`currentWriter.appVersion` 还必须同时等于 `release.json.version` 与版本唯一来源派生的 app version。
package qualification 在这些 identity 绑定通过后仍独立校验 closed schema、semantic policy、corpus
artifact 存在性与 checksum。包内同时改写 policy 的 current/accepted 版本，或改写 frozen corpus artifact
并同步自带 checksum，都不能形成一份新的自洽 authority。

`writerCompatibility.verificationGate` 在本 revision 固定为
`disabled-until-packaged-runtime-evidence`。它是 closed capability 状态，不是说明文字：PR-14a 尚未拥有
可验证 GitHub formal Release/tag/asset 的 producer，也没有 PR-14b/14c 的 packaged runtime
reader/import/零写入 execution consumer，因此任何 `accepted.status: verified` 都必须稳定 fail closed。

JSON Schema 不负责跨字段关系。`scripts/versioning.py` 的 semantic validator 额外保证：`accepted`
恰有一条 `current` 且版本等于 `currentWriter.appVersion`；`nMinusOneTarget` 恰好出现在 accepted 或
pending 之一；pending 最多一条且只能是该 N-1 目标；两个集合互斥；workspace manifest 与
SnapshotPackage 的公开 format 字段分别和 current writer 一致。accepted/pending 的版本闭集只能是
current writer 与 N-1 target；任何 `verified` target 还必须命中 anchor 的 `formalReleaseCount` 所冻结的
`previousFormalReleases` 前缀。内部 snapshot manifest 与 WorkspaceManifest schema version 继续通过真实
C#/Go writer/reader source contract 独立绑定，不因当前数值相同而和 package format 建立关系。

本 revision 冻结以下事实：

- current writer 是 VibeTable 0.5.1；
- workspace manifest 当前且唯一支持 format 2；
- WorkspaceManifest current writer 的 topology schema 与 business schema version 均为 1；
- SnapshotPackage 当前格式与最低支持格式均为 2；
- SnapshotPackage 内部 snapshot manifest 当前格式为 2，但独立于 package format 演进；
- writer compatibility 的目标窗口是当前正式版本 N 与上一正式版本 N-1。

### 2. 接受与待验证是不同状态

当前 writer 0.5.1 以 `current` 状态列入 `accepted`。v0.5.0 只能列入 `pending`，并必须同时标记
`compatibility: unverified`。pending 条目不是打开、迁移或写回旧工作区的兼容性保证，不得用于产品
文案、Release 说明或验收结论中的兼容性声明。本 policy-only 变更不改写现有 runtime admission；
runtime 通过版本字段只是必要条件，不是旧版兼容性证据。

`nMinusOneTarget` 显式记录当前窗口的上一正式版本，其目标值以及 `accepted/verified` 或
`pending/unverified` 状态由正式 Release corpus 证据所有。release versioning module 更新 app version
时，只更新 `currentWriter.appVersion` 与 `accepted` 中唯一的 `current` 条目，保留目标值和已有证据状态；
推进目标或改变证据状态必须由独立的正式 Release 证据驱动，不能从 previous writer 推断。

只有取得由对应正式发布版本产生的代表性 Workspace/SnapshotPackage，并通过当前 reader 的读取、
迁移（如有）和零误写失败路径验证后，后续决策才能把该版本从 `pending` 提升为 `verified`。仅由当前
writer 重新生成 fixture、修改 `minimumAppVersion`，或让 schema 校验通过，都不构成 N-1 证据。
promotion 还必须先把正式 Release 条目追加到 `previousFormalReleases`，由后续独立已合并 anchor 的
`formalReleaseCount` 冻结该条目；空 corpus、未进入冻结前缀的尾项或额外历史版本均不能产生 verified。
正式条目采用 closed evidence shape：`sourceRelease` 精确绑定 `v{writerVersion}` tag、40 位 source commit
与 release asset name；artifact 清单必须包含真实存在且 checksum 匹配的 workspace archive、
SnapshotPackage 以及供拒绝路径使用的输入，所有 artifact id 唯一且 path 受限于 corpus 目录。closed cases
只能引用清单中的 artifact，并至少覆盖 `workspace.open` 与 `snapshot.import` 的 read-or-migrate，以及一条
`reject-zero-write`。不得加入自我声明的 `tested`、`passed` 等布尔结论替代这些可验证关系。

PR-14a 只冻结 evidence interface 与静态完整性契约，不执行旧版本 runtime 消费验证；promotion 必须等待
PR-14b/14c 的真实 reader/import/零写入测试产出机器证据，并由后续 policy revision 显式启用新的
verification gate 后才能发生。即使 corpus 条目已由 remote-main anchor 冻结、静态 evidence shape 与
artifact checksum 全部通过，当前 disabled gate 仍不得授权 promotion；anchor 只证明历史不可原位改写，
不把任意 40 位 commit、文件字节和自声明 cases 变成执行结论。

### 3. 旧格式 fail closed

workspace manifest format 小于 2 时，当前产品明确拒绝，执行零写入，也不提供未经验证的自动迁移。
format 大于 2 时按 newer-format 路径拒绝并保持零写入。拒绝不能创建同名空工作区、修改原 manifest、
数据库、repository 或 Snapshot。

SnapshotPackage 只接受 format 2；小于或大于 2 的 package 均拒绝并保持零写入。package metadata 中的
`writerVersion` 与 `minimumAppVersion` 是输入兼容性 guard，不会把尚未验证的 v0.5.0 自动提升为
accepted。内部 snapshot manifest 当前也是 format 2，但它不属于本条 SnapshotPackage 最低版本承诺；
两个版本字段恰好同值仍必须分别验证。

### 4. Corpus 只允许尾部追加

append-only 精确定义为：已经冻结的 `baselines` 与 `previousFormalReleases` 前缀不得删除、重排、替换
或原位修改；新证据只能追加在对应数组尾部。artifact checksum 继续用于验证 corpus 当前条目引用的
字节，但不承担历史不可重写证明。

独立权威 producer 是已合并的 Git commit
`b28a0fc3f0829ed9fd7c9b974daf41d350eba560` 中的
`contracts/v2/compatibility-corpus.json` tree entry。policy 记录该 anchor 与已冻结条目计数；contract
test 只从本地 Git object database 读取 anchor，并从 `.ci/project.json` 取得正式 GitHub repository
identity；只有 URL 精确指向该 identity 的 GitHub remote 所对应的 `refs/remotes/<remote>/main` 才是
authority ref。测试要求 anchor 是该 ref 的祖先，再验证 anchor 含目标路径并对当前 corpus 做结构化前缀比较。
因此 feature branch 不能先在 commit A 改 corpus、再在 commit B 把 A 写成 anchor 自我批准；A 尚未
进入正式仓库 remote main，测试立即失败，而不是等 squash 后的 main CI 才暴露；fork 或 mirror 上的
同名 main 也不能充当权威。测试不访问网络，也不使用
corpus 自己声明的 checksum 证明自身不可改写。CI checkout 已配置 `fetch-depth: 0`；缺少 remote main
authority ref、anchor 不可达或不含目标路径一律以清晰错误 fail closed。
现有条目中的 `baselineParentCommit` 只保留为历史描述字段，不是 immutable producer，也不参与该证明。

以后扩展冻结前缀时，新增 corpus 尾项必须先存在于一个独立、已合并且不可改写的 producer commit；
后续变更才能把 anchor 和计数前移到该 commit。不得在加入条目的同一提交中用同步修改 artifact、
checksum、policy 与测试的方式自我批准。

## 后果

### 正向

- 产品、测试和发布说明有一份明确区分 current、verified 与 pending/unverified 的声明。
- workspace manifest、SnapshotPackage 与内部 snapshot manifest 三个版本轴被分别声明和验证。
- v0.5.0 的兼容性保持诚实的待验证状态。
- corpus 历史前缀由独立 Git tree 约束，同提交同步改写 artifact/checksum 会被 contract test 拒绝。

### 成本

- N-1 promotion 需要真实旧版 producer 与后续冻结步骤，不能只改一份 JSON 宣告完成。
- 在旧版证据补齐前，策略窗口是目标而不是已经兑现的兼容性承诺。
- 本地运行 append-only contract test 需要包含 anchor 的 Git 历史；官方 CI 的 full-history checkout
  满足这一条件，缺少历史的 source snapshot 必须显式补齐历史后再运行该测试。

## 被否决方案

- 把 v0.5.0 直接列为 `verified`：没有旧版正式产物 corpus，属于伪造兼容性结论。
- 因 SnapshotPackage 与内部 snapshot manifest 当前都为 format 2 就把它们合并为一个字段：这会令任一
  版本轴独立演进时失去明确的 writer/parser 契约。
- 继续仅靠 `appendOnly: true` 与 artifact SHA-256：producer 与 consumer 同源，不能阻止同提交同步改写。
- 测试运行时查询 GitHub 或动态选择 merge-base：引入网络和 PR checkout 形态差异，不能形成稳定权威。
- 接受任意 `refs/remotes/*/main`：fork 或 mirror 可以伪装成已合并 producer，不能证明条目进入正式仓库。
