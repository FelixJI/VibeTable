<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NDatePicker,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NIcon,
  NInput,
  NModal,
  NSelect,
  NSpin,
  NTag,
  NTooltip,
} from "naive-ui";
import {
  Activity,
  AlertTriangle,
  ArchiveRestore,
  ArrowRight,
  Clock3,
  FilterX,
  History as HistoryIcon,
  RefreshCw,
  RotateCcw,
  Search,
  ShieldAlert,
  UserRound,
} from "lucide-vue-next";
import type { HistoryChangeSet, HistoryRecordChange } from "@/contracts";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const props = withDefaults(defineProps<{
  fieldOptions?: readonly { label: string; value: string }[];
}>(), { fieldOptions: () => [] });

const emit = defineEmits<{
  close: [];
  reload: [];
  loadMore: [];
  preview: [target: { itemId: string; revisionId: string; field?: string | null }];
  apply: [];
}>();

const store = useRevisionHistoryStore();
const ui = useUiStore();
const searchDraft = ref("");
const actorDraft = ref("");
const recordDraft = ref("");
const dateRange = ref<[number, number] | null>(null);

watch(
  () => store.panelOpen,
  (open) => {
    if (!open) return;
    searchDraft.value = store.query.search;
    actorDraft.value = store.query.actorId;
    recordDraft.value = store.query.recordId;
    dateRange.value = store.query.dateFrom && store.query.dateTo
      ? [Date.parse(store.query.dateFrom), Date.parse(store.query.dateTo)]
      : null;
  },
);

const title = computed(() => t(`history.title.${store.scope}`));
const scopeLabel = computed(() => {
  if (store.scope === "row") return t("history.scope.row", { item: store.itemId ?? "—" });
  if (store.scope === "cell") {
    return t("history.scope.cell", { item: store.itemId ?? "—", field: store.field ?? "—" });
  }
  return t(`history.scope.${store.scope}`);
});
const actionOptions = computed(() => [
  { label: t("history.action.create"), value: "create" },
  { label: t("history.action.update"), value: "update" },
  { label: t("history.action.delete"), value: "delete" },
  { label: t("history.action.restore"), value: "restore" },
]);
const fieldSelectOptions = computed(() => [...props.fieldOptions]);
const expandedByDefault = computed(() => {
  if (store.scope === "archived") {
    const defaults = new Set(Object.values(store.archivedDefaultRevisionIds));
    const index = store.changeSets.findIndex((changeSet) =>
      recordsFor(changeSet).some((record) => defaults.has(record.revisionId)));
    if (index >= 0) return [changeSetKey(store.changeSets[index]!, index)];
  }
  return store.changeSets[0] ? [changeSetKey(store.changeSets[0], 0)] : [];
});
const previewOpen = computed(() => store.restorePhase !== "idle");
const archivedDefaultRevisionIds = computed(() => {
  if (store.scope !== "archived") return new Set<string>();
  return new Set(Object.values(store.archivedDefaultRevisionIds));
});
const restoreFailureText = computed(() => {
  if (store.restoreErrorCode === "restore_conflict" || store.restoreErrorCode === "schema_drift") {
    return t("history.restoreConflict");
  }
  if (store.restoreErrorCode === "restore_token_expired") return t("history.restoreExpired");
  if (store.restoreErrorCode === "restore_no_fields") return t("history.restoreNoFields");
  return store.restoreError ?? t("history.restoreFailed");
});

const localizedDiagnosticCodes = new Set([
  "primary_key",
  "field_generated",
  "system_field",
  "field_readonly",
  "field_not_updatable",
  "type_incompatible",
  "complex_relation",
  "relation_target_unavailable",
  "field_not_readable",
]);

function diagnosticText(code: string, fallback: string): string {
  return localizedDiagnosticCodes.has(code) ? t(`history.diagnostic.${code}`) : fallback;
}

function changeSetKey(changeSet: HistoryChangeSet, index: number): string {
  return changeSet.activityId || changeSet.rootRevisionId || String(index);
}

function recordsFor(changeSet: HistoryChangeSet): readonly HistoryRecordChange[] {
  if (changeSet.recordChanges?.length) return changeSet.recordChanges;
  const itemId = changeSet.itemId ?? store.itemId;
  return [{
    revisionId: changeSet.rootRevisionId,
    itemId: itemId ?? "",
    recordLabel: changeSet.recordLabel ?? null,
    action: changeSet.action,
    scalarChanges: changeSet.scalarChanges,
    relationChanges: changeSet.relationChanges,
  }];
}

