<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { NAlert, NButton, NIcon, NRadioButton, NRadioGroup, NTag } from "naive-ui";
import {
  AlertTriangle,
  CheckCircle2,
  GitCompareArrows,
  RefreshCw,
  ShieldAlert,
} from "lucide-vue-next";
import { t } from "@/i18n";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type { WorkspaceV2UiAction } from "@/contracts/workspaceV2Bridge";

export type ConflictCenterAction = WorkspaceV2UiAction<
  | "conflict.list"
  | "conflict.inspect"
  | "conflict.preview"
  | "conflict.apply"
  | "replica.forceTakeover"
>;

const emit = defineEmits<{ action: [action: ConflictCenterAction] }>();
const protection = useWorkspaceProtectionStore();
const session = useWorkspaceSessionStore();
const selectedId = ref<string | null>(null);
const focusedId = ref<string | null>(null);

const conflictSets = computed(() => protection.conflictSets);
const selectedItems = computed(() =>
  protection.conflicts.filter((conflict) => conflict.conflictId === selectedId.value));
const selectedPlan = computed(() =>
  selectedId.value ? protection.conflictPlans[selectedId.value] ?? null : null);
const advisory = computed(() => session.activeWorkspace?.coordinationStrength === "advisory");

watch(conflictSets, (conflicts) => {
  if (!conflicts.some((conflict) => conflict.conflictId === selectedId.value)) {
    selectedId.value = conflicts[0]?.conflictId ?? null;
  }
  if (!conflicts.some((conflict) => conflict.conflictId === focusedId.value)) {
    focusedId.value = conflicts[0]?.conflictId ?? null;
  }
}, { immediate: true });

function focusItem(index: number): void {
  const conflict = conflictSets.value[index];
  if (!conflict) return;
  focusedId.value = conflict.conflictId;
  selectedId.value = conflict.conflictId;
  void nextTick(() =>
    document.querySelector<HTMLElement>(`[data-conflict-id="${conflict.conflictId}"]`)?.focus());
}

function onListKeydown(event: KeyboardEvent, index: number): void {
  if (event.key === "ArrowDown") focusItem(Math.min(conflictSets.value.length - 1, index + 1));
  else if (event.key === "ArrowUp") focusItem(Math.max(0, index - 1));
  else if (event.key === "Home") focusItem(0);
  else if (event.key === "End") focusItem(conflictSets.value.length - 1);
  else return;
  event.preventDefault();
}

function preview(): void {
  if (!selectedId.value || !selectedItems.value.length ||
      selectedItems.value.some((item) => !item.selected)) return;
  emit("action", {
    method: "conflict.preview",
    params: {
      conflictId: selectedId.value,
      choices: selectedItems.value.map((item) => ({
        itemId: item.itemId,
        kind: item.kind,
        side: item.selected!,
      })),
    },
  });
}

function chooseSelected(
  itemId: string,
  value: "local" | "replica" | "both",
): void {
  if (!selectedId.value) return;
  protection.chooseConflict(selectedId.value, itemId, value);
  protection.setConflictPlan(selectedId.value, null);
}

function inspect(conflictId: string): void {
  selectedId.value = conflictId;
  focusedId.value = conflictId;
  emit("action", { method: "conflict.inspect", params: { conflictId } });
}
</script>

