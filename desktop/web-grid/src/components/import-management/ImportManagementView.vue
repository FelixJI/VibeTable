<script setup lang="ts">
import { computed, ref } from "vue";
import { NAlert, NButton, NEmpty, NIcon, NTag, NTooltip } from "naive-ui";
import { ArrowDownToLine, ChevronDown, CloudOff, FileSpreadsheet, FileText, RefreshCw, Table2 } from "@lucide/vue";
import type { ImportHistoryEntry, SourceImportEntry } from "@/contracts/importManagement";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "@/components/layout/collectionLabel";
import { IMPORT_SOURCES, type ImportSourceId } from "./importSources";
import SourceImportHistory from "./SourceImportHistory.vue";
import { getLocale, t } from "@/i18n";

/**
 * Pure-presentation import management page. WorkspaceView owns every service
 * call (picker, preview, cancellation, history RPC); this component only reads
 * the workspace table catalog for target selection and emits user intent.
 */
const props = withDefaults(defineProps<{
  loading: boolean;
  error: string | null;
  loaded: boolean;
  items: readonly ImportHistoryEntry[];
  /** Durable source-migration receipts projected by importManagementService. */
  sourceEntries?: readonly SourceImportEntry[];
  activeTaskId: string | null;
  taskCancellable: boolean;
  cancellingTaskId: string | null;
  pendingSourceName: string | null;
  canStart: boolean;
}>(), {
  sourceEntries: () => [],
});

const emit = defineEmits<{
  newImport: [];
  cancelSource: [];
  chooseTarget: [collection: string];
  refresh: [];
  cancelTask: [taskId: string];
  openTarget: [collection: string];
  openSourceTarget: [tableId: string];
}>();

const workspace = useWorkspaceStore();

const sourceIcons: Record<ImportSourceId, typeof FileText> = {
  csv: FileText,
  xlsx: FileSpreadsheet,
  feishu: CloudOff,
  wps: CloudOff,
};

const catalogSources = IMPORT_SOURCES.map((source) => ({
  ...source,
  label: t(source.labelKey),
  hint: t("importManagement.source.unavailableHint"),
  icon: sourceIcons[source.id],
}));

const targetTables = computed(() => workspace.collections.map((collection) => ({
  collection: collection.collection,
  label: collectionLabel(collection, workspace.displayNames),
})));

const stateLabels: Record<ImportHistoryEntry["state"], { label: string; kind: "default" | "info" | "success" | "warning" | "error" }> = {
  queued: { label: t("importManagement.state.queued"), kind: "default" },
  running: { label: t("importManagement.state.running"), kind: "info" },
  succeeded: { label: t("importManagement.state.succeeded"), kind: "success" },
  failed: { label: t("importManagement.state.failed"), kind: "error" },
  cancelled: { label: t("importManagement.state.cancelled"), kind: "warning" },
  interrupted: { label: t("importManagement.state.interrupted"), kind: "warning" },
  aborted: { label: t("importManagement.state.aborted"), kind: "default" },
};

function stateOf(entry: ImportHistoryEntry) {
  return stateLabels[entry.state];
}

function isCancellable(entry: ImportHistoryEntry): boolean {
  // Host projects queued/running entries for the current session only; showing
  // cancellation anywhere else would pretend to own a foreign/terminal task.
  return entry.state === "queued" || entry.state === "running";
}

function commitKnown(entry: ImportHistoryEntry): boolean {
  return entry.commitState === "committed";
}

function entryHint(entry: ImportHistoryEntry): string | null {
  if (entry.state === "interrupted") return t("importManagement.entry.interruptedHint");
  if (entry.state === "cancelled") return t("importManagement.entry.cancelledHint");
  if (!commitKnown(entry)) return t("importManagement.entry.commitUnknownHint");
  return null;
}

function formatTime(value: string | null): string {
  if (!value) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? value
    : new Intl.DateTimeFormat(getLocale(), { dateStyle: "short", timeStyle: "medium" }).format(parsed);
}

function sourceLabel(entry: ImportHistoryEntry): string {
  return entry.sourceType === "xlsx"
    ? t("importManagement.source.xlsx")
    : t("importManagement.source.csv");
}

function targetLabel(entry: ImportHistoryEntry): string {
  const known = workspace.collections.find((collection) => collection.collection === entry.collection);
  return known ? collectionLabel(known, workspace.displayNames) : entry.collection;
}

/** Presentation-only expanded-detail state; services own no part of it. */
const expandedTaskIds = ref(new Set<string>());

function isExpanded(taskId: string): boolean {
  return expandedTaskIds.value.has(taskId);
}