function actionLabel(action: string): string {
  const normalized = action.toLocaleLowerCase();
  if (normalized.includes("create")) return t("history.action.create");
  if (normalized.includes("delete") || normalized.includes("archive")) return t("history.action.delete");
  if (normalized.includes("restore")) return t("history.action.restore");
  return t("history.action.update");
}

function actionType(action: string): "success" | "warning" | "error" | "info" {
  const normalized = action.toLocaleLowerCase();
  if (normalized.includes("create")) return "success";
  if (normalized.includes("delete") || normalized.includes("archive")) return "error";
  if (normalized.includes("restore")) return "info";
  return "warning";
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(date);
}

function formatValue(value: unknown): string {
  if (value === null) return t("history.null");
  if (value === undefined) return t("history.unavailableValue");
  if (typeof value === "string") return value || "\"\"";
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return t("history.unavailableValue");
    }
  }
  return String(value);
}

function relationBefore(change: HistoryRecordChange["relationChanges"][number]): string {
  return change.beforeDisplayValue ?? change.beforeItemId ?? t("history.null");
}

function relationAfter(change: HistoryRecordChange["relationChanges"][number]): string {
  if (change.targetAvailable === false) return t("history.unavailableValue");
  return change.afterDisplayValue ?? change.displayValue ?? change.afterItemId ?? t("history.null");
}

function submitSearch(): void {
  store.updateQuery({ search: searchDraft.value });
  emit("reload");
}

function updateActions(actions: string[]): void {
  store.updateQuery({ actions });
  emit("reload");
}

function updateField(field: string | null): void {
  store.updateQuery({ field: field ?? "" });
  emit("reload");
}

function updateDates(range: [number, number] | null): void {
  dateRange.value = range;
  store.updateQuery({
    dateFrom: range ? new Date(range[0]).toISOString() : "",
    dateTo: range ? new Date(range[1]).toISOString() : "",
  });
  emit("reload");
}

function submitAdvanced(): void {
  store.updateQuery({ actorId: actorDraft.value, recordId: recordDraft.value });
  emit("reload");
}

function clearFilters(): void {
  store.clearFilters();
  searchDraft.value = "";
  actorDraft.value = "";
  recordDraft.value = "";
  dateRange.value = null;
  emit("reload");
}

function requestPreview(record: HistoryRecordChange): void {
  if (!record.itemId) return;
  emit("preview", {
    itemId: record.itemId,
    revisionId: record.revisionId,
    field: store.scope === "cell" ? store.field : null,
  });
}

function isDefaultArchivedTarget(record: HistoryRecordChange): boolean {
  return archivedDefaultRevisionIds.value.has(record.revisionId);
}

function closeDrawer(): void {
  emit("close");
}
</script>

