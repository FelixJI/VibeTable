import type { RetentionPolicyV2 } from "@/contracts/workspaceV2";

type RetentionBucketValue = RetentionPolicyV2["snapshotBuckets"][number];

export interface RetentionDraftValues {
  snapshotDays: number;
  snapshotCount: number;
  snapshotBuckets: RetentionBucketValue[];
  fileRevisionDays: number;
  fileRevisionCount: number;
  fileRevisionBuckets: RetentionBucketValue[];
  repositoryLimitBytes: number | null;
}

// 与 sidecar internal/workspacev2/state.go 的 defaultRetentionPolicy 保持
// 一致（产品文案已声明：快照 30 天/至少 50 个、文件版本 30 天/至少 100 个）。
// 仅用于“恢复默认”草稿预填；策略修订号不进入草稿，authority 在
// retention.update 时仍以 expectedRevision 校验并重新验证全部取值。
export const DEFAULT_RETENTION_DRAFT: RetentionDraftValues = {
  snapshotDays: 30,
  snapshotCount: 50,
  snapshotBuckets: ["hourly", "daily", "weekly", "monthly"],
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