function toggleExpanded(taskId: string): void {
  const next = new Set(expandedTaskIds.value);
  if (next.has(taskId)) next.delete(taskId);
  else next.add(taskId);
  expandedTaskIds.value = next;
}
</script>

<template>
  <section class="import-management" data-testid="import-management">
    <header class="import-management-header">
      <div>
        <h2>{{ t("importManagement.title") }}</h2>
        <p>{{ t("importManagement.subtitle") }}</p>
      </div>
      <NButton
        size="small"
        secondary
        :loading="props.loading"
        data-testid="import-history-refresh"
        @click="emit('refresh')"
      >
        <template #icon><NIcon :size="14"><RefreshCw /></NIcon></template>
        {{ t("importManagement.history.refresh") }}
      </NButton>
    </header>

    <div v-if="props.activeTaskId" class="import-active-task" role="status" aria-live="polite" data-testid="import-active-task">
      <div class="import-active-task-copy">
        <strong>{{ t("importManagement.task.title") }}</strong>
        <span>{{ t("importManagement.task.running", { taskId: props.activeTaskId }) }}</span>
      </div>
      <NButton
        size="tiny"
        type="warning"
        secondary
        :disabled="!props.taskCancellable"
        :loading="props.cancellingTaskId === props.activeTaskId"
        data-testid="import-cancel-active-task"
        @click="emit('cancelTask', props.activeTaskId)"
      >
        {{ t("importManagement.task.cancel") }}
      </NButton>
    </div>

    <section class="import-new" aria-labelledby="import-new-title">
      <h3 id="import-new-title">{{ t("importManagement.newImport") }}</h3>
      <template v-if="!props.pendingSourceName">
        <p class="import-new-hint">{{ t("importManagement.source.hint") }}</p>
        <div class="import-source-grid">
          <template v-for="source in catalogSources" :key="source.id">
            <button
              v-if="source.available"
              type="button"
              class="import-source-card"
              :disabled="!props.canStart"
              :data-testid="`import-source-${source.id}`"
              @click="emit('newImport')"
            >
              <span class="import-source-icon"><NIcon :size="19"><component :is="source.icon" /></NIcon></span>
              <span class="import-source-copy">
                <strong>{{ source.label }}</strong>
                <small>{{ t("importManagement.source.hint") }}</small>
              </span>
            </button>
            <NTooltip v-else placement="top" :delay="200">
              <template #trigger>
                <div
                  class="import-source-card import-source-card--unavailable"
                  :data-testid="`import-source-${source.id}`"
                  role="button"
                  aria-disabled="true"
                >
                  <span class="import-source-icon"><NIcon :size="19"><component :is="source.icon" /></NIcon></span>
                  <span class="import-source-copy">
                    <strong>{{ source.label }}</strong>
                    <small>{{ t(source.unavailableReasonKey) }}</small>
                  </span>
                  <NTag size="small" type="warning" :bordered="false">{{ t("importManagement.source.unavailable") }}</NTag>
                </div>
              </template>
              {{ t("importManagement.source.unavailableTooltip") }}
            </NTooltip>
          </template>
        </div>
      </template>
      <template v-else>
        <div class="import-target-picker" data-testid="import-target">
          <div class="import-target-file">
            <NIcon :size="16"><FileSpreadsheet /></NIcon>
            <span>{{ t("importManagement.target.hint", { name: props.pendingSourceName }) }}</span>
            <NButton size="tiny" quaternary data-testid="import-cancel-source" @click="emit('cancelSource')">
              {{ t("importManagement.target.cancel") }}
            </NButton>
          </div>
          <ul class="import-target-list">
            <li v-for="table in targetTables" :key="table.collection">
              <button
                type="button"
                data-testid="import-target-option"
                :data-collection="table.collection"
                @click="emit('chooseTarget', table.collection)"
              >
                <NIcon :size="15"><Table2 /></NIcon>
                <span>{{ table.label }}</span>
                <small v-if="table.label !== table.collection">{{ table.collection }}</small>
              </button>
            </li>
          </ul>
          <NEmpty
            v-if="targetTables.length === 0"
            size="small"
            :description="t('importManagement.target.noTables')"
          />
        </div>
      </template>
    </section>

    <section class="import-history" aria-labelledby="import-history-title">
      <h3 id="import-history-title">{{ t("importManagement.history.title") }}</h3>
      <p v-if="props.loading && !props.loaded" role="status" aria-live="polite">
        {{ t("importManagement.history.loading") }}
      </p>
      <NAlert
        v-else-if="props.error"
        type="error"
        :show-icon="false"
        data-testid="import-history-error"
      >
        {{ t("importManagement.history.error", { message: props.error }) }}
      </NAlert>
      <div v-else-if="!props.items || props.items.length === 0" class="import-history-empty">
        <span class="import-history-empty-icon"><NIcon :size="22"><ArrowDownToLine /></NIcon></span>
        <strong>{{ t("importManagement.history.empty") }}</strong>
        <p>{{ t("importManagement.history.emptyHint") }}</p>
      </div>
      <ol v-else class="import-history-list" data-testid="import-history-list">
        <li
          v-for="entry in props.items"
          :key="entry.taskId"
          class="import-history-item"
          data-testid="import-history-row"
          :data-task-id="entry.taskId"
          :data-state="entry.state"
          :data-commit-state="entry.commitState"
        >
          <div class="import-history-main">
            <div class="import-history-title">
              <strong :title="entry.taskId">{{ entry.sourceName }}</strong>
              <NTag size="small" :type="stateOf(entry).kind" :bordered="false">{{ stateOf(entry).label }}</NTag>
              <NTag v-if="!commitKnown(entry)" size="small" type="warning">{{ t("importManagement.entry.commitUnknown") }}</NTag>
            </div>
            <dl v-if="isExpanded(entry.taskId)">
              <div><dt>{{ t("importManagement.entry.source") }}</dt><dd>{{ sourceLabel(entry) }}</dd></div>
              <div><dt>{{ t("importManagement.entry.target") }}</dt><dd>{{ targetLabel(entry) }}</dd></div>
              <div>
                <dt>{{ t("importManagement.entry.result") }}</dt>
                <dd v-if="commitKnown(entry)" data-testid="import-entry-counts">
                  {{ t("importManagement.entry.created", { count: entry.createdCount ?? 0 }) }}
                  ·
                  {{ t("importManagement.entry.updated", { count: entry.updatedCount ?? 0 }) }}
                </dd>
                <dd v-else data-testid="import-entry-unknown">{{ t("importManagement.entry.commitUnknown") }}</dd>
              </div>
              <div><dt>{{ t("importManagement.entry.startedAt") }}</dt><dd>{{ formatTime(entry.startedAt) }}</dd></div>
              <div><dt>{{ t("importManagement.entry.finishedAt") }}</dt><dd>{{ formatTime(entry.finishedAt) }}</dd></div>
              <div v-if="entry.errorCode">
                <dt>{{ t("importManagement.entry.error") }}</dt>
                <dd data-testid="import-entry-error-code">{{ t("importManagement.entry.errorCode", { code: entry.errorCode }) }}</dd>
              </div>
            </dl>
            <p v-if="isExpanded(entry.taskId) && entryHint(entry)" class="import-history-hint">{{ entryHint(entry) }}</p>
          </div>
          <div class="import-history-actions">
            <NButton
              v-if="isCancellable(entry)"
              size="tiny"
              type="warning"
              secondary
              :loading="props.cancellingTaskId === entry.taskId"
              data-testid="import-history-cancel"
              @click="emit('cancelTask', entry.taskId)"
            >
              {{ t("importManagement.entry.cancel") }}
            </NButton>
            <NButton
              size="tiny"
              tertiary
              :data-testid="`import-history-detail-${entry.taskId}`"
              :aria-expanded="isExpanded(entry.taskId)"
              @click="toggleExpanded(entry.taskId)"
            >
              <template #icon>
                <NIcon :size="13" :class="{ 'import-detail-chevron--open': isExpanded(entry.taskId) }"><ChevronDown /></NIcon>
              </template>
              {{ t("importManagement.entry.detail") }}
            </NButton>
            <NButton
              size="tiny"
              tertiary
              :data-testid="`import-history-target-${entry.taskId}`"
              @click="emit('openTarget', entry.collection)"
            >
              <template #icon><NIcon :size="13"><Table2 /></NIcon></template>
              {{ t("importManagement.entry.openTarget") }}
            </NButton>
          </div>
        </li>
      </ol>
    </section>

    <SourceImportHistory
      :entries="props.sourceEntries"
      :cancelling-job-id="props.cancellingTaskId"
      @cancel-task="(jobId) => emit('cancelTask', jobId)"
      @open-target="(tableId) => emit('openSourceTarget', tableId)"
    />
  </section>
