import type { RetentionPolicyV2 } from "@/contracts/workspaceV2";

export interface RetentionDraftValues {
  snapshotDays: number;
  snapshotCount: number;
  snapshotBuckets: RetentionPolicyV2["snapshotBuckets"][number][];
  fileRevisionDays: number;
  fileRevisionCount: number;
  fileRevisionBuckets: RetentionPolicyV2["fileRevisionBuckets"][number][];
  repositoryLimitBytes: number | null;
}

// 与 sidecar internal/workspacev2/state.go 的 defaultRetentionPolicy 保持
// 一致：智能快照最长两年/至少 50 个；现有工作区策略不自动覆盖。
// 仅用于“恢复默认”草稿预填；策略修订号不进入草稿，authority 在
// retention.update 时仍以 expectedRevision 校验并重新验证全部取值。
export const DEFAULT_RETENTION_DRAFT: RetentionDraftValues = {
  snapshotDays: 730,
  snapshotCount: 50,
  snapshotBuckets: ["smart"],
  fileRevisionDays: 30,
  fileRevisionCount: 100,
  fileRevisionBuckets: ["daily", "weekly", "monthly"],
  repositoryLimitBytes: null,
};

export function cloneDefaultRetentionDraft(): RetentionDraftValues {
  return {
    ...DEFAULT_RETENTION_DRAFT,
    snapshotBuckets: [...DEFAULT_RETENTION_DRAFT.snapshotBuckets],
    fileRevisionBuckets: [...DEFAULT_RETENTION_DRAFT.fileRevisionBuckets],
  };
}