<template>
  <main class="conflict-center" data-testid="conflict-center">
    <header>
      <div class="conflict-title">
        <span><GitCompareArrows :size="20" /></span>
        <div>
          <p>CONFLICT CENTER</p>
          <h1>{{ t("workspaceV2.conflict.title") }}</h1>
          <small>{{ t("workspaceV2.conflict.description") }}</small>
        </div>
      </div>
      <NButton
        quaternary
        @click="emit('action', { method: 'conflict.list', params: { cursor: null, limit: 50 } })"
      >
        <template #icon><NIcon><RefreshCw /></NIcon></template>
        {{ t("workspaceV2.conflict.refresh") }}
      </NButton>
    </header>

    <NAlert v-if="advisory" type="warning" :title="t('workspaceV2.conflict.advisoryTitle')">
      {{ t("workspaceV2.conflict.advisoryHint") }}
      <template #action>
        <NButton
          size="small"
          type="warning"
          @click="emit('action', { method: 'replica.forceTakeover', params: { mode: 'provisional' } })"
        >
          {{ t("workspaceV2.conflict.takeover") }}
        </NButton>
      </template>
    </NAlert>

    <div v-if="conflictSets.length" class="conflict-workbench">
      <section
        class="conflict-list"
        role="listbox"
        :aria-label="t('workspaceV2.conflict.list')"
      >
        <button
          v-for="(conflict, index) in conflictSets"
          :key="conflict.conflictId"
          class="conflict-row"
          :class="{ selected: selectedId === conflict.conflictId }"
          role="option"
          :aria-selected="selectedId === conflict.conflictId"
          :tabindex="focusedId === conflict.conflictId ? 0 : -1"
          :data-conflict-id="conflict.conflictId"
          @click="inspect(conflict.conflictId)"
          @keydown="onListKeydown($event, index)"
        >
          <span class="conflict-kind">
            <AlertTriangle :size="15" />
          </span>
          <span>
            <strong>{{ conflict.itemCount }} {{ t("workspaceV2.conflict.choice") }}</strong>
            <small>{{ conflict.createdAt }}</small>
          </span>
          <NTag size="small" :type="conflict.state === 'ready' ? 'success' : 'warning'">
            {{ conflict.state === "ready" ? t("workspaceV2.conflict.chosen") : t("workspaceV2.conflict.pending") }}
          </NTag>
        </button>
      </section>

      <section v-if="selectedItems.length" class="conflict-detail">
        <header>
          <div><small>CONFLICT SET</small><strong>{{ selectedItems.length }} {{ t("workspaceV2.conflict.choice") }}</strong></div>
        </header>

        <article v-for="item in selectedItems" :key="item.itemId" class="conflict-item">
          <header>
            <div><small>{{ item.kind.toLocaleUpperCase() }}</small><strong>{{ item.path }}</strong></div>
            <NTag :type="item.state === 'failed' ? 'error' : item.state === 'ready' ? 'success' : 'warning'">
              {{ t(`workspaceV2.conflict.state.${item.state}`) }}
            </NTag>
          </header>
          <div class="base-line">
            <span>{{ t("workspaceV2.conflict.base") }}</span>
            <p>{{ item.baseSummary }}</p>
          </div>
          <NRadioGroup
            :value="item.selected"
            class="choice-grid"
            :aria-label="`${t('workspaceV2.conflict.choice')}: ${item.path}`"
            @update:value="chooseSelected(item.itemId, $event as 'local' | 'replica' | 'both')"
          >
            <NRadioButton value="local" class="choice-card">
              <span>{{ t("workspaceV2.conflict.local") }}</span>
              <strong>{{ item.localSummary }}</strong>
            </NRadioButton>
            <NRadioButton value="replica" class="choice-card">
              <span>{{ t("workspaceV2.conflict.replica") }}</span>
              <strong>{{ item.replicaSummary }}</strong>
            </NRadioButton>
            <NRadioButton v-if="item.kind === 'file'" value="both" class="choice-card">
              <span>{{ t("workspaceV2.conflict.both") }}</span>
              <strong>{{ t("workspaceV2.conflict.bothHint") }}</strong>
            </NRadioButton>
          </NRadioGroup>
          <section class="dependency-panel">
            <div>
              <ShieldAlert :size="16" />
              <strong>{{ t("workspaceV2.conflict.dependencies") }}</strong>
            </div>
            <p v-if="!item.dependencies.length">{{ t("workspaceV2.conflict.noDependencies") }}</p>
            <ul v-else>
              <li v-for="dependency in item.dependencies" :key="dependency">{{ dependency }}</li>
            </ul>
          </section>
        </article>

        <NAlert
          v-if="selectedPlan"
          :type="selectedPlan.valid ? 'success' : 'error'"
          :title="selectedPlan.valid ? t('workspaceV2.conflict.planReady') : t('workspaceV2.conflict.planBlocked')"
        >
          <ul v-if="selectedPlan.diagnostics.length">
            <li v-for="diagnostic in selectedPlan.diagnostics" :key="diagnostic">{{ diagnostic }}</li>
          </ul>
        </NAlert>

        <footer>
          <p>{{ t("workspaceV2.conflict.recovery") }}</p>
          <div>
            <NButton
              size="small"
              :disabled="selectedItems.some((item) => !item.selected)"
              data-testid="conflict-preview"
              @click="preview"
            >
              {{ t("workspaceV2.conflict.preview") }}
            </NButton>
            <NButton
              size="small"
              type="primary"
              :disabled="!selectedPlan?.valid"
              data-testid="conflict-apply"
              @click="selectedPlan && emit('action', { method: 'conflict.apply', params: { planId: selectedPlan.planId } })"
            >
              {{ t("workspaceV2.conflict.apply") }}
            </NButton>
          </div>
        </footer>
      </section>
    </div>

    <section v-else class="conflict-empty">
      <span><CheckCircle2 :size="23" /></span>
      <h2>{{ t("workspaceV2.conflict.empty") }}</h2>
      <p>{{ t("workspaceV2.conflict.emptyHint") }}</p>
    </section>
  </main>
</template>

