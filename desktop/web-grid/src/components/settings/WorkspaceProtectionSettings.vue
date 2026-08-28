<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NTag,
} from "naive-ui";
import {
  ArchiveRestore,
  CheckCircle2,
  Copy,
  Download,
  ExternalLink,
  FileDown,
  HardDrive,
  Pin,
  PinOff,
  Plus,
  RefreshCw,
  RotateCw,
  ShieldAlert,
  ShieldCheck,
} from "@lucide/vue";
import { t } from "@/i18n";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceProtectionStore, type SnapshotTimelineItem } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type { WorkspaceV2UiAction } from "@/contracts/workspaceV2Bridge";
import type { RetentionPolicyV2 } from "@/contracts/workspaceV2";
import {
  HOST_SNAPSHOT_EXTRACT_GRANT,
  HOST_SNAPSHOT_EXPORT_GRANT,
  HOST_SNAPSHOT_IMPORT_GRANT,
  HOST_WORKSPACE_ROOT_GRANT,
} from "@/services/workspaceV2HostAdapter";
import {
  canUseSnapshotExport,
  type SnapshotExportIdentity,
} from "./snapshotExportGuard";

export type WorkspaceProtectionAction = WorkspaceV2UiAction<
  | "snapshot.request"
  | "workspace.storage.preview"
  | "workspace.storage.apply"
  | "snapshot.list"
  | "snapshot.inspect"
  | "snapshot.update"
  | "snapshot.previewRestore"
  | "snapshot.applyRestore"
  | "snapshot.openAsNewWorkspace"
  | "snapshot.previewExtract"
  | "snapshot.applyExtract"
  | "snapshot.export"
  | "snapshot.inspectPackage"
  | "snapshot.import"
  | "repository.verify"
  | "repository.previewKeyRotation"
  | "repository.applyKeyRotation"
  | "retention.get"
  | "retention.status"
  | "retention.update"
  | "retention.plan"
  | "retention.apply"
>;

const { mode } = defineProps<{ mode: "versions" | "storage" }>();
const emit = defineEmits<{ action: [action: WorkspaceProtectionAction] }>();
const ui = useUiStore();
const session = useWorkspaceSessionStore();
const protection = useWorkspaceProtectionStore();
const restoreTarget = ref<SnapshotTimelineItem | null>(null);
const restoreTrigger = ref<HTMLElement | null>(null);
const importTrigger = ref<HTMLElement | null>(null);
const importCredential = ref("");
const exportTarget = ref<SnapshotTimelineItem | null>(null);
const exportIdentity = ref<SnapshotExportIdentity | null>(null);
const exportProtection = ref<"none" | "recipient" | "passphrase">("none");
const exportRecipient = ref("");
const exportCredential = ref("");
const extractTarget = ref<SnapshotTimelineItem | null>(null);
const extractDocumentId = ref<string | null>(null);
const convenientPasswordCopied = ref(false);
const storageConfirmation = ref("");

watch(
  () => [
    session.policyEnabled,
    session.activeWorkspaceId,
    session.sessionEpoch,
    protection.retentionHydrated,
    protection.retentionStatus,
  ] as const,
  ([policyEnabled]) => {
    if (!policyEnabled) return;
    if (!protection.retentionHydrated) {
      emit("action", { method: "retention.get", params: {} });
      return;
    }
    if (protection.retentionStatus === null) {
      emit("action", { method: "retention.status", params: {} });
    }
  },
  { immediate: true },
);

type RetentionBucket = RetentionPolicyV2["snapshotBuckets"][number];
const emptyRetentionDraft = () => ({
  snapshotDays: 0,
  snapshotCount: 0,
  snapshotBuckets: [] as RetentionBucket[],
  fileRevisionDays: 0,
  fileRevisionCount: 0,
  fileRevisionBuckets: [] as RetentionBucket[],
  repositoryLimitBytes: null as number | null,
});
const retentionDraft = ref(emptyRetentionDraft());
const gibibyte = 1024 ** 3;
const repositoryLimitGiB = computed<number | null>({
  get: () => retentionDraft.value.repositoryLimitBytes === null
    ? null
    : retentionDraft.value.repositoryLimitBytes / gibibyte,
  set: (value) => {
    retentionDraft.value.repositoryLimitBytes = value === null
      ? null
      : Math.round(value * gibibyte);
  },
});

watch(
  () => protection.retention,
  (next) => {
    if (!next) {
      retentionDraft.value = emptyRetentionDraft();
      return;
    }
    retentionDraft.value = {
      snapshotDays: next.snapshotDays,
      snapshotCount: next.snapshotCount,
      snapshotBuckets: [...next.snapshotBuckets],
      fileRevisionDays: next.fileRevisionDays,
      fileRevisionCount: next.fileRevisionCount,
      fileRevisionBuckets: [...next.fileRevisionBuckets],
      repositoryLimitBytes: next.repositoryLimitBytes,
    };
  },
  { deep: true },
);
watch(
  () => protection.storagePlan,
  (next) => {
    if (next) storageConfirmation.value = "";
  },
);
watch(
  () => protection.snapshotPackagePlan,
  (next, previous) => {
    if (previous && !next) {
      importCredential.value = "";
      const target = importTrigger.value;
      importTrigger.value = null;
      void nextTick(() => target?.focus({ preventScroll: true }));
    }
  },
);

const selected = computed(() => protection.selectedSnapshot);
const busy = computed(() => protection.busyOperation !== null);
const exportContext = computed(() => ({
  busy: busy.value,
  transitioning: session.isTransitioning,
  workspaceId: session.activeWorkspaceId,
  sessionEpoch: session.sessionEpoch,
}));
const canOpenExport = computed(() => canUseSnapshotExport(exportContext.value));
const canConfirmExport = computed(() => exportIdentity.value !== null
  && canUseSnapshotExport(exportContext.value, exportIdentity.value));

watch(
  () => [session.activeWorkspaceId, session.sessionEpoch] as const,
  ([workspaceId, sessionEpoch]) => {
    const opened = exportIdentity.value;
    if (opened && (opened.workspaceId !== workspaceId || opened.sessionEpoch !== sessionEpoch)) {
      closeExport();
    }
  },
);
const retentionDirty = computed(() =>
  protection.retentionHydrated
  && protection.retention !== null
  && (retentionDraft.value.snapshotDays !== protection.retention.snapshotDays
  || retentionDraft.value.snapshotCount !== protection.retention.snapshotCount
  || retentionDraft.value.snapshotBuckets.join(",") !== protection.retention.snapshotBuckets.join(",")
  || retentionDraft.value.fileRevisionDays !== protection.retention.fileRevisionDays
  || retentionDraft.value.fileRevisionCount !== protection.retention.fileRevisionCount
  || retentionDraft.value.fileRevisionBuckets.join(",") !== protection.retention.fileRevisionBuckets.join(",")
  || retentionDraft.value.repositoryLimitBytes !== protection.retention.repositoryLimitBytes));
