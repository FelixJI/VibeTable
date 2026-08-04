<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCheckbox,
  NIcon,
  NInput,
  NModal,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NTag,
} from "naive-ui";
import {
  AlertTriangle,
  ArchiveRestore,
  ArrowRight,
  CloudOff,
  Copy,
  FolderInput,
  FolderPlus,
  HardDrive,
  ListRestart,
  LogOut,
  Trash2,
  ShieldCheck,
} from "lucide-vue-next";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import type { WorkspaceRegistryEntryV2 } from "@/contracts/workspaceV2";
import type { WorkspaceV2UiAction } from "@/contracts/workspaceV2Bridge";
import {
  HOST_SNAPSHOT_IMPORT_GRANT,
  HOST_WORKSPACE_ROOT_GRANT,
} from "@/services/workspaceV2HostAdapter";
import { t } from "@/i18n";

const emit = defineEmits<{
  action: [action: WorkspaceV2UiAction];
  open: [workspace: WorkspaceRegistryEntryV2];
}>();

const ui = useUiStore();
const session = useWorkspaceSessionStore();
const protection = useWorkspaceProtectionStore();
const mirroredCreationEnabled = computed(() =>
  session.capabilities.includes("workspace.storage.mirrored-create.v2"));
const flow = ref<"create" | "connect" | null>(null);
const flowTrigger = ref<HTMLElement | null>(null);
const displayName = ref("");
const locationPolicy = ref<"managedDefault" | "other">("managedDefault");
const storageMode = ref<"direct" | "mirrored">("direct");
const userMarkedSync = ref(false);
const mirroredCreationAvailable = computed(() =>
  locationPolicy.value === "other" && mirroredCreationEnabled.value);
const encryptionMode = ref<"none" | "convenient" | "protected">("convenient");
const convenientPasswordCopied = ref(false);
const workspaceFlowModalStyle = { maxHeight: "calc(100dvh - 32px)" } as const;
const workspaceFlowContentStyle = { minHeight: "0", overflowY: "auto" } as const;
const deleteWorkspaceId = ref<string | null>(null);
const deleteTrigger = ref<HTMLElement | null>(null);
const deleteConfirmation = ref("");
const importTrigger = ref<HTMLElement | null>(null);
const importCredential = ref("");
const deletePlan = computed(() =>
  session.deletePlan?.workspaceId === deleteWorkspaceId.value ? session.deletePlan : null);
const dateFormatter = computed(() => new Intl.DateTimeFormat(ui.locale, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
}));

function formatDate(value: string | null): string {
  return value ? dateFormatter.value.format(new Date(value)) : t("workspaceV2.center.noRecord");
}

function healthLabel(workspace: WorkspaceRegistryEntryV2): string {
  return {
    healthy: t("workspaceV2.center.health.healthy"),
    offline: t("workspaceV2.center.health.offline"),
    degraded: t("workspaceV2.center.health.degraded"),
    corrupt: t("workspaceV2.center.health.corrupt"),
    unknown: t("workspaceV2.center.health.unknown"),
  }[workspace.lastKnownHealth];
}

function healthType(workspace: WorkspaceRegistryEntryV2): "success" | "warning" | "error" | "default" {
  if (workspace.lastKnownHealth === "healthy") return "success";
  if (workspace.lastKnownHealth === "corrupt") return "error";
  if (["offline", "degraded"].includes(workspace.lastKnownHealth)) return "warning";
  return "default";
}

function restoreFocus(target: HTMLElement | null): void {
  void nextTick(() => target?.focus({ preventScroll: true }));
}

function openFlow(kind: "create" | "connect", event?: MouseEvent): void {
  flowTrigger.value = event?.currentTarget instanceof HTMLElement ? event.currentTarget : null;
  flow.value = kind;
  if (kind === "create") {
    displayName.value = "";
    locationPolicy.value = "managedDefault";
    storageMode.value = "direct";
    userMarkedSync.value = false;
    encryptionMode.value = "convenient";
  }
}

function closeFlow(): void {
  flow.value = null;
  const target = flowTrigger.value;
  flowTrigger.value = null;
  restoreFocus(target);
}