<style scoped>
.conflict-center { height: 100%; overflow: auto; padding: clamp(24px, 5vw, 56px); background: var(--vt-bg); }
.conflict-center > header { display: flex; max-width: 1080px; align-items: center; justify-content: space-between; gap: 16px; margin: 0 auto 16px; }
.conflict-title { display: flex; align-items: flex-start; gap: 12px; }
.conflict-title > span,
.conflict-empty > span {
  display: grid;
  flex: none;
  place-items: center;
  width: 42px;
  height: 42px;
  color: var(--vt-color-warning);
  border-radius: 12px;
  background: var(--vt-color-warning-50);
}
.conflict-title p { margin: 0; color: var(--vt-color-warning); font-size: 10px; font-weight: 700; letter-spacing: .14em; }
.conflict-title h1 { margin: 2px 0; font-size: var(--vt-font-heading); font-weight: 650; letter-spacing: -.02em; }
.conflict-title small { color: var(--vt-fg-muted); }
.conflict-center > :deep(.n-alert) { max-width: 1080px; margin: 0 auto 14px; }
.conflict-workbench {
  display: grid;
  grid-template-columns: minmax(280px, .75fr) minmax(420px, 1.25fr);
  max-width: 1080px;
  min-height: 520px;
  margin: 0 auto;
  overflow: hidden;
  border: 1px solid var(--vt-border);
  border-radius: 12px;
  background: var(--vt-bg);
  box-shadow: var(--vt-shadow-1);
}
.conflict-list { overflow: auto; border-right: 1px solid var(--vt-border); }
.conflict-row {
  display: grid;
  grid-template-columns: 30px minmax(100px, 1fr) auto;
  align-items: center;
  gap: 9px;
  width: 100%;
  min-height: 64px;
  padding: 10px 12px;
  color: inherit;
  border: 0;
  border-bottom: 1px solid var(--vt-border);
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.conflict-row:hover { background: var(--vt-bg-subtle); }
.conflict-row.selected { background: var(--vt-color-primary-50); box-shadow: inset 3px 0 var(--vt-color-primary-500); }
:root.dark .conflict-row.selected { background: rgba(91, 139, 255, .12); }
.conflict-kind { display: grid; place-items: center; width: 28px; height: 28px; color: var(--vt-color-warning); border-radius: 8px; background: var(--vt-color-warning-50); }
.conflict-row > span:nth-child(2) { display: flex; min-width: 0; flex-direction: column; }
.conflict-row strong { overflow: hidden; font-weight: 570; text-overflow: ellipsis; white-space: nowrap; }
.conflict-row small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.conflict-detail { padding: 20px; overflow: auto; background: var(--vt-bg-subtle); }
.conflict-detail > header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.conflict-detail > header > div { display: flex; min-width: 0; flex-direction: column; }
.conflict-detail > header small { color: var(--vt-color-primary-500); font-size: 9px; font-weight: 700; letter-spacing: .14em; }
.conflict-detail > header strong { margin-top: 3px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.base-line { margin: 18px 0 10px; padding: 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.base-line span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.base-line p { margin: 4px 0 0; }
.choice-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.choice-card { height: auto; min-height: 94px; padding: 12px !important; border-radius: var(--vt-radius-lg) !important; white-space: normal; }
.choice-card :deep(.n-radio-button__content) { display: flex; align-items: flex-start; flex-direction: column; gap: 5px; }
.choice-card span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.dependency-panel { margin-top: 14px; padding: 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.dependency-panel > div { display: flex; align-items: center; gap: 7px; }
.dependency-panel > div svg { color: var(--vt-color-warning); }
.dependency-panel p,
.dependency-panel ul { margin: 6px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.conflict-detail footer { display: flex; align-items: flex-end; justify-content: space-between; gap: 14px; margin-top: 18px; }
.conflict-detail footer p { max-width: 360px; margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.5; }
.conflict-detail footer > div { display: flex; flex: none; gap: 8px; }
.conflict-empty { display: flex; max-width: 680px; min-height: 380px; align-items: center; justify-content: center; flex-direction: column; margin: 30px auto; text-align: center; }
.conflict-empty > span { color: var(--vt-color-success-600); background: var(--vt-color-success-50); }
.conflict-empty h2 { margin: 12px 0 4px; font-size: var(--vt-font-title); }
.conflict-empty p { max-width: 420px; margin: 0; color: var(--vt-fg-muted); }
@media (max-width: 800px) {
  .conflict-workbench { grid-template-columns: 1fr; }
  .conflict-list { max-height: 240px; border-right: 0; border-bottom: 1px solid var(--vt-border); }
}
@media (max-width: 560px) {
  .conflict-center { padding: 20px 12px; }
  .choice-grid { grid-template-columns: 1fr; }
  .conflict-detail footer { align-items: stretch; flex-direction: column; }
  .conflict-detail footer > div { justify-content: flex-end; }
}
</style>