const retentionBucketOptions = computed(() =>
  (["hourly", "daily", "weekly", "monthly"] as const).map((value) => ({
    value,
    label: t(`workspaceV2.retention.bucket.${value}`),
  })));
const extractDocumentOptions = computed(() =>
  protection.documents.map((document) => ({
    value: document.documentId,
    label: document.relativePath,
  })));
const dateFormatter = computed(() => new Intl.DateTimeFormat(ui.locale, {
  dateStyle: "medium",
  timeStyle: "short",
}));

function formatDate(value: string): string {
  return dateFormatter.value.format(new Date(value));
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

function triggerLabel(trigger: SnapshotTimelineItem["trigger"]): string {
  return t(`workspaceV2.snapshot.trigger.${trigger}`);
}

function integrityType(item: SnapshotTimelineItem): "success" | "warning" | "error" | "default" {
  if (item.integrity === "verified") return "success";
  if (item.integrity === "corrupt") return "error";
  if (item.integrity === "repairing") return "warning";
  return "default";
}

function snapshotKeydown(event: KeyboardEvent, index: number): void {
  const items = protection.snapshots;
  if (!items.length) return;
  let next = index;
  if (event.key === "ArrowDown") next = Math.min(items.length - 1, index + 1);
  else if (event.key === "ArrowUp") next = Math.max(0, index - 1);
  else if (event.key === "Home") next = 0;
  else if (event.key === "End") next = items.length - 1;
  else return;
  event.preventDefault();
  const snapshotId = items[next]?.snapshotId ?? null;
  protection.selectSnapshot(snapshotId);
  if (snapshotId) emit("action", { method: "snapshot.inspect", params: { snapshotId } });
  void nextTick(() => {
    document.querySelector<HTMLElement>(`[data-snapshot-index="${next}"]`)?.focus();
  });
}

function selectSnapshot(item: SnapshotTimelineItem): void {
  protection.selectSnapshot(item.snapshotId);
  emit("action", { method: "snapshot.inspect", params: { snapshotId: item.snapshotId } });
}

function beginImport(event: MouseEvent): void {
  importTrigger.value = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
  protection.setSnapshotPackagePlan(null);
  emit("action", {
    method: "snapshot.inspectPackage",
    params: { pathGrant: HOST_SNAPSHOT_IMPORT_GRANT, credential: null },
  });
}

function closeImport(): void {
  protection.setSnapshotPackagePlan(null);
  importCredential.value = "";
  const target = importTrigger.value;
  importTrigger.value = null;
  void nextTick(() => target?.focus({ preventScroll: true }));
}

function confirmImport(): void {
  const plan = protection.snapshotPackagePlan;
  if (!plan) return;
  emit("action", {
    method: "snapshot.import",
    params: {
      planId: plan.planId,
      credential: importCredential.value || null,
      targetMode: "newWorkspace",
      targetWorkspaceId: null,
    },
  });
}

function openExport(item: SnapshotTimelineItem): void {
  if (!canOpenExport.value) return;
  exportTarget.value = item;
  exportIdentity.value = {
    workspaceId: session.activeWorkspaceId,
    sessionEpoch: session.sessionEpoch,
  };
  exportProtection.value = "none";
  exportRecipient.value = "";
  exportCredential.value = "";
}

function closeExport(): void {
  exportTarget.value = null;
  exportIdentity.value = null;
  exportRecipient.value = "";
  exportCredential.value = "";
}

function confirmExport(): void {
  if (!canConfirmExport.value) return;
  const item = exportTarget.value;
  if (!item) return;
  const recipients = exportProtection.value === "recipient"
    ? exportRecipient.value.split(/\s+/).map((value) => value.trim()).filter(Boolean)
    : [];
  const credential = exportProtection.value === "passphrase"
    ? exportCredential.value
    : null;
  if (
    exportProtection.value === "recipient" && recipients.length === 0
    || exportProtection.value === "passphrase" && !credential
  ) return;
  emit("action", {
    method: "snapshot.export",
    params: {
      snapshotId: item.snapshotId,
      pathGrant: HOST_SNAPSHOT_EXPORT_GRANT,
      encryption: exportProtection.value === "none" ? "none" : "age",
      recipients,
      credential,
    },
  });
  closeExport();
}

function openExtract(item: SnapshotTimelineItem): void {
  extractTarget.value = item;
  extractDocumentId.value = protection.documents[0]?.documentId ?? null;
  protection.setExtractPlan(null);
}

function closeExtract(): void {
  extractTarget.value = null;
  extractDocumentId.value = null;
  protection.setExtractPlan(null);
}

function advanceExtract(): void {
  const item = extractTarget.value;
  if (!item) return;
  if (protection.extractPlan) {
    emit("action", {
      method: "snapshot.applyExtract",
      params: {
        planId: protection.extractPlan.planId,
        pathGrant: HOST_SNAPSHOT_EXTRACT_GRANT,
      },
    });
    closeExtract();
    return;
  }
  if (!extractDocumentId.value) return;
  emit("action", {
    method: "snapshot.previewExtract",
    params: {
      snapshotId: item.snapshotId,
      documentId: extractDocumentId.value,
    },
  });
}

function openRestore(
  item: SnapshotTimelineItem,
  event: MouseEvent,
): void {
  restoreTrigger.value = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
  restoreTarget.value = item;
  protection.setRestorePlan(null);
}

function openAsNewWorkspace(item: SnapshotTimelineItem): void {
  emit("action", {
    method: "snapshot.openAsNewWorkspace",
    params: { snapshotId: item.snapshotId },
  });
}

function closeRestore(): void {
  restoreTarget.value = null;
  protection.setRestorePlan(null);
  const target = restoreTrigger.value;
  restoreTrigger.value = null;
  void nextTick(() => target?.focus({ preventScroll: true }));
}

function advanceRestore(): void {
  const item = restoreTarget.value;
  if (!item) return;
  if (protection.restorePlan) {
    emit("action", {
      method: "snapshot.applyRestore",
      params: { planId: protection.restorePlan.planId, confirmed: true },
    });
    closeRestore();
    return;
  }
  emit("action", {
    method: "snapshot.previewRestore",
    params: { snapshotId: item.snapshotId, targetMode: "currentWorkspace" },
  });
}

function saveRetention(): void {
  if (!protection.retention) return;
  emit("action", {
    method: "retention.update",
    params: {
      expectedRevision: protection.retention.policyRevision,
      ...retentionDraft.value,
    },
  });
}

async function copyConvenientPassword(): Promise<void> {
  try {
    await navigator.clipboard.writeText("password");
    convenientPasswordCopied.value = true;
  } catch {
    convenientPasswordCopied.value = false;
  }
}

function previewKeyRotation(): void {
  protection.setKeyRotationPlan(null);
  emit("action", {
    method: "repository.previewKeyRotation",
    params: {},
  });
}

function closeKeyRotation(): void {
  protection.setKeyRotationPlan(null);
}

function applyKeyRotation(): void {
  const plan = protection.keyRotationPlan;
  if (!plan) return;
  emit("action", {
    method: "repository.applyKeyRotation",
    params: { planId: plan.planId, confirmed: true },
  });
}

function previewStorageRelocation(): void {
  const workspaceId = session.activeWorkspaceId;
  if (!workspaceId) return;
  protection.setStoragePlan(null);
  emit("action", {
    method: "workspace.storage.preview",
    params: {
      workspaceId,
      action: "relocate",
      targetMode: "direct",
      selectedRootGrant: HOST_WORKSPACE_ROOT_GRANT,
    },
  });
}

function previewStorageTopologyConversion(): void {
  const workspaceId = session.activeWorkspaceId;
  const currentMode = protection.storage?.mode;
  if (!workspaceId || !currentMode) return;
  protection.setStoragePlan(null);
  emit("action", {
    method: "workspace.storage.preview",
    params: {
      workspaceId,
      action: "convertTopology",
      targetMode: currentMode === "direct" ? "mirrored" : "direct",
      selectedRootGrant: HOST_WORKSPACE_ROOT_GRANT,
    },
  });
}

function previewActivityCacheRelease(): void {
  const workspaceId = session.activeWorkspaceId;
  if (!workspaceId) return;
  protection.setStoragePlan(null);
  emit("action", {
    method: "workspace.storage.preview",
    params: {
      workspaceId,
      action: "releaseActivityCache",
      targetMode: null,
      selectedRootGrant: null,
    },
  });
}

function closeStoragePlan(): void {
  protection.setStoragePlan(null);
  storageConfirmation.value = "";
}

function applyStoragePlan(): void {
  const plan = protection.storagePlan;
  if (!plan) return;
  emit("action", {
    method: "workspace.storage.apply",
    params: {
      planId: plan.planId,
      confirmation: storageConfirmation.value,
    },
  });
}

</script>

<template>
  <section v-if="mode === 'versions'" class="protection-settings" data-testid="snapshot-settings">
    <header class="protection-heading">
      <div>
        <p class="section-kicker">{{ t("workspaceV2.snapshot.kicker") }}</p>
        <h1>{{ t("workspaceV2.snapshot.title") }}</h1>
        <p>{{ t("workspaceV2.snapshot.description") }}</p>
      </div>
      <div class="heading-actions">
        <NButton
          data-testid="snapshot-import"
          :disabled="busy || !session.snapshotPackageEnabled"
          @click="beginImport"
        >
          <template #icon><NIcon><ArchiveRestore /></NIcon></template>
          {{ t("workspaceV2.snapshot.import") }}
        </NButton>
        <NButton
          quaternary
          :disabled="busy"
          :aria-label="t('workspaceV2.snapshot.refresh')"
          @click="emit('action', { method: 'snapshot.list', params: { cursor: null, limit: 50 } })"
        >
          <template #icon><NIcon><RefreshCw /></NIcon></template>
          {{ t("workspaceV2.snapshot.refresh") }}
        </NButton>
        <NButton
          type="primary"
          :disabled="busy"
          data-testid="snapshot-create"
          @click="emit('action', { method: 'snapshot.request', params: { trigger: 'manual', urgency: 'foreground' } })"
        >
          <template #icon><NIcon><Plus /></NIcon></template>
          {{ t("workspaceV2.snapshot.create") }}
        </NButton>
      </div>
    </header>

    <NAlert
      v-if="protection.operationError"
      type="error"
      :title="t('workspaceV2.operation.failed')"
      data-testid="snapshot-operation-error"
    >
      {{ protection.operationError }}
    </NAlert>

    <div class="timeline-workbench">
      <section
        class="snapshot-timeline"
        role="listbox"
        :aria-label="t('workspaceV2.snapshot.timeline')"
        :aria-activedescendant="selected ? `snapshot-${selected.snapshotId}` : undefined"
      >
        <button
          v-for="(snapshot, index) in protection.snapshots"
          :id="`snapshot-${snapshot.snapshotId}`"
          :key="snapshot.snapshotId"
          class="snapshot-row"
          :class="{ selected: snapshot.snapshotId === protection.selectedSnapshotId }"
          role="option"
          :aria-selected="snapshot.snapshotId === protection.selectedSnapshotId"
          :tabindex="snapshot.snapshotId === protection.selectedSnapshotId || (!selected && index === 0) ? 0 : -1"
          :data-snapshot-index="index"
          @click="selectSnapshot(snapshot)"
          @keydown="snapshotKeydown($event, index)"
        >
          <span class="timeline-node" :class="`integrity-${snapshot.integrity}`">
            <CheckCircle2 v-if="snapshot.integrity === 'verified'" :size="13" />
            <ShieldAlert v-else :size="13" />
          </span>
          <span class="snapshot-main">
            <span>
              <strong>{{ formatDate(snapshot.createdAt) }}</strong>
              <NTag size="small" :type="integrityType(snapshot)">
                {{ t(`workspaceV2.snapshot.integrity.${snapshot.integrity}`) }}
              </NTag>
              <Pin v-if="snapshot.pinned" :size="13" :aria-label="t('workspaceV2.snapshot.pinned')" />
            </span>
            <small>{{ triggerLabel(snapshot.trigger) }} · {{ snapshot.note ?? t("workspaceV2.snapshot.noNote") }}</small>
          </span>
          <span class="snapshot-metrics">
            <strong>{{ formatBytes(snapshot.logicalSize) }}</strong>
            <small>{{ t("workspaceV2.snapshot.physical", { size: formatBytes(snapshot.physicalSize) }) }}</small>
          </span>
          <NTag size="small" :type="snapshot.syncState === 'failed' ? 'error' : snapshot.syncState === 'replicated' ? 'success' : 'default'">
            {{ t(`workspaceV2.snapshot.sync.${snapshot.syncState}`) }}
          </NTag>
        </button>
        <div v-if="!protection.snapshots.length" class="timeline-empty">
          <ArchiveRestore :size="24" />
          <strong>{{ t("workspaceV2.snapshot.empty") }}</strong>
          <p>{{ t("workspaceV2.snapshot.emptyHint") }}</p>
        </div>
      </section>

      <aside class="snapshot-detail" :aria-label="t('workspaceV2.snapshot.detail')">
        <template v-if="selected">
          <div class="detail-title">
            <div>
              <small>SNAPSHOT</small>
              <strong>{{ formatDate(selected.createdAt) }}</strong>
            </div>
            <NButton
              quaternary
              circle
              :aria-label="selected.pinned ? t('workspaceV2.snapshot.unpin') : t('workspaceV2.snapshot.pin')"
              @click="emit('action', { method: 'snapshot.update', params: { snapshotId: selected.snapshotId, action: selected.pinned ? 'unpin' : 'pin', expectedCatalogRevision: selected.catalogRevision } })"
            >
              <template #icon><NIcon><PinOff v-if="selected.pinned" /><Pin v-else /></NIcon></template>
            </NButton>
          </div>
          <dl class="detail-facts">
            <div><dt>{{ t("workspaceV2.snapshot.trigger") }}</dt><dd>{{ triggerLabel(selected.trigger) }}</dd></div>
            <div><dt>{{ t("workspaceV2.snapshot.retention") }}</dt><dd>{{ selected.retentionReasons.join(" · ") || "—" }}</dd></div>
            <div><dt>{{ t("workspaceV2.snapshot.logical") }}</dt><dd>{{ formatBytes(selected.logicalSize) }}</dd></div>
            <div><dt>{{ t("workspaceV2.snapshot.actual") }}</dt><dd>{{ formatBytes(selected.physicalSize) }}</dd></div>
          </dl>
          <div class="detail-actions">
            <NButton
              v-if="session.capabilities.includes('snapshot.open-as-new.v2')"
              size="small"
              :disabled="busy || session.isTransitioning"
              data-testid="snapshot-open-as-new"
              @click="openAsNewWorkspace(selected)"
            >
              <template #icon><NIcon><ExternalLink /></NIcon></template>
              {{ t("workspaceV2.snapshot.openNew") }}
            </NButton>
            <NButton
              size="small"
              :disabled="!canOpenExport"
              data-testid="snapshot-export-open"
              @click="openExport(selected)"
            >
              <template #icon><NIcon><Download /></NIcon></template>
              {{ t("workspaceV2.snapshot.export") }}
            </NButton>
            <NButton
              size="small"
              data-testid="snapshot-extract-open"
              :disabled="!protection.documents.length"
              @click="openExtract(selected)"
            >
              <template #icon><NIcon><FileDown /></NIcon></template>
              {{ t("workspaceV2.snapshot.extract") }}
            </NButton>
            <NButton
              size="small"
              type="warning"
              secondary
              data-testid="snapshot-restore-open"
              @click="openRestore(selected, $event)"
            >
              <template #icon><NIcon><RotateCw /></NIcon></template>
              {{ t("workspaceV2.snapshot.restore") }}
            </NButton>
          </div>
          <p class="audit-note">
            <ShieldCheck :size="15" />
            {{ t("workspaceV2.snapshot.auditNote") }}
          </p>
        </template>
        <div v-else class="detail-empty">
          {{ t("workspaceV2.snapshot.selectHint") }}
        </div>
      </aside>
    </div>

    <NModal
      :show="restoreTarget !== null"
      preset="card"
      class="snapshot-restore-modal"
      :title="t('workspaceV2.snapshot.restoreTitle')"
      :auto-focus="true"
      :trap-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closeRestore() }"
    >
      <div class="restore-warning">
        <ShieldAlert :size="20" />
        <div>
          <strong>{{ session.activeWorkspace?.displayName }}</strong>
          <p>{{ t("workspaceV2.snapshot.restoreWarning") }}</p>
          <small>{{ t("workspaceV2.snapshot.restoreSafety") }}</small>
        </div>
      </div>
      <section v-if="protection.restorePlan" class="plan-summary" role="status">
        <div>
          <dt>{{ t("workspaceV2.plan.id") }}</dt>
          <dd><code>{{ protection.restorePlan.planId }}</code></dd>
        </div>
        <div>
          <dt>{{ t("workspaceV2.plan.changes") }}</dt>
          <dd>{{ protection.restorePlan.changes.length }}</dd>
        </div>
        <NAlert
          v-if="protection.restorePlan.protectionRequired"
          type="warning"
          :title="t('workspaceV2.snapshot.restoreSafety')"
        />
      </section>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="closeRestore">{{ t("common.cancel") }}</NButton>
          <NButton
            type="warning"
            data-testid="snapshot-restore-preview"
            :disabled="busy"
            @click="advanceRestore"
          >
            {{ protection.restorePlan ? t("workspaceV2.snapshot.restore") : t("workspaceV2.snapshot.restorePreview") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="extractTarget !== null"
      preset="card"
      class="snapshot-restore-modal"
      :title="t('workspaceV2.snapshot.extractTitle')"
      :mask-closable="false"
      @update:show="show => { if (!show) closeExtract() }"
    >
      <template v-if="!protection.extractPlan">
        <p class="modal-copy">{{ t("workspaceV2.snapshot.extractHint") }}</p>
        <NSelect
          v-model:value="extractDocumentId"
          :options="extractDocumentOptions"
          filterable
          data-testid="snapshot-extract-document"
        />
      </template>
      <NAlert
        v-else
        type="info"
        :title="protection.extractPlan.displayName"
      >
        {{ t("workspaceV2.snapshot.extractPlan", {
          size: formatBytes(protection.extractPlan.size),
          expires: formatDate(protection.extractPlan.expiresAt),
        }) }}
      </NAlert>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="closeExtract">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            data-testid="snapshot-extract-advance"
            :disabled="busy || (!protection.extractPlan && !extractDocumentId)"
            @click="advanceExtract"
          >
            {{ protection.extractPlan
              ? t("workspaceV2.snapshot.extractSave")
              : t("workspaceV2.snapshot.extractPreview") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="exportTarget !== null"
      preset="card"
      class="snapshot-restore-modal"
      data-testid="snapshot-export-modal"
      :title="t('workspaceV2.snapshot.exportTitle')"
      :mask-closable="false"
      @update:show="show => { if (!show) closeExport() }"
    >
      <div class="export-modes" role="radiogroup" :aria-label="t('workspaceV2.snapshot.exportProtection')">
        <NButton
          v-for="option in (['none', 'recipient', 'passphrase'] as const)"
          :key="option"
          size="small"
          :type="exportProtection === option ? 'primary' : 'default'"
          :secondary="exportProtection === option"
          role="radio"
          :aria-checked="exportProtection === option"
          @click="exportProtection = option"
        >
          {{ t(`workspaceV2.snapshot.exportMode.${option}`) }}
        </NButton>
      </div>
      <NAlert
        :type="exportProtection === 'none' ? 'warning' : 'info'"
        :title="t(exportProtection === 'none' ? 'workspaceV2.snapshot.exportUnencrypted' : 'workspaceV2.snapshot.exportEncrypted')"
      />
      <NInput
        v-if="exportProtection === 'recipient'"
        v-model:value="exportRecipient"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 4 }"
        :placeholder="t('workspaceV2.snapshot.ageRecipient')"
        data-testid="snapshot-export-recipient"
      />
      <NInput
        v-else-if="exportProtection === 'passphrase'"
        v-model:value="exportCredential"
        type="password"
        show-password-on="click"
        :placeholder="t('workspaceV2.snapshot.agePassphrase')"
        data-testid="snapshot-export-passphrase"
      />
      <template #footer>
        <div class="modal-actions">
          <NButton @click="closeExport">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            :disabled="!canConfirmExport"
            data-testid="snapshot-export-apply"
            @click="confirmExport"
          >
            {{ t("workspaceV2.snapshot.export") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="protection.snapshotPackagePlan !== null"
      preset="card"
      class="snapshot-restore-modal"
      :title="t('workspaceV2.snapshot.importTitle')"
      :auto-focus="true"
      :trap-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closeImport() }"
    >
      <template v-if="protection.snapshotPackagePlan">
        <NAlert
          :type="protection.snapshotPackagePlan.trusted ? 'success' : 'warning'"
          :title="protection.snapshotPackagePlan.trusted ? t('workspaceV2.snapshot.packageTrusted') : t('workspaceV2.snapshot.packageUntrusted')"
        >
          {{ t("workspaceV2.snapshot.packageCount", { count: protection.snapshotPackagePlan.snapshotCount }) }}
        </NAlert>
        <dl class="plan-summary">
          <div>
            <dt>{{ t("workspaceV2.snapshot.packageWorkspace") }}</dt>
            <dd><code>{{ protection.snapshotPackagePlan.workspaceId }}</code></dd>
          </div>
        </dl>
        <NInput
          v-if="protection.snapshotPackagePlan.encrypted && !protection.snapshotPackagePlan.verified"
          v-model:value="importCredential"
          type="password"
          show-password-on="click"
          :placeholder="t('workspaceV2.snapshot.packageCredential')"
          data-testid="snapshot-import-credential"
        />
      </template>
      <template #footer>
        <div class="modal-actions">
          <NButton :disabled="busy" @click="closeImport">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            data-testid="snapshot-import-apply"
            :disabled="busy || Boolean(protection.snapshotPackagePlan?.encrypted && !protection.snapshotPackagePlan.verified && !importCredential)"
            @click="confirmImport"
          >
            {{ t("workspaceV2.snapshot.importApply") }}
          </NButton>
        </div>
      </template>
    </NModal>
  </section>

  <section v-else class="protection-settings" data-testid="storage-settings">
    <header class="protection-heading">
      <div>
        <p class="section-kicker">{{ t("workspaceV2.storage.kicker") }}</p>
        <h1>{{ t("workspaceV2.storage.title") }}</h1>
        <p>{{ t("workspaceV2.storage.description") }}</p>
      </div>
      <NTag v-if="protection.storage" :type="protection.storage.health === 'healthy' ? 'success' : 'warning'">
        {{ t(`workspaceV2.storage.health.${protection.storage.health}`) }}
      </NTag>
    </header>

    <section class="storage-overview">
      <div class="storage-path">
        <span><HardDrive :size="19" /></span>
        <div>
          <small>{{ t("workspaceV2.storage.location") }}</small>
          <code>{{ protection.storage?.location ?? "—" }}</code>
          <small>{{ t("workspaceV2.storage.activity") }} · {{ protection.storage?.activityRoot ?? "—" }}</small>
        </div>
      </div>
      <div class="size-meter" :aria-label="t('workspaceV2.storage.usage')">
        <div><span>{{ t("workspaceV2.snapshot.logical") }}</span><strong>{{ formatBytes(protection.storage?.logicalSize ?? 0) }}</strong></div>
        <div><span>{{ t("workspaceV2.snapshot.actual") }}</span><strong>{{ formatBytes(protection.storage?.physicalSize ?? 0) }}</strong></div>
        <div><span>{{ t("workspaceV2.storage.reclaimable") }}</span><strong>{{ formatBytes(protection.storage?.reclaimableSize ?? 0) }}</strong></div>
      </div>
    </section>

    <section class="policy-card">
      <div class="policy-heading">
        <div><strong>{{ t("workspaceV2.storage.mode") }}</strong><small>{{ t("workspaceV2.storage.modeHint") }}</small></div>
        <NTag>{{ protection.storage?.mode === "mirrored" ? t("workspaceV2.storage.mirrored") : t("workspaceV2.storage.direct") }}</NTag>
      </div>
      <div
        v-if="session.capabilities.includes('workspace.storage.relocate.v2')
          || session.capabilities.includes('workspace.storage.topology.v2')
          || session.capabilities.includes('workspace.storage.release-cache.v2')"
        class="storage-mode-actions"
      >
        <NButton
          v-if="session.capabilities.includes('workspace.storage.relocate.v2')
            && protection.storage?.mode === 'direct'
            && protection.storage?.provider === 'fixed'"
          size="small"
          :disabled="busy || session.isTransitioning"
          data-testid="workspace-storage-relocate-preview"
          @click="previewStorageRelocation"
        >
          {{ t("workspaceV2.storage.relocate") }}
        </NButton>
        <NButton
          v-if="session.capabilities.includes('workspace.storage.topology.v2')"
          size="small"
          :disabled="busy || session.isTransitioning"
          data-testid="workspace-storage-convert-preview"
          @click="previewStorageTopologyConversion"
        >
          {{ protection.storage?.mode === "mirrored"
            ? t("workspaceV2.storage.convertToDirect")
            : t("workspaceV2.storage.convertToMirrored") }}
        </NButton>
        <NButton
          v-if="session.capabilities.includes('workspace.storage.release-cache.v2')
            && protection.storage?.mode === 'mirrored'"
          size="small"
          :disabled="busy || session.isTransitioning
            || protection.storage?.pendingSync || !protection.storage?.replicaVerified"
          :title="t('workspaceV2.storage.releaseBlocked')"
          data-testid="workspace-storage-release-cache-preview"
          @click="previewActivityCacheRelease"
        >
          {{ t("workspaceV2.storage.releaseCache") }}
        </NButton>
      </div>
      <div class="policy-heading">
        <div><strong>{{ t("workspaceV2.storage.encryption") }}</strong><small>{{ t("workspaceV2.storage.encryptionHint") }}</small></div>
        <NTag>{{ t(`workspaceV2.storage.encryption.${protection.storage?.encryption ?? "none"}`) }}</NTag>
      </div>
      <div v-if="protection.storage" class="key-card">
        <ShieldCheck :size="20" />
        <div>
          <strong>{{ t(`workspaceV2.storage.encryptionNotice.${protection.storage.encryption}`) }}</strong>
          <small>{{ t(`workspaceV2.storage.encryptionDetail.${protection.storage.encryption}`) }}</small>
        </div>
        <NButton
          v-if="protection.storage.encryption === 'convenient'"
          size="small"
          data-testid="storage-copy-convenient-password"
          @click="copyConvenientPassword"
        >
          <template #icon><NIcon><Copy /></NIcon></template>
          {{ convenientPasswordCopied ? t("common.copied") : t("common.copy") }}
        </NButton>
        <NButton
          v-else-if="protection.storage.encryption === 'protected'
            && session.capabilities.includes('repository.key-rotation.v2')"
          size="small"
          :disabled="busy || session.isTransitioning || protection.storage.pendingSync"
          data-testid="repository-key-rotation-preview"
          @click="previewKeyRotation"
        >
          <template #icon><NIcon><RefreshCw /></NIcon></template>
          {{ t("workspaceV2.key.preview") }}
        </NButton>
      </div>
      <footer>
        <NButton
          size="small"
          :disabled="busy || !protection.storage || !session.snapshotEnabled"
          data-testid="repository-verify"
          @click="emit('action', { method: 'repository.verify', params: {} })"
        >
          <template #icon><NIcon><ShieldCheck /></NIcon></template>
          {{ t("workspaceV2.storage.verify") }}
        </NButton>
      </footer>
      <NAlert
        v-if="protection.repositoryVerification"
        :type="protection.repositoryVerification.state === 'verified' ? 'success' : 'error'"
        :title="t(`workspaceV2.storage.verify.${protection.repositoryVerification.state}`)"
      >
        {{ t("workspaceV2.storage.verifySummary", {
          snapshots: protection.repositoryVerification.snapshotCount,
          objects: protection.repositoryVerification.objectCount,
          corrupt: protection.repositoryVerification.corruptSnapshotIds.length,
        }) }}
      </NAlert>
    </section>

    <section class="policy-card retention-card">
      <div class="policy-title">
        <div><strong>{{ t("workspaceV2.retention.title") }}</strong><small>{{ t("workspaceV2.retention.hint") }}</small></div>
        <NTag size="small">{{ t("workspaceV2.retention.revision", { revision: protection.retention?.policyRevision ?? "—" }) }}</NTag>
      </div>
      <NAlert
        v-if="!protection.retentionHydrated"
        type="info"
        :title="t('workspaceV2.retention.loading')"
      />
      <NAlert
        v-if="protection.retentionStatus?.automaticSnapshotsPaused"
        type="warning"
        data-testid="retention-automatic-paused"
        :title="t('workspaceV2.retention.automaticPaused')"
      >
        {{ t("workspaceV2.retention.automaticPausedDetail", {
          usage: formatBytes(protection.retentionStatus.repositoryUsageBytes),
          limit: protection.retentionStatus.repositoryLimitBytes === null
            ? t("workspaceV2.retention.unlimited")
            : formatBytes(protection.retentionStatus.repositoryLimitBytes),
        }) }}
      </NAlert>
      <NAlert
        v-if="protection.retentionStatus?.integrityStatus === 'corrupt'"
        type="error"
        data-testid="retention-integrity-corrupt"
        :title="t('workspaceV2.retention.integrityCorrupt')"
      >
        {{ t("workspaceV2.retention.integrityCorruptDetail") }}
      </NAlert>
      <NAlert
        v-else-if="protection.retentionStatus"
        :type="protection.retentionStatus.integrityStatus === 'verified' ? 'success' : 'info'"
        data-testid="retention-integrity-status"
        :title="t(`workspaceV2.retention.integrity.${protection.retentionStatus.integrityStatus}`)"
      >
        {{ t("workspaceV2.retention.integrityChecks", {
          incremental: protection.retentionStatus.lastIncrementalCheckAt
            ? formatDate(protection.retentionStatus.lastIncrementalCheckAt)
            : "—",
          full: protection.retentionStatus.lastFullCheckAt
            ? formatDate(protection.retentionStatus.lastFullCheckAt)
            : "—",
        }) }}
      </NAlert>
      <div class="retention-grid">
        <label><span>{{ t("workspaceV2.retention.snapshotDays") }}</span><NInputNumber v-model:value="retentionDraft.snapshotDays" :min="1" :disabled="!protection.retentionHydrated" /></label>
        <label><span>{{ t("workspaceV2.retention.snapshotCount") }}</span><NInputNumber v-model:value="retentionDraft.snapshotCount" :min="1" :disabled="!protection.retentionHydrated" /></label>
        <label class="bucket-field"><span>{{ t("workspaceV2.retention.snapshotBuckets") }}</span><NSelect v-model:value="retentionDraft.snapshotBuckets" multiple :options="retentionBucketOptions" :disabled="!protection.retentionHydrated" /></label>
        <label><span>{{ t("workspaceV2.retention.fileDays") }}</span><NInputNumber v-model:value="retentionDraft.fileRevisionDays" :min="1" :disabled="!protection.retentionHydrated" /></label>
        <label><span>{{ t("workspaceV2.retention.fileCount") }}</span><NInputNumber v-model:value="retentionDraft.fileRevisionCount" :min="1" :disabled="!protection.retentionHydrated" /></label>
        <label class="bucket-field"><span>{{ t("workspaceV2.retention.fileBuckets") }}</span><NSelect v-model:value="retentionDraft.fileRevisionBuckets" multiple :options="retentionBucketOptions" :disabled="!protection.retentionHydrated" /></label>
        <label>
          <span>{{ t("workspaceV2.retention.repositoryLimitGiB") }}</span>
          <NInputNumber
            v-model:value="repositoryLimitGiB"
            :min="1"
            :precision="1"
            clearable
            :placeholder="t('workspaceV2.retention.unlimited')"
            :disabled="!protection.retentionHydrated"
            data-testid="retention-repository-limit"
          />
        </label>
      </div>
      <NAlert
        v-if="repositoryLimitGiB !== null"
        type="info"
        :title="t('workspaceV2.retention.limitActive', { size: `${repositoryLimitGiB.toFixed(1)} GiB` })"
      >
        {{ t("workspaceV2.retention.limitBehavior") }}
      </NAlert>
      <NAlert
        v-if="protection.retentionPlan"
        :type="protection.retentionPlan.blockedReasons.length ? 'warning' : 'info'"
        :title="t('workspaceV2.retention.cleanupPlan', { size: formatBytes(protection.retentionPlan.reclaimableBytes) })"
      >
        {{ protection.retentionPlan.blockedReasons.join(" · ") || t("workspaceV2.retention.cleanupReady") }}
      </NAlert>
      <footer>
        <NButton
          size="small"
          :disabled="!protection.retentionHydrated"
          data-testid="retention-plan-preview"
          @click="emit('action', { method: 'retention.plan', params: {} })"
        >
          {{ t("workspaceV2.retention.cleanupPreview") }}
        </NButton>
        <NButton
          v-if="protection.retentionPlan"
          size="small"
          type="warning"
          data-testid="retention-plan-apply"
          :disabled="!protection.retentionHydrated"
          @click="emit('action', { method: 'retention.apply', params: { planId: protection.retentionPlan.planId } })"
        >
          {{ t("workspaceV2.retention.cleanupApply") }}
        </NButton>
        <NButton
          size="small"
          type="primary"
          :disabled="!retentionDirty"
          data-testid="retention-save"
          @click="saveRetention"
        >
          {{ t("common.save") }}
        </NButton>
      </footer>
    </section>

    <NModal
      :show="protection.storagePlan !== null"
      preset="card"
      class="storage-plan-modal"
      :title="t('workspaceV2.storage.planTitle')"
      :auto-focus="true"
      :trap-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closeStoragePlan() }"
    >
      <template v-if="protection.storagePlan">
        <p class="storage-plan-route">
          <code>{{ protection.storagePlan.source.selectedRoot }}</code>
          <span aria-hidden="true">→</span>
          <code>{{ protection.storagePlan.target.selectedRoot }}</code>
        </p>
        <NAlert type="warning" :title="t('workspaceV2.storage.previewOnly')">
          {{ t("workspaceV2.storage.planApplyHint", {
            size: formatBytes(protection.storagePlan.bytesToCopy),
          }) }}
        </NAlert>
        <ul v-if="protection.storagePlan.warnings.length" class="plan-warnings">
          <li v-for="warning in protection.storagePlan.warnings" :key="warning">
            {{ warning }}
          </li>
        </ul>
        <label class="confirmation-field">
          <span>{{ t("workspaceV2.storage.confirmName", {
            name: session.activeWorkspace?.displayName ?? "",
          }) }}</span>
          <NInput
            v-model:value="storageConfirmation"
            data-testid="workspace-storage-confirmation"
            :placeholder="session.activeWorkspace?.displayName ?? ''"
          />
        </label>
      </template>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="closeStoragePlan">{{ t("common.cancel") }}</NButton>
          <NButton
            type="warning"
            :disabled="storageConfirmation !== session.activeWorkspace?.displayName"
            data-testid="workspace-storage-relocate-apply"
            @click="applyStoragePlan"
          >
            {{ t("workspaceV2.storage.apply") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="protection.keyRotationPlan !== null"
      preset="card"
      class="storage-plan-modal"
      :title="t('workspaceV2.key.planTitle')"
      :auto-focus="true"
      :trap-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closeKeyRotation() }"
    >
      <NAlert type="warning" :title="t('workspaceV2.key.planWarning')">
        {{ t("workspaceV2.key.planHint") }}
      </NAlert>
      <p v-if="protection.keyRotationPlan" class="plan-expiry">
        {{ t("workspaceV2.key.planExpiry", {
          time: formatDate(protection.keyRotationPlan.expiresAt),
        }) }}
      </p>
      <template #footer>
        <div class="modal-actions">
          <NButton @click="closeKeyRotation">{{ t("common.cancel") }}</NButton>
          <NButton
            type="warning"
            data-testid="repository-key-rotation-apply"
            @click="applyKeyRotation"
          >
            {{ t("workspaceV2.key.apply") }}
          </NButton>
        </div>
      </template>
    </NModal>
  </section>
</template>

<style scoped>
.protection-settings { display: grid; gap: 16px; }
.protection-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; }
.protection-heading h1 { margin: 0; font-size: var(--vt-font-heading); font-weight: 650; letter-spacing: -.02em; }
.protection-heading p:not(.section-kicker) { max-width: 660px; margin: 6px 0 0; color: var(--vt-fg-muted); line-height: 1.55; }
.section-kicker { margin: 0 0 4px; color: var(--vt-color-primary-500); font-size: 10px; font-weight: 700; letter-spacing: .14em; }
.heading-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 7px; }
.timeline-workbench {
  display: grid;
  grid-template-columns: minmax(390px, 1.5fr) minmax(260px, .85fr);
  min-height: 430px;
  overflow: hidden;
  border: 1px solid var(--vt-border);
  border-radius: 12px;
  background: var(--vt-bg);
  box-shadow: var(--vt-shadow-1);
}
.snapshot-timeline { min-width: 0; overflow: auto; border-right: 1px solid var(--vt-border); }
.snapshot-row {
  display: grid;
  grid-template-columns: 26px minmax(140px, 1fr) auto auto;
  align-items: center;
  gap: 10px;
  width: 100%;
  min-height: 74px;
  padding: 10px 14px;
  color: inherit;
  border: 0;
  border-bottom: 1px solid var(--vt-border);
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.snapshot-row:hover { background: var(--vt-bg-subtle); }
.snapshot-row.selected { background: var(--vt-color-primary-50); box-shadow: inset 3px 0 var(--vt-color-primary-500); }
:root.dark .snapshot-row.selected { background: rgba(91, 139, 255, .12); }
.timeline-node {
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  color: var(--vt-fg-muted);
  border: 1px solid var(--vt-border);
  border-radius: 50%;
  background: var(--vt-bg);
}
.timeline-node.integrity-verified { color: var(--vt-color-success-600); border-color: var(--vt-color-success-500); }
.timeline-node.integrity-corrupt { color: var(--vt-color-danger-600); border-color: var(--vt-color-danger-500); }
.snapshot-main,
.snapshot-metrics { display: flex; min-width: 0; flex-direction: column; }
.snapshot-main > span { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.snapshot-main strong { font-size: var(--vt-font-body); font-weight: 570; }
.snapshot-main small,
.snapshot-metrics small { overflow: hidden; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; }
.snapshot-metrics { align-items: flex-end; }
.snapshot-metrics strong { font-size: var(--vt-font-caption); font-weight: 600; }
.timeline-empty,
.detail-empty { display: flex; min-height: 320px; align-items: center; justify-content: center; flex-direction: column; padding: 32px; color: var(--vt-fg-muted); text-align: center; }
.timeline-empty strong { margin-top: 9px; color: var(--vt-fg-secondary); }
.timeline-empty p { max-width: 320px; margin: 4px 0; }
.snapshot-detail { padding: 18px; overflow: auto; background: var(--vt-bg-subtle); }
.detail-title { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.detail-title > div { display: flex; min-width: 0; flex-direction: column; }
.detail-title small { color: var(--vt-color-primary-500); font-size: 9px; font-weight: 700; letter-spacing: .16em; }
.detail-title strong { margin-top: 3px; font-size: var(--vt-font-label); }
.detail-facts { margin: 18px 0; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.detail-facts > div,
.plan-summary > div { display: flex; justify-content: space-between; gap: 12px; padding: 9px 11px; border-bottom: 1px solid var(--vt-border); }
.detail-facts > div:last-child,
.plan-summary > div:last-child { border-bottom: 0; }
.detail-facts dt,
.plan-summary dt { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.detail-facts dd,
.plan-summary dd { margin: 0; color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); font-weight: 550; text-align: right; }
.detail-actions { display: grid; grid-template-columns: 1fr 1fr; gap: 7px; }
.audit-note { display: flex; align-items: flex-start; gap: 7px; margin: 16px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.5; }
.audit-note svg { flex: none; margin-top: 2px; color: var(--vt-color-primary-500); }
.restore-warning { display: flex; align-items: flex-start; gap: 12px; }
.restore-warning > svg { flex: none; color: var(--vt-color-warning); }
.restore-warning p { margin: 4px 0; }
.restore-warning small { color: var(--vt-fg-muted); }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; }
.export-modes { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.export-modes + :deep(.n-alert) { margin-bottom: 12px; }
:global(.snapshot-restore-modal),
:global(.storage-plan-modal) { width: min(520px, calc(100vw - 28px)); }
.plan-expiry { margin: 12px 0 0; color: var(--vt-fg-muted); font-size: 12px; }
.storage-plan-route {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  gap: 9px;
  margin: 0 0 12px;
}
.storage-plan-route code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.plan-warnings { margin: 12px 0; padding-left: 20px; color: var(--vt-fg-muted); }
.confirmation-field { display: grid; gap: 6px; margin-top: 14px; }
.confirmation-field > span { color: var(--vt-fg-muted); font-size: 12px; }
.storage-overview {
  display: grid;
  grid-template-columns: minmax(300px, 1fr) minmax(280px, .8fr);
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--vt-border);
  border-radius: 12px;
  background: var(--vt-bg);
  box-shadow: var(--vt-shadow-1);
}
.storage-path { display: flex; align-items: flex-start; gap: 11px; min-width: 0; }
.storage-path > span,
.key-icon { display: grid; flex: none; place-items: center; width: 36px; height: 36px; color: var(--vt-color-primary-500); border-radius: 10px; background: var(--vt-color-primary-50); }
.storage-path > div { display: flex; min-width: 0; flex-direction: column; gap: 3px; }
.storage-path small,
.policy-heading small,
.policy-title small,
.key-card small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.storage-path code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.size-meter { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; }
.size-meter > div { display: flex; flex-direction: column; padding: 9px; border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.size-meter span { color: var(--vt-fg-muted); font-size: 10px; }
.size-meter strong { margin-top: 3px; font-size: var(--vt-font-caption); }
.policy-card { border: 1px solid var(--vt-border); border-radius: 12px; background: var(--vt-bg); }
.policy-heading { display: flex; align-items: center; justify-content: space-between; gap: 18px; padding: 14px 16px; border-bottom: 1px solid var(--vt-border); }
.storage-mode-actions { display: flex; flex-wrap: wrap; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--vt-border); }
.policy-heading > div,
.policy-title > div,
.key-card > div:nth-child(2) { display: flex; min-width: 0; flex-direction: column; }
.policy-control { width: min(240px, 42%); }
.policy-card footer { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 16px; }
.policy-title { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 14px 16px 6px; }
.retention-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; padding: 10px 16px 14px; }
.retention-grid label { display: flex; min-width: 0; flex-direction: column; gap: 5px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.retention-grid .bucket-field { grid-column: span 2; }
.key-card { display: grid; grid-template-columns: 40px 1fr auto; align-items: center; gap: 10px; padding: 14px 16px; }
.plan-summary { margin: 14px 0; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
@media (max-width: 960px) {
  .timeline-workbench { grid-template-columns: 1fr; }
  .snapshot-timeline { border-right: 0; border-bottom: 1px solid var(--vt-border); }
  .snapshot-detail { min-height: 260px; }
  .storage-overview { grid-template-columns: 1fr; }
  .retention-grid { grid-template-columns: 1fr 1fr; }
  .retention-grid .bucket-field { grid-column: span 1; }
}
@media (max-width: 620px) {
  .protection-heading { flex-direction: column; }
  .heading-actions { justify-content: flex-start; }
  .snapshot-row { grid-template-columns: 24px minmax(120px, 1fr) auto; }
  .snapshot-row > :last-child { grid-column: 2 / -1; justify-self: start; }
  .detail-actions,
  .retention-grid { grid-template-columns: 1fr; }
  .policy-heading { align-items: stretch; flex-direction: column; }
  .policy-control { width: 100%; }
  .key-card { grid-template-columns: 40px 1fr; }
  .key-card > :last-child { grid-column: 1 / -1; }
}
@media (prefers-reduced-motion: reduce) {
  .snapshot-row { scroll-behavior: auto; }
}
</style>