async function copyConvenientPassword(): Promise<void> {
  try {
    await navigator.clipboard.writeText("password");
    convenientPasswordCopied.value = true;
  } catch {
    convenientPasswordCopied.value = false;
  }
}

function confirmFlow(): void {
  if (flow.value === "create") {
    const name = displayName.value.trim();
    if (!name) return;
    const topology = locationPolicy.value === "managedDefault"
      ? {
          locationPolicy: "managedDefault" as const,
          selectedRootGrant: null,
          storageMode: "direct" as const,
          userMarkedSync: false as const,
        }
      : {
          locationPolicy: "other" as const,
          selectedRootGrant: HOST_WORKSPACE_ROOT_GRANT,
          storageMode: storageMode.value,
          userMarkedSync: userMarkedSync.value,
        };
    emit("action", {
      method: "workspace.create",
      params: {
        displayName: name,
        encryptionMode: encryptionMode.value,
        ...topology,
      },
    });
  } else if (flow.value === "connect") {
    emit("action", {
      method: "workspace.register",
      params: { selectedRootGrant: HOST_WORKSPACE_ROOT_GRANT },
    });
  }
  closeFlow();
}

function planDelete(workspace: WorkspaceRegistryEntryV2, event: MouseEvent): void {
  deleteWorkspaceId.value = workspace.workspaceId;
  deleteTrigger.value = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
  deleteConfirmation.value = "";
  session.setDeletePlan(null);
  emit("action", {
    method: "workspace.planDelete",
    params: { workspaceId: workspace.workspaceId },
  });
}

function closeDelete(): void {
  deleteWorkspaceId.value = null;
  deleteConfirmation.value = "";
  session.setDeletePlan(null);
  const target = deleteTrigger.value;
  deleteTrigger.value = null;
  restoreFocus(target);
}

function applyDelete(): void {
  const plan = deletePlan.value;
  if (!plan) return;
  emit("action", {
    method: "workspace.applyDelete",
    params: { planId: plan.planId, confirmation: deleteConfirmation.value },
  });
  closeDelete();
}

function beginPackageImport(event: MouseEvent): void {
  importTrigger.value = event.currentTarget instanceof HTMLElement
    ? event.currentTarget
    : null;
  importCredential.value = "";
  protection.setSnapshotPackagePlan(null);
  emit("action", {
    method: "snapshot.inspectPackage",
    params: {
      pathGrant: HOST_SNAPSHOT_IMPORT_GRANT,
      credential: null,
    },
  });
}

function closePackageImport(): void {
  protection.setSnapshotPackagePlan(null);
  importCredential.value = "";
  const target = importTrigger.value;
  importTrigger.value = null;
  restoreFocus(target);
}