</template>

<style scoped>
.import-management {
  display: flex;
  flex-direction: column;
  gap: 22px;
  height: 100%;
  padding: 22px 26px;
  overflow: auto;
  background: var(--vt-bg);
}
.import-management-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.import-management-header h2 {
  margin: 0 0 4px;
  font-size: var(--vt-font-title, 16px);
  font-weight: 650;
  color: var(--vt-fg);
}
.import-management-header p {
  margin: 0;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.import-active-task {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid var(--vt-color-primary-200);
  border-radius: var(--vt-radius-md);
  background: var(--vt-color-primary-50);
}
.import-active-task-copy { display: grid; gap: 2px; min-width: 0; }
.import-active-task-copy strong { font-size: var(--vt-font-caption); color: var(--vt-fg); font-weight: 600; }
.import-active-task-copy span {
  overflow: hidden; color: var(--vt-fg-muted); font-size: var(--vt-font-caption);
  text-overflow: ellipsis; white-space: nowrap;
}
.import-new h3,
.import-history h3 {
  margin: 0 0 10px;
  font-size: var(--vt-font-caption);
  font-weight: 650;
  letter-spacing: 0.02em;
  color: var(--vt-fg-muted);
  text-transform: uppercase;
}
.import-new-hint { margin: 0 0 10px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.import-source-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 10px;
}
.import-source-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  text-align: left;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
  color: inherit;
  cursor: pointer;
  transition: border-color 140ms var(--vt-ease), box-shadow 140ms var(--vt-ease), transform 140ms var(--vt-ease);
}
button.import-source-card:hover:not(:disabled) {
  border-color: var(--vt-color-primary-300);
  box-shadow: 0 4px 16px rgb(15 23 42 / 8%);
  transform: translateY(-1px);
}
button.import-source-card:disabled { opacity: 0.55; cursor: not-allowed; }
.import-source-card--unavailable { cursor: not-allowed; opacity: 0.72; background: var(--vt-bg-subtle); }
.import-source-icon {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 38px;
  height: 38px;
  color: var(--vt-color-primary-500);
  border-radius: var(--vt-radius-md);
  background: var(--vt-color-primary-50);
}
.import-source-card--unavailable .import-source-icon { color: var(--vt-fg-muted); background: var(--vt-bg-sunken); }
.import-source-copy { display: grid; flex: 1 1 auto; gap: 2px; min-width: 0; }
.import-source-copy strong { color: var(--vt-fg); font-weight: 600; }
.import-source-copy small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.import-target-picker {
  display: grid;
  gap: 10px;
  padding: 14px;
  border: 1px solid var(--vt-color-primary-200);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-color-primary-50);
}
.import-target-file {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
}
.import-target-file > span { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.import-target-list { display: grid; gap: 6px; max-height: 260px; margin: 0; padding: 0; overflow: auto; list-style: none; }
.import-target-list button {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 9px 12px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg);
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.import-target-list button:hover { border-color: var(--vt-color-primary-300); }
.import-target-list button span { font-weight: 600; }
.import-target-list button small { margin-left: auto; color: var(--vt-fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.import-history-empty {
  display: grid;
  justify-items: center;
  gap: 6px;
  padding: 34px 16px;
  border: 1px dashed var(--vt-border);
  border-radius: var(--vt-radius-lg);
  color: var(--vt-fg-muted);
  text-align: center;
}
.import-history-empty-icon {
  display: grid;
  place-items: center;
  width: 42px;
  height: 42px;
  color: var(--vt-color-primary-500);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-color-primary-50);
}
.import-history-empty strong { color: var(--vt-fg); font-weight: 600; }
.import-history-empty p { margin: 0; font-size: var(--vt-font-caption); }
.import-history-list { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.import-history-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 12px 14px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
}
.import-history-main { display: grid; flex: 1 1 auto; gap: 8px; min-width: 0; }
.import-history-title { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.import-history-title strong {
  overflow: hidden;
  max-width: 340px;
  color: var(--vt-fg);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.import-history-main dl {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 22px;
  margin: 0;
}
.import-history-main dl > div { display: flex; align-items: baseline; gap: 6px; }
.import-history-main dt { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.import-history-main dd { margin: 0; color: var(--vt-fg); font-size: var(--vt-font-caption); font-variant-numeric: tabular-nums; }
.import-history-hint {
  margin: 0;
  padding: 6px 10px;
  border-left: 2px solid var(--vt-color-warning);
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  background: var(--vt-bg-subtle);
  border-radius: 0 var(--vt-radius-md) var(--vt-radius-md) 0;
}
.import-history-actions { display: flex; flex: 0 0 auto; flex-direction: column; align-items: flex-end; gap: 6px; }
.import-detail-chevron--open { transform: rotate(180deg); }
</style>