<template>
  <NDrawer
    :show="store.panelOpen"
    width="min(calc(100vw - 24px), clamp(360px, 34vw, 420px))"
    placement="right"
    :show-mask="false"
    :mask-closable="false"
    :trap-focus="false"
    :class="['revision-history-drawer', `density-${ui.density}`]"
    @update:show="(show) => { if (!show) closeDrawer(); }"
  >
    <NDrawerContent closable :native-scrollbar="false" @close="closeDrawer">
      <template #header>
        <div class="drawer-heading">
          <span class="drawer-heading-icon"><NIcon :size="17"><HistoryIcon /></NIcon></span>
          <div>
            <strong>{{ title }}</strong>
            <span data-testid="history-scope-label">{{ scopeLabel }}</span>
          </div>
        </div>
      </template>

      <div class="audit-tools">
        <NInput
          v-model:value="searchDraft"
          size="small"
          clearable
          :placeholder="t('history.search')"
          data-testid="history-search"
          @keyup.enter="submitSearch"
          @clear="submitSearch"
        >
          <template #prefix><NIcon :size="14"><Search /></NIcon></template>
        </NInput>
        <div class="filter-grid">
          <NSelect
            :value="store.query.actions"
            multiple
            clearable
            size="small"
            max-tag-count="responsive"
            :options="actionOptions"
            :placeholder="t('history.filter.action')"
            data-testid="history-action-filter"
            @update:value="updateActions"
          />
          <NSelect
            v-if="store.scope !== 'cell'"
            :value="store.query.field || null"
            clearable
            filterable
            size="small"
            :options="fieldSelectOptions"
            :placeholder="t('history.filter.field')"
            data-testid="history-field-filter"
            @update:value="updateField"
          />
          <NDatePicker
            :value="dateRange"
            type="datetimerange"
            clearable
            size="small"
            :placeholder="t('history.filter.date')"
            data-testid="history-date-filter"
            @update:value="updateDates"
          />
          <NInput
            v-model:value="actorDraft"
            size="small"
            clearable
            :placeholder="t('history.filter.actor')"
            data-testid="history-actor-filter"
            @keyup.enter="submitAdvanced"
          />
          <NInput
            v-if="store.scope === 'table' || store.scope === 'archived'"
            v-model:value="recordDraft"
            size="small"
            clearable
            :placeholder="t('history.filter.record')"
            data-testid="history-record-filter"
            @keyup.enter="submitAdvanced"
          />
        </div>
        <div class="tool-meta">
          <span>{{ t('history.resultCount', { count: store.total }) }}</span>
          <div>
            <NTooltip v-if="store.isFiltered">
              <template #trigger>
                <NButton quaternary size="tiny" :aria-label="t('history.filter.clear')" @click="clearFilters">
                  <template #icon><NIcon><FilterX /></NIcon></template>
                </NButton>
              </template>
              {{ t('history.filter.clear') }}
            </NTooltip>
            <NTooltip>
              <template #trigger>
                <NButton quaternary size="tiny" :aria-label="t('history.refresh')" data-testid="history-refresh" @click="emit('reload')">
                  <template #icon><NIcon><RefreshCw /></NIcon></template>
                </NButton>
              </template>
              {{ t('history.refresh') }}
            </NTooltip>
          </div>
        </div>
      </div>

      <div v-if="store.phase === 'loading'" class="state-block" data-testid="history-loading">
        <NSpin size="small" />
        <span>{{ t('history.loading') }}</span>
      </div>
      <div v-else-if="store.phase === 'unavailable'" class="state-block unavailable" data-testid="history-unavailable">
        <span class="state-icon"><NIcon :size="22"><ShieldAlert /></NIcon></span>
        <strong>{{ t('history.unavailable') }}</strong>
        <p>{{ store.lastError }}</p>
      </div>
      <div v-else-if="store.phase === 'failed'" class="state-block" data-testid="history-error">
        <span class="state-icon danger"><NIcon :size="22"><AlertTriangle /></NIcon></span>
        <strong>{{ t('history.failed') }}</strong>
        <p>{{ store.lastError }}</p>
        <NButton size="small" @click="emit('reload')">{{ t('history.retry') }}</NButton>
      </div>
      <NEmpty
        v-else-if="store.phase === 'empty'"
        class="history-empty"
        :description="store.isFiltered ? t('history.emptyFiltered') : t('history.empty')"
        data-testid="history-empty"
      >
        <template #icon><NIcon :size="26"><Activity /></NIcon></template>
        <template #extra>
          <span>{{ store.isFiltered ? t('history.emptyFilteredHint') : t('history.emptyHint') }}</span>
        </template>
      </NEmpty>

      <div v-else class="timeline" data-testid="history-timeline">
        <NCollapse :default-expanded-names="expandedByDefault" arrow-placement="right">
          <NCollapseItem
            v-for="(changeSet, index) in store.changeSets"
            :key="changeSetKey(changeSet, index)"
            :name="changeSetKey(changeSet, index)"
            class="timeline-entry"
          >
            <template #header>
              <div
                class="entry-header"
                :data-testid="`history-entry-${changeSet.rootRevisionId}`"
              >
                <span class="timeline-dot" :class="`is-${actionType(changeSet.action)}`"></span>
                <div class="entry-title">
                  <span>
                    <NTag size="small" :bordered="false" :type="actionType(changeSet.action)">{{ actionLabel(changeSet.action) }}</NTag>
                    <strong>{{ changeSet.recordLabel || recordsFor(changeSet)[0]?.recordLabel || t('history.unknownRecord') }}</strong>
                  </span>
                  <small :data-testid="`history-entry-meta-${changeSet.rootRevisionId}`">
                    <span class="meta-item">
                      <NIcon :size="12"><Clock3 /></NIcon>
                      {{ formatTimestamp(changeSet.timestamp) }}
                    </span>
                    <i></i>
                    <span class="meta-item">
                      <NIcon :size="12"><UserRound /></NIcon>
                      {{ changeSet.actor.displayName || t('history.unknownActor') }}
                    </span>
                    <template v-if="(changeSet.affectedRecords || recordsFor(changeSet).length) > 1">
                      <i></i>
                      <span class="affected-count">
                        {{ t('history.affectedRecords', { count: changeSet.affectedRecords || recordsFor(changeSet).length }) }}
                      </span>
                    </template>
                  </small>
                </div>
              </div>
            </template>

            <div class="record-list">
              <section v-for="record in recordsFor(changeSet)" :key="record.revisionId" class="record-change">
                <header>
                  <div class="record-identity">
                    <strong>{{ record.recordLabel || record.itemId || t('history.unknownRecord') }}</strong>
                    <span>{{ t('history.revision', { id: record.revisionId }) }}</span>
                  </div>
                  <div class="record-actions">
                    <NTag
                      v-if="isDefaultArchivedTarget(record)"
                      size="small"
                      :bordered="false"
                      type="success"
                      :data-testid="`history-default-${record.revisionId}`"
                    >{{ t('history.defaultArchivedVersion') }}</NTag>
                    <NButton
                      v-if="store.scope !== 'table' || record.itemId"
                      size="tiny"
                      tertiary
                      type="primary"
                      :disabled="!record.itemId"
                      :data-testid="`history-preview-${record.revisionId}`"
                      @click.stop="requestPreview(record)"
                    >
                      <template #icon><NIcon><RotateCcw /></NIcon></template>
                      {{ t('history.restore') }}
                    </NButton>
                  </div>
                </header>
                <div class="diff-table">
                  <div v-for="change in record.scalarChanges" :key="`s-${change.field}`" class="diff-row">
                    <code>{{ change.field }}</code>
                    <span class="diff-value before">{{ formatValue(change.before) }}</span>
                    <NIcon :size="13"><ArrowRight /></NIcon>
                    <span class="diff-value after">{{ formatValue(change.after) }}</span>
                  </div>
                  <div v-for="change in record.relationChanges" :key="`r-${change.field}`" class="diff-row relation">
                    <code>{{ change.field }}</code>
                    <span class="diff-value before">{{ relationBefore(change) }}</span>
                    <NIcon :size="13"><ArrowRight /></NIcon>
                    <span class="diff-value after">{{ relationAfter(change) }}</span>
                  </div>
                </div>
              </section>
            </div>
          </NCollapseItem>
        </NCollapse>

        <NButton
          v-if="store.hasMore"
          block
          secondary
          size="small"
          :loading="store.phase === 'loadingMore'"
          data-testid="history-load-more"
          @click="emit('loadMore')"
        >{{ store.phase === 'loadingMore' ? t('history.loadingMore') : t('history.loadMore') }}</NButton>
      </div>
    </NDrawerContent>
  </NDrawer>

  <NModal
    :show="previewOpen"
    preset="card"
    :title="t('history.restorePreview')"
    :style="{ width: 'min(580px, calc(100vw - 32px))' }"
    :mask-closable="false"
    class="restore-preview-modal"
    @update:show="(show) => { if (!show && store.restorePhase !== 'applying') store.clearPreview(); }"
  >
    <div v-if="store.restorePhase === 'previewing'" class="restore-loading">
      <NSpin size="small" />
      <span>{{ t('history.restoreLoading') }}</span>
    </div>
    <div v-else-if="store.restorePhase === 'failed'" data-testid="restore-error">
      <NAlert type="error" :title="t('history.restoreFailed')">{{ restoreFailureText }}</NAlert>
    </div>
    <div v-else-if="store.preview" class="restore-preview" data-testid="restore-preview">
      <p class="restore-hint">{{ t('history.restorePreviewHint') }}</p>
      <div class="preview-diffs">
        <div v-for="change in store.preview.scalarChanges" :key="change.field" class="diff-row">
          <code>{{ change.field }}</code>
          <span class="diff-value before">{{ formatValue(change.before) }}</span>
          <NIcon :size="13"><ArrowRight /></NIcon>
          <span class="diff-value after">{{ formatValue(change.after) }}</span>
        </div>
        <div v-for="change in store.preview.relationChanges" :key="change.field" class="diff-row relation">
          <code>{{ change.field }}</code>
          <span class="diff-value before">{{ relationBefore(change) }}</span>
          <NIcon :size="13"><ArrowRight /></NIcon>
          <span class="diff-value after">{{ relationAfter(change) }}</span>
        </div>
      </div>
      <div v-if="store.preview.diagnostics.length" class="diagnostics">
        <div v-for="diagnostic in store.preview.diagnostics" :key="`${diagnostic.field}-${diagnostic.code}`">
          <NTag size="small" :bordered="false" :type="diagnostic.severity === 'error' ? 'error' : 'warning'">
            {{ diagnostic.classification === 'recoverable' ? t('history.restorable') : t('history.skipped') }}
          </NTag>
          <code>{{ diagnostic.field }}</code>
          <span>{{ diagnosticText(diagnostic.code, diagnostic.message) }}</span>
        </div>
      </div>
      <small class="expiry">{{ t('history.expiresAt', { time: formatTimestamp(store.preview.expiresAt) }) }}</small>
    </div>
    <template #footer>
      <div class="modal-actions">
        <NButton :disabled="store.restorePhase === 'applying'" @click="store.clearPreview()">{{ t('history.restoreCancel') }}</NButton>
        <NButton
          type="primary"
          :loading="store.restorePhase === 'applying'"
          :disabled="!store.canApply"
          data-testid="restore-confirm"
          @click.stop="emit('apply')"
        >
          <template #icon><NIcon><ArchiveRestore /></NIcon></template>
          {{ store.restorePhase === 'applying' ? t('history.restoreApplying') : t('history.restoreConfirm') }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.drawer-heading { display: flex; align-items: center; gap: 10px; min-width: 0; }
.drawer-heading-icon { display: grid; place-items: center; width: 30px; height: 30px; flex: 0 0 30px; color: var(--vt-color-primary-500); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.drawer-heading > div { display: flex; min-width: 0; flex-direction: column; }
.drawer-heading strong { color: var(--vt-fg); font-size: var(--vt-font-label); font-weight: 600; }
.drawer-heading span:last-child { overflow: hidden; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); font-weight: 400; text-overflow: ellipsis; white-space: nowrap; }
.audit-tools { position: sticky; z-index: 2; top: 0; padding-bottom: 10px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-elevated); }
.filter-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; margin-top: 7px; }
.filter-grid > :deep(.n-date-picker) { grid-column: 1 / -1; }
.tool-meta { display: flex; align-items: center; justify-content: space-between; min-height: 26px; margin-top: 5px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); font-variant-numeric: tabular-nums; }
.tool-meta > div { display: flex; }
.state-block { display: flex; min-height: 260px; flex-direction: column; align-items: center; justify-content: center; gap: 8px; color: var(--vt-fg-muted); text-align: center; }
.state-block strong { color: var(--vt-fg); font-weight: 600; }
.state-block p { max-width: 310px; margin: 0 0 6px; }
.state-icon { display: grid; place-items: center; width: 42px; height: 42px; color: var(--vt-color-warning); border-radius: 50%; background: color-mix(in srgb, var(--vt-color-warning) 12%, transparent); }
.state-icon.danger { color: var(--vt-color-danger); background: color-mix(in srgb, var(--vt-color-danger) 10%, transparent); }
.history-empty { padding-top: 92px; }
.history-empty :deep(.n-empty__extra) { max-width: 280px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: center; }
.timeline { position: relative; padding: 6px 0 20px 12px; }
.timeline::before { position: absolute; top: 24px; bottom: 24px; left: 17px; width: 1px; background: var(--vt-border); content: ""; }
.timeline :deep(.n-collapse-item__header) { padding: 8px 0 !important; }
.timeline :deep(.n-collapse-item__header-main) { min-width: 0; }
.timeline :deep(.n-collapse-item__content-inner) { padding: 0 0 6px 20px; }
.entry-header { display: flex; align-items: center; width: 100%; min-width: 0; gap: 9px; }
.timeline-dot { z-index: 1; width: 11px; height: 11px; flex: 0 0 11px; border: 2px solid var(--vt-bg-elevated); border-radius: 50%; box-shadow: 0 0 0 1px var(--vt-border); background: var(--vt-gray-400); }
.timeline-dot.is-success { background: var(--vt-color-success); }
.timeline-dot.is-warning { background: var(--vt-color-warning); }
.timeline-dot.is-error { background: var(--vt-color-danger); }
.timeline-dot.is-info { background: var(--vt-color-info); }
.entry-title { display: flex; flex: 1 1 auto; min-width: 0; flex-direction: column; gap: 3px; }
.entry-title > span { display: flex; align-items: center; min-width: 0; gap: 7px; }
.entry-title strong { overflow: hidden; font-size: var(--vt-font-body); font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.entry-title small { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: 4px; color: var(--vt-fg-muted); font-size: 11px; font-weight: 400; font-variant-numeric: tabular-nums; line-height: 16px; }
.entry-title small i { width: 1px; height: 10px; flex: 0 0 1px; margin: 0 2px; background: var(--vt-border); }
.meta-item { display: inline-flex; min-width: 0; align-items: center; gap: 3px; }
.affected-count { color: var(--vt-fg-muted); font-size: 11px; white-space: nowrap; }
.record-list { display: flex; flex-direction: column; gap: 8px; }
.record-change { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.record-change > header { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 38px; padding: 5px 8px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-elevated); }
.record-change > header > .record-identity { display: flex; min-width: 0; flex-direction: column; }
.record-change > header strong { overflow: hidden; font-size: var(--vt-font-caption); font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.record-change > header span { color: var(--vt-fg-muted); font-size: 10px; font-variant-numeric: tabular-nums; }
.record-actions { display: flex; align-items: center; gap: 5px; }
.diff-table, .preview-diffs { display: flex; flex-direction: column; }
.diff-row { display: grid; grid-template-columns: minmax(68px, .65fr) minmax(0, 1fr) 15px minmax(0, 1fr); align-items: center; min-height: 33px; gap: 5px; padding: 5px 8px; border-bottom: 1px solid var(--vt-border); }
.diff-row:last-child { border-bottom: 0; }
.diff-row > code { overflow: hidden; color: var(--vt-fg-secondary); font-family: Consolas, "SFMono-Regular", monospace; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.diff-row > :deep(.n-icon) { color: var(--vt-fg-muted); }
.diff-value { overflow: hidden; padding: 2px 5px; border-radius: var(--vt-radius-sm); font-size: 11px; line-height: 18px; text-overflow: ellipsis; white-space: nowrap; }
.diff-value.before { color: var(--vt-fg-muted); background: var(--vt-bg-sunken); text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--vt-color-danger) 55%, transparent); }
.diff-value.after { color: var(--vt-fg); background: color-mix(in srgb, var(--vt-color-success) 10%, var(--vt-bg)); }
.restore-loading { display: flex; min-height: 180px; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--vt-fg-muted); }
.restore-hint { margin: 0 0 10px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.preview-diffs { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.diagnostics { display: flex; flex-direction: column; gap: 5px; margin-top: 10px; }
.diagnostics > div { display: grid; grid-template-columns: auto minmax(60px, auto) minmax(0, 1fr); align-items: center; gap: 7px; color: var(--vt-fg-secondary); font-size: var(--vt-font-caption); }
.diagnostics code { color: var(--vt-fg); font-family: Consolas, "SFMono-Regular", monospace; }
.expiry { display: block; margin-top: 10px; color: var(--vt-fg-muted); font-variant-numeric: tabular-nums; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 420px) {
  .filter-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .filter-grid > :deep(*) {
    grid-column: 1 / -1;
  }
  .tool-meta {
    align-items: flex-start;
  }
  .entry-title small {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1px;
  }
  .entry-title small i {
    display: none;
  }
  .meta-item,
  .affected-count {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta-item :deep(.n-icon) {
    display: none;
  }
}
</style>

<style>
.revision-history-drawer .n-drawer { border-left: 1px solid var(--vt-border); background: var(--vt-bg-elevated); box-shadow: var(--vt-shadow-3); }
.revision-history-drawer .n-drawer-header { min-height: 54px; padding: 10px 14px; border-bottom: 1px solid var(--vt-border); }
.revision-history-drawer .n-drawer-body-content-wrapper { padding: 10px 12px 0; }
.revision-history-drawer.density-compact .n-drawer-header { min-height: 48px; padding-block: 7px; }
.revision-history-drawer.density-compact .n-drawer-body-content-wrapper { padding-top: 7px; }
.revision-history-drawer.density-compact .n-collapse-item__header { padding-block: 5px !important; }
.restore-preview-modal.n-card { border: 1px solid var(--vt-border); background: var(--vt-bg-elevated); box-shadow: var(--vt-shadow-3); }
</style>