function applyPackageImport(): void {
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

watch(deletePlan, (plan) => {
  if (plan) deleteConfirmation.value = "";
});
watch(locationPolicy, (policy) => {
  if (policy === "managedDefault") {
    storageMode.value = "direct";
    userMarkedSync.value = false;
  }
});
watch(userMarkedSync, (marked) => {
  if (marked) storageMode.value = "mirrored";
});
watch(
  () => protection.snapshotPackagePlan,
  (next, previous) => {
    if (previous && !next) {
      importCredential.value = "";
      const target = importTrigger.value;
      importTrigger.value = null;
      restoreFocus(target);
    }
  },
);
</script>

<template>
  <main class="workspace-center" data-testid="workspace-center">
    <NAlert
      v-if="protection.operationError"
      type="error"
      :title="t('workspaceV2.operation.failed')"
      data-testid="workspace-operation-error"
    >
      {{ protection.operationError }}
    </NAlert>
    <header class="center-hero">
      <div class="hero-mark" aria-hidden="true">
        <HardDrive :size="21" />
      </div>
      <div>
        <p class="eyebrow">{{ t("workspaceV2.center.kicker") }}</p>
        <h1>{{ t("workspaceV2.center.title") }}</h1>
        <p>{{ t("workspaceV2.center.description") }}</p>
      </div>
      <div class="hero-actions">
        <NButton
          quaternary
          :aria-label="t('workspaceV2.center.refresh')"
          @click="emit('action', { method: 'workspace.list', params: {} })"
        >
          <template #icon><NIcon><ListRestart /></NIcon></template>
          {{ t("workspaceV2.center.refresh") }}
        </NButton>
        <NButton
          v-if="session.hasOpenWorkspace"
          quaternary
          @click="emit('action', { method: 'workspace.close', params: { reason: 'user' } })"
        >
          <template #icon><NIcon><LogOut /></NIcon></template>
          {{ t("workspaceV2.center.close") }}
        </NButton>
        <NButton data-testid="workspace-connect" @click="openFlow('connect', $event)">
          <template #icon><NIcon><FolderInput /></NIcon></template>
          {{ t("workspaceV2.center.connect") }}
        </NButton>
        <NButton
          v-if="session.snapshotPackageEnabled"
          data-testid="workspace-import-package"
          @click="beginPackageImport($event)"
        >
          <template #icon><NIcon><ArchiveRestore /></NIcon></template>
          {{ t("workspaceV2.center.import") }}
        </NButton>
        <NButton type="primary" data-testid="workspace-create" @click="openFlow('create', $event)">
          <template #icon><NIcon><FolderPlus /></NIcon></template>
          {{ t("workspaceV2.center.create") }}
        </NButton>
      </div>
    </header>

    <section v-if="session.workspaces.length" class="workspace-catalog" :aria-label="t('workspaceV2.center.list')">
      <article
        v-for="workspace in session.workspaces"
        :key="workspace.workspaceId"
        class="workspace-card"
        :class="`health-${workspace.lastKnownHealth}`"
      >
        <div class="card-leading">
          <span class="workspace-monogram" aria-hidden="true">
            {{ workspace.displayName.slice(0, 1).toLocaleUpperCase() }}
          </span>
          <div class="workspace-copy">
            <div class="workspace-name-row">
              <h2>{{ workspace.displayName }}</h2>
              <NTag size="small" :type="healthType(workspace)">
                {{ healthLabel(workspace) }}
              </NTag>
              <NTag v-if="workspace.pendingSync" size="small" type="warning">
                {{ t("workspaceV2.center.pendingSync") }}
              </NTag>
            </div>
            <code :title="workspace.selectedRoot">{{ workspace.selectedRoot }}</code>
          </div>
        </div>

        <dl>
          <div>
            <dt>{{ t("workspaceV2.center.lastSnapshot") }}</dt>
            <dd>{{ formatDate(workspace.lastSnapshotAt) }}</dd>
          </div>
          <div>
            <dt>{{ t("workspaceV2.center.lastSync") }}</dt>
            <dd>{{ formatDate(workspace.lastSyncAt) }}</dd>
          </div>
          <div>
            <dt>{{ t("workspaceV2.center.coordination") }}</dt>
            <dd>{{ workspace.coordinationStrength === "strong" ? t("workspaceV2.center.strong") : t("workspaceV2.center.advisory") }}</dd>
          </div>
        </dl>

        <footer>
          <div class="health-note">
            <ShieldCheck v-if="workspace.lastKnownHealth === 'healthy'" :size="15" />
            <CloudOff v-else-if="workspace.lastKnownHealth === 'offline'" :size="15" />
            <AlertTriangle v-else :size="15" />
            <span>
              {{ workspace.storageKind === "fixed" ? t("workspaceV2.center.fixed") : t("workspaceV2.center.portable") }}
            </span>
          </div>
          <div class="card-actions">
            <NButton
              v-if="workspace.lastKnownHealth !== 'healthy'"
              size="small"
              quaternary
              :title="t('workspaceV2.center.relinkHint')"
              :data-testid="`workspace-relink-${workspace.workspaceId}`"
              @click="emit('action', {
                method: 'workspace.relink',
                params: {
                  workspaceId: workspace.workspaceId,
                  selectedRootGrant: HOST_WORKSPACE_ROOT_GRANT,
                },
              })"
            >
              <template #icon><NIcon><FolderInput /></NIcon></template>
              {{ t("workspaceV2.center.relink") }}
            </NButton>
            <NButton
              size="small"
              quaternary
              @click="emit('action', { method: 'workspace.remove', params: { workspaceId: workspace.workspaceId } })"
            >
              {{ t("workspaceV2.center.remove") }}
            </NButton>
            <NButton
              size="small"
              quaternary
              type="warning"
              :data-testid="`workspace-delete-${workspace.workspaceId}`"
              @click="planDelete(workspace, $event)"
            >
              <template #icon><NIcon><Trash2 /></NIcon></template>
              {{ t("workspaceV2.center.delete") }}
            </NButton>
            <NButton
              size="small"
              type="primary"
              :disabled="session.isTransitioning"
              :aria-label="t('workspaceV2.center.openNamed', { name: workspace.displayName })"
              @click="emit('open', workspace)"
            >
              {{ t("workspaceV2.center.open") }}
              <template #icon><NIcon><ArrowRight /></NIcon></template>
            </NButton>
          </div>
        </footer>
      </article>
    </section>

    <section v-else class="center-empty">
      <span aria-hidden="true"><HardDrive :size="24" /></span>
      <h2>{{ t("workspaceV2.center.empty") }}</h2>
      <p>{{ t("workspaceV2.center.emptyHint") }}</p>
      <div>
        <NButton @click="openFlow('connect', $event)">{{ t("workspaceV2.center.connectFolder") }}</NButton>
        <NButton
          v-if="session.snapshotPackageEnabled"
          data-testid="workspace-import-package-empty"
          @click="beginPackageImport($event)"
        >
          {{ t("workspaceV2.center.import") }}
        </NButton>
        <NButton type="primary" @click="openFlow('create', $event)">{{ t("workspaceV2.center.createFirst") }}</NButton>
      </div>
    </section>

    <NModal
      :show="flow !== null"
      preset="card"
      class="workspace-flow-modal"
      :style="workspaceFlowModalStyle"
      :content-style="workspaceFlowContentStyle"
      :title="flow === 'create' ? t('workspaceV2.center.createTitle') : t('workspaceV2.center.connectTitle')"
      :trap-focus="true"
      :auto-focus="true"
      :mask-closable="false"
      aria-modal="true"
      data-testid="workspace-flow-modal"
      @update:show="show => { if (!show) closeFlow() }"
    >
      <div v-if="flow === 'create'" class="workspace-flow">
        <label>
          <span>{{ t("workspaceV2.center.name") }}</span>
          <NInput v-model:value="displayName" autofocus :placeholder="t('workspaceV2.center.namePlaceholder')" />
        </label>
        <fieldset>
          <legend>{{ t("workspaceV2.center.locationPolicy") }}</legend>
          <NRadioGroup
            v-model:value="locationPolicy"
            class="flow-options"
            data-testid="workspace-location-policy"
          >
            <NRadioButton value="managedDefault">
              <strong>{{ t("workspaceV2.center.locationManaged") }}</strong>
              <small>{{ t("workspaceV2.center.locationManagedHint") }}</small>
            </NRadioButton>
            <NRadioButton value="other">
              <strong>{{ t("workspaceV2.center.locationOther") }}</strong>
              <small>{{ t("workspaceV2.center.locationOtherHint") }}</small>
            </NRadioButton>
          </NRadioGroup>
        </fieldset>
        <fieldset>
          <legend>{{ t("workspaceV2.center.storageMode") }}</legend>
          <NRadioGroup v-model:value="storageMode" class="flow-options">
            <NRadioButton value="direct">
              <strong>{{ t("workspaceV2.storage.direct") }}</strong>
              <small>{{ t("workspaceV2.center.directHint") }}</small>
            </NRadioButton>
            <NRadioButton value="mirrored" :disabled="!mirroredCreationAvailable">
              <strong>{{ t("workspaceV2.storage.mirrored") }}</strong>
              <small>
                {{ locationPolicy === "managedDefault"
                  ? t("workspaceV2.center.mirroredManagedUnavailable")
                  : mirroredCreationEnabled
                    ? t("workspaceV2.center.mirroredHint")
                    : t("workspaceV2.center.mirroredUnavailable") }}
              </small>
            </NRadioButton>
          </NRadioGroup>
        </fieldset>
        <NCheckbox
          v-if="locationPolicy === 'other'"
          v-model:checked="userMarkedSync"
          data-testid="workspace-user-marked-sync"
        >
          <span class="sync-provider-option">
            <strong>{{ t("workspaceV2.center.userMarkedSync") }}</strong>
            <small>{{ t("workspaceV2.center.userMarkedSyncHint") }}</small>
          </span>
        </NCheckbox>
        <label>
          <span>{{ t("workspaceV2.storage.encryption") }}</span>
          <NSelect
            v-model:value="encryptionMode"
            :options="[
              { label: t('workspaceV2.storage.encryption.none'), value: 'none' },
              { label: t('workspaceV2.storage.encryption.convenient'), value: 'convenient' },
              { label: t('workspaceV2.storage.encryption.protected'), value: 'protected' },
            ]"
          />
        </label>
        <NAlert
          :type="encryptionMode === 'protected' ? 'warning' : 'info'"
          :title="t(`workspaceV2.storage.encryptionNotice.${encryptionMode}`)"
        >
          <span>{{ t(`workspaceV2.storage.encryptionDetail.${encryptionMode}`) }}</span>
          <NButton
            v-if="encryptionMode === 'convenient'"
            size="tiny"
            text
            data-testid="workspace-copy-convenient-password"
            @click="copyConvenientPassword"
          >
            <template #icon><NIcon><Copy /></NIcon></template>
            {{ convenientPasswordCopied ? t("common.copied") : t("common.copy") }}
          </NButton>
        </NAlert>
        <NAlert type="info" :title="t('workspaceV2.center.locationTitle')">
          {{ t(locationPolicy === "managedDefault"
            ? "workspaceV2.center.locationManagedSummary"
            : "workspaceV2.center.locationHint") }}
        </NAlert>
      </div>
      <div v-else class="workspace-flow">
        <NAlert type="info" :title="t('workspaceV2.center.connectSafety')">
          {{ t("workspaceV2.center.connectHint") }}
        </NAlert>
        <p>{{ t("workspaceV2.center.connectProbe") }}</p>
      </div>
      <template #footer>
        <div class="flow-actions">
          <NButton @click="closeFlow">{{ t("common.cancel") }}</NButton>
          <NButton
            type="primary"
            :disabled="flow === 'create' && !displayName.trim()"
            data-testid="workspace-flow-confirm"
            @click="confirmFlow"
          >
            {{ flow === "create"
              ? t(locationPolicy === "managedDefault"
                ? "workspaceV2.center.createManaged"
                : "workspaceV2.center.chooseLocation")
              : t("workspaceV2.center.chooseFolder") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="protection.snapshotPackagePlan !== null"
      preset="card"
      class="workspace-flow-modal"
      :style="workspaceFlowModalStyle"
      :content-style="workspaceFlowContentStyle"
      :title="t('workspaceV2.center.importTitle')"
      :trap-focus="true"
      :auto-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closePackageImport() }"
    >
      <div v-if="protection.snapshotPackagePlan" class="workspace-flow">
        <NAlert
          :type="protection.snapshotPackagePlan.trusted ? 'success' : 'warning'"
          :title="protection.snapshotPackagePlan.trusted
            ? t('workspaceV2.snapshot.packageTrusted')
            : t('workspaceV2.snapshot.packageUntrusted')"
        >
          {{ t("workspaceV2.snapshot.packageCount", {
            count: protection.snapshotPackagePlan.snapshotCount,
          }) }}
        </NAlert>
        <dl class="import-summary">
          <div>
            <dt>{{ t("workspaceV2.snapshot.packageWorkspace") }}</dt>
            <dd><code>{{ protection.snapshotPackagePlan.workspaceId }}</code></dd>
          </div>
        </dl>
        <NInput
          v-if="protection.snapshotPackagePlan.encrypted
            && !protection.snapshotPackagePlan.verified"
          v-model:value="importCredential"
          type="password"
          show-password-on="click"
          :placeholder="t('workspaceV2.snapshot.packageCredential')"
          data-testid="workspace-import-credential"
        />
        <p>{{ t("workspaceV2.center.importTargetHint") }}</p>
      </div>
      <template #footer>
        <div class="flow-actions">
          <NButton
            :disabled="protection.busyOperation !== null"
            @click="closePackageImport"
          >
            {{ t("common.cancel") }}
          </NButton>
          <NButton
            type="primary"
            data-testid="workspace-import-apply"
            :disabled="protection.busyOperation !== null || Boolean(
              protection.snapshotPackagePlan?.encrypted
              && !protection.snapshotPackagePlan.verified
              && !importCredential
            )"
            @click="applyPackageImport"
          >
            {{ t("workspaceV2.center.importChooseTarget") }}
          </NButton>
        </div>
      </template>
    </NModal>

    <NModal
      :show="deletePlan !== null"
      preset="card"
      class="workspace-flow-modal"
      :style="workspaceFlowModalStyle"
      :content-style="workspaceFlowContentStyle"
      :title="t('workspaceV2.center.deleteTitle')"
      :trap-focus="true"
      :auto-focus="true"
      :mask-closable="false"
      aria-modal="true"
      @update:show="show => { if (!show) closeDelete() }"
    >
      <div v-if="deletePlan" class="workspace-flow">
        <NAlert type="error" :title="deletePlan.displayName">
          {{ t("workspaceV2.center.deleteHint") }}
        </NAlert>
        <label>
          <span>{{ t("workspaceV2.center.deleteConfirmation", { name: deletePlan.displayName }) }}</span>
          <NInput
            v-model:value="deleteConfirmation"
            autofocus
            :placeholder="deletePlan.displayName"
          />
        </label>
      </div>
      <template #footer>
        <div class="flow-actions">
          <NButton @click="closeDelete">{{ t("common.cancel") }}</NButton>
          <NButton
            type="error"
            :disabled="!deletePlan || (deletePlan.requiresTypedName && deleteConfirmation !== deletePlan.displayName)"
            data-testid="workspace-delete-apply"
            @click="applyDelete"
          >
            {{ t("workspaceV2.center.deleteApply") }}
          </NButton>
        </div>
      </template>
    </NModal>
  </main>
