# 产品 E2E 历史失败记录

此页保留尚未查明根因的失败出处。通过样本的 source、报告契约和场景清单统一见
[当前产品 E2E 证据](../e2e-performance.md#当前产品-e2e-证据)，不能用后续通过记录注销未归因的失败。

## 目录副本恢复按钮不可用

[PR #246 CI 34026490000](https://github.com/FelixJI/VibeTable/actions/runs/34026490000)，
head `3a3fbbce17149cd70b406ce27099086a76788b1f`：场景 23 等待释放活动缓存按钮可用超过 90 秒。
诊断中没有对应的释放请求，按钮尚未准入。后续本地场景通过不证明本次失败根因已修复。

## 文档列表桥接失败

[main CI 34024595177](https://github.com/FelixJI/VibeTable/actions/runs/34024595177)，
source `4be7c93d86c9453b7befd20544ac0635507f8ebe`：场景 14 存在未确认桥接失败；
文档列表与保留策略查询未留下可归因的错误码。后续 diff/restore 成功不能补足缺失的失败原因。

以上两项保持未关闭。此页不增加当前通过场景、重试预算或任何能力声明。
