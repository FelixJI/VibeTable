## 背景与根因

问题触发条件、根因及本 PR 对应的一个可验收行为：

关联 Issue：

## 变更内容、影响与风险

范围、不变量、兼容/回滚：

## Acceptance Evidence

| AC | 结果 | 可核实证据 |
|---|---|---|
| AC1 | 未验证 | |

## 验证

精确命令 / cwd / 退出码 / SHA / CI 结果位置；未执行项写明原因，pending 不得写为 passed：

WPF/Web 可见改动截图（适用时）：

基线已有失败 / 新增失败 / 未跑项：

## Routing / Risk

Difficulty: L? — 理由：

Risk: R? — 理由：

规划建议实施者：Codex / GLM / Codex-first → GLM

实际实施者：Codex / GLM / Codex + GLM

## Reviewer Subagent

reviewer：Codex fresh read-only subagent

reviewed_head_sha / reviewed_base_sha：

Verdict: 未审阅 / PASS / CHANGES_REQUIRED / INSUFFICIENT_EVIDENCE

审阅评论 / findings / 未解决 blockers：

新提交后的验证与增量复核：

## Manual Handoff / Merge

Issue 当前 GLM handoff_id、阶段和下一动作：

只有 Codex ↔ GLM 跨工具时由用户手工启动下一侧；reviewer 子代理由 Codex 自动启动。MERGE_READY 不是合并结果，最终合并由用户决定。

发布/生产/真实数据操作另行授权。

仅人工 squash merge；保留严格同步 main 的 required 门禁和所有 review conversation 处理要求，不使用 admin/bypass。