</template>

<style scoped>
.workspace-center {
  height: 100%;
  overflow: auto;
  padding: clamp(28px, 6vw, 72px);
  background:
    linear-gradient(135deg, color-mix(in srgb, var(--vt-color-primary-50) 55%, transparent), transparent 42%),
    var(--vt-bg);
}
.center-hero {
  display: grid;
  grid-template-columns: 48px minmax(260px, 1fr) auto;
  align-items: start;
  gap: 18px;
  max-width: 1040px;
  margin: 0 auto 28px;
}
.hero-mark,
.center-empty > span {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  color: var(--vt-color-primary-500);
  border: 1px solid var(--vt-color-primary-100);
  border-radius: 14px;
  background: var(--vt-color-primary-50);
}
.eyebrow {
  margin: 0 0 5px;
  color: var(--vt-color-primary-500);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: .16em;
}
.center-hero h1,
.center-empty h2 {
  margin: 0;
  color: var(--vt-fg);
  font-size: clamp(22px, 3vw, 30px);
  font-weight: 650;
  letter-spacing: -.025em;
}
.center-hero p:not(.eyebrow),
.center-empty p {
  max-width: 640px;
  margin: 7px 0 0;
  color: var(--vt-fg-muted);
  line-height: 1.65;
}
.hero-actions,
.center-empty > div,
.card-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}
.workspace-catalog {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
  gap: 14px;
  max-width: 1040px;
  margin: 0 auto;
}
.workspace-card {
  position: relative;
  min-width: 0;
  padding: 18px;
  overflow: hidden;
  border: 1px solid var(--vt-border);
  border-radius: 12px;
  background: var(--vt-bg);
  box-shadow: var(--vt-shadow-1);
  transition: border-color var(--vt-duration-fast), transform var(--vt-duration-fast), box-shadow var(--vt-duration-fast);
}
.workspace-card::before {
  position: absolute;
  top: 0;
  left: 0;
  width: 3px;
  height: 100%;
  background: var(--vt-color-primary-500);
  content: "";
}
.workspace-card.health-offline::before,
.workspace-card.health-degraded::before { background: var(--vt-color-warning); }
.workspace-card.health-corrupt::before { background: var(--vt-color-danger-500); }
.workspace-card:hover {
  border-color: color-mix(in srgb, var(--vt-color-primary-500) 35%, var(--vt-border));
  box-shadow: var(--vt-shadow-2);
  transform: translateY(-1px);
}
.card-leading { display: flex; gap: 12px; min-width: 0; }
.workspace-monogram {
  display: grid;
  flex: 0 0 36px;
  place-items: center;
  height: 36px;
  color: var(--vt-color-primary-600);
  border-radius: 10px;
  background: var(--vt-color-primary-50);
  font-weight: 700;
}
.workspace-copy { min-width: 0; flex: 1; }
.workspace-name-row { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.workspace-name-row h2 {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  font-size: var(--vt-font-title);
  font-weight: 620;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workspace-copy code {
  display: block;
  margin-top: 5px;
  overflow: hidden;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workspace-card dl {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin: 18px 0;
}
.workspace-card dl > div {
  min-width: 0;
  padding: 9px 10px;
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.workspace-card dt { color: var(--vt-fg-muted); font-size: 10px; }
.workspace-card dd {
  margin: 3px 0 0;
  overflow: hidden;
  color: var(--vt-fg-secondary);
  font-size: var(--vt-font-caption);
  font-weight: 550;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workspace-card footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.health-note { display: flex; align-items: center; gap: 5px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.center-empty {
  display: flex;
  max-width: 620px;
  min-height: 360px;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  margin: 30px auto;
  padding: 42px;
  border: 1px dashed var(--vt-border-strong);
  border-radius: 16px;
  background: color-mix(in srgb, var(--vt-bg-subtle) 62%, var(--vt-bg));
  text-align: center;
}
.center-empty > span { margin-bottom: 15px; }
.center-empty > div { margin-top: 20px; justify-content: center; }
:global(.workspace-flow-modal) { width: min(580px, calc(100vw - 28px)); }
.workspace-flow { display: grid; gap: 16px; }
.workspace-flow > label { display: grid; gap: 6px; }
.workspace-flow > label > span,
.workspace-flow legend { color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); font-weight: 600; }
.workspace-flow fieldset { min-width: 0; margin: 0; padding: 0; border: 0; }
.workspace-flow legend { margin-bottom: 7px; }
.flow-options {
  display: grid !important;
  width: 100%;
  height: auto !important;
  min-height: 76px;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.flow-options :deep(.n-radio-group__splitor) { display: none; }
.flow-options :deep(.n-radio-button) { height: auto; min-height: 76px; padding: 10px !important; border-radius: var(--vt-radius-lg) !important; white-space: normal; }
.flow-options :deep(.n-radio-button__content) { display: flex; align-items: flex-start; flex-direction: column; gap: 3px; }
.flow-options small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.sync-provider-option { display: inline-grid; gap: 3px; padding-inline-start: 4px; }
.sync-provider-option small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.45; }
.workspace-flow > p { margin: 0; color: var(--vt-fg-muted); line-height: 1.6; }
.flow-actions { display: flex; justify-content: flex-end; gap: 8px; }
.import-summary { margin: 0; }
.import-summary div { display: grid; gap: 4px; }
.import-summary dt { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.import-summary dd { margin: 0; overflow-wrap: anywhere; }
@media (max-width: 760px) {
  .workspace-center { padding: 24px 16px; }
  .center-hero { grid-template-columns: 44px 1fr; }
  .hero-actions { grid-column: 1 / -1; justify-content: flex-start; }
  .workspace-card dl { grid-template-columns: 1fr; }
  .workspace-card footer { align-items: flex-start; flex-direction: column; }
  .flow-options { grid-template-columns: 1fr; }
}
@media (prefers-reduced-motion: reduce) {
  .workspace-card { transition: none; }
  .workspace-card:hover { transform: none; }
}
</style>
