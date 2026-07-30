import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type { FileDocumentV2, RetentionPolicyV2 } from "@/contracts/workspaceV2";
import type {
  CleanupPlanResult,
  ConflictPlanResult,
  FileRevisionTreeProjection,
  PendingFileChange,
  RepositoryKeyRotationPlan,
  RepositoryVerificationResult,
  RetentionProtectionStatus,
  RestorePlanResult,
  SnapshotExtractPlan,
  SnapshotPackagePlan,
  SnapshotTimelineItem,
  WorkspaceConflictItem,
  WorkspaceConflictSummary,
  WorkspaceStoragePlan,
  WorkspaceStorageProjection,
} from "@/contracts/workspaceV2Bridge";
import { registerWorkspaceEpochReset } from "@/stores/workspaceSessionStore";

export type {
  FileRevisionTreeProjection,
  SnapshotTimelineItem,
  WorkspaceConflictItem,
  WorkspaceConflictSummary,
  WorkspaceStorageProjection,
} from "@/contracts/workspaceV2Bridge";

export const useWorkspaceProtectionStore = defineStore("workspace-protection-v2", () => {
  const snapshots = ref<readonly SnapshotTimelineItem[]>([]);
  const selectedSnapshotId = ref<string | null>(null);
  const storage = ref<WorkspaceStorageProjection | null>(null);
  const storagePlan = ref<WorkspaceStoragePlan | null>(null);
  // Null is the only pre-bootstrap state: Web never owns product defaults.
  const retention = ref<RetentionPolicyV2 | null>(null);
  const retentionHydrated = ref(false);
  const retentionStatus = ref<RetentionProtectionStatus | null>(null);
  const conflicts = ref<readonly WorkspaceConflictItem[]>([]);
  const conflictSets = ref<readonly WorkspaceConflictSummary[]>([]);
  const fileTrees = ref<Readonly<Record<string, FileRevisionTreeProjection>>>({});
  const pendingFileChanges = ref<readonly PendingFileChange[]>([]);
  const documents = ref<readonly FileDocumentV2[]>([]);
  const restorePlan = ref<RestorePlanResult | null>(null);
  const extractPlan = ref<SnapshotExtractPlan | null>(null);
  const repositoryVerification = ref<RepositoryVerificationResult | null>(null);
  const keyRotationPlan = ref<RepositoryKeyRotationPlan | null>(null);
  const retentionPlan = ref<CleanupPlanResult | null>(null);
  const conflictPlans = ref<Readonly<Record<string, ConflictPlanResult>>>({});
  const snapshotPackagePlan = ref<SnapshotPackagePlan | null>(null);
  const busyOperation = ref<string | null>(null);
  const operationError = ref<string | null>(null);

  const selectedSnapshot = computed(() =>
    snapshots.value.find((snapshot) => snapshot.snapshotId === selectedSnapshotId.value) ?? null);
  const pendingConflictCount = computed(() =>
    conflictSets.value.filter((conflict) => conflict.state !== "ready").length);

  registerWorkspaceEpochReset("workspace-protection-v2", () => {
    snapshots.value = [];
    selectedSnapshotId.value = null;
    storage.value = null;
    storagePlan.value = null;
    retention.value = null;
    retentionHydrated.value = false;
    retentionStatus.value = null;
    conflicts.value = [];
    conflictSets.value = [];
    fileTrees.value = {};
    pendingFileChanges.value = [];
    documents.value = [];
    restorePlan.value = null;
    extractPlan.value = null;
    repositoryVerification.value = null;
    keyRotationPlan.value = null;
    retentionPlan.value = null;
    conflictPlans.value = {};
    snapshotPackagePlan.value = null;
    busyOperation.value = null;
    operationError.value = null;
  });

  function setSnapshots(next: readonly SnapshotTimelineItem[]): void {
    snapshots.value = [...next];
    if (
      selectedSnapshotId.value
      && !snapshots.value.some((snapshot) => snapshot.snapshotId === selectedSnapshotId.value)
    ) {
      selectedSnapshotId.value = null;
    }
  }

  function upsertSnapshot(next: SnapshotTimelineItem): void {
    const index = snapshots.value.findIndex((item) => item.snapshotId === next.snapshotId);
    snapshots.value = index < 0
      ? [next, ...snapshots.value]
      : snapshots.value.map((item) => item.snapshotId === next.snapshotId ? next : item);
  }

  function selectSnapshot(snapshotId: string | null): void {
    selectedSnapshotId.value = snapshotId;
  }

  function setStorage(next: WorkspaceStorageProjection | null): void {
    storage.value = next;
  }

  function setStoragePlan(next: WorkspaceStoragePlan | null): void {
    storagePlan.value = next;
  }

  function setRetention(next: RetentionPolicyV2): void {
    retention.value = next;
    retentionHydrated.value = true;
  }

  function setRetentionStatus(next: RetentionProtectionStatus | null): void {
    retentionStatus.value = next;
  }

  function setConflicts(next: readonly WorkspaceConflictItem[]): void {
    conflicts.value = [...next];
    if (!next.length) return;
    const summaries = new Map(
      conflictSets.value.map((summary) => [summary.conflictId, summary]),
    );
    const grouped = new Map<string, WorkspaceConflictItem[]>();
    for (const item of next) {
      const items = grouped.get(item.conflictId) ?? [];
      items.push(item);
      grouped.set(item.conflictId, items);
    }
    for (const [conflictId, items] of grouped) {
      const existing = summaries.get(conflictId);
      summaries.set(conflictId, {
        conflictId,
        state: items[0]!.state,
        createdAt: existing?.createdAt ?? "—",
        itemCount: items.length,
      });
    }
    conflictSets.value = [...summaries.values()];
  }

  function setConflictSets(next: readonly WorkspaceConflictSummary[]): void {
    conflictSets.value = [...next];
    const current = new Set(next.map((item) => item.conflictId));
    conflicts.value = conflicts.value.filter((item) => current.has(item.conflictId));
  }

  function chooseConflict(
    conflictId: string,
    itemId: string,
    choice: "local" | "replica" | "both",
  ): void {
    conflicts.value = conflicts.value.map((conflict) =>
      conflict.conflictId === conflictId && conflict.itemId === itemId
        ? { ...conflict, selected: choice }
        : conflict);
  }

  function setFileTree(next: FileRevisionTreeProjection): void {
    fileTrees.value = { ...fileTrees.value, [next.documentId]: next };
  }

  function setPendingFileChanges(next: readonly PendingFileChange[]): void {
    pendingFileChanges.value = [...next];
  }

  function setDocuments(next: readonly FileDocumentV2[]): void {
    documents.value = [...next];
  }

  function removePendingFileChange(changeId: string): void {
    pendingFileChanges.value = pendingFileChanges.value.filter(
      (change) => change.changeId !== changeId,
    );
  }

  function setRestorePlan(next: RestorePlanResult | null): void {
    restorePlan.value = next;
  }

  function setExtractPlan(next: SnapshotExtractPlan | null): void {
    extractPlan.value = next;
  }

  function setRepositoryVerification(
    next: RepositoryVerificationResult | null,
  ): void {
    repositoryVerification.value = next;
  }

  function setKeyRotationPlan(next: RepositoryKeyRotationPlan | null): void {
    keyRotationPlan.value = next;
  }

  function setRetentionPlan(next: CleanupPlanResult | null): void {
    retentionPlan.value = next;
  }

  function setConflictPlan(conflictId: string, next: ConflictPlanResult | null): void {
    const plans = { ...conflictPlans.value };
    if (next) plans[conflictId] = next;
    else delete plans[conflictId];
    conflictPlans.value = plans;
  }

  function setSnapshotPackagePlan(next: SnapshotPackagePlan | null): void {
    snapshotPackagePlan.value = next;
  }

  function beginOperation(operation: string): boolean {
    if (busyOperation.value) return false;
    busyOperation.value = operation;
    operationError.value = null;
    return true;
  }

  function finishOperation(error?: string): void {
    busyOperation.value = null;
    operationError.value = error ?? null;
  }

  return {
    snapshots,
    selectedSnapshotId,
    selectedSnapshot,
    storage,
    storagePlan,
    retention,
    retentionHydrated,
    retentionStatus,
    conflicts,
    conflictSets,
    pendingConflictCount,
    fileTrees,
    pendingFileChanges,
    documents,
    restorePlan,
    extractPlan,
    repositoryVerification,
    keyRotationPlan,
    retentionPlan,
    conflictPlans,
    snapshotPackagePlan,
    busyOperation,
    operationError,
    setSnapshots,
    upsertSnapshot,
    selectSnapshot,
    setStorage,
    setStoragePlan,
    setRetention,
    setRetentionStatus,
    setConflicts,
    setConflictSets,
    chooseConflict,
    setFileTree,
    setPendingFileChanges,
    setDocuments,
    removePendingFileChange,
    setRestorePlan,
    setExtractPlan,
    setRepositoryVerification,
    setKeyRotationPlan,
    setRetentionPlan,
    setConflictPlan,
    setSnapshotPackagePlan,
    beginOperation,
    finishOperation,
  };
});
