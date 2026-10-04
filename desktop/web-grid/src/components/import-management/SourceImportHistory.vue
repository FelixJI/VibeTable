<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NIcon, NTag } from "naive-ui";
import { ChevronDown, CloudUpload, Table2 } from "@lucide/vue";
import type { SourceImportEntry, SourceImportState } from "@/contracts/importManagement";
import { getLocale, t } from "@/i18n";

/**
 * Pure-presentation source-migration history (#435). It renders the durable
 * Go receipts projected by importManagementService: strict committed /
 * not-submitted / pending-confirmation splits, honest window-read limits,
 * snapshot/skip degradation and unknown-outcome guidance. It never renders
 * raw source definitions, identities, tokens or URLs, and diagnostics use a
 * fixed localized code map with a safe generic fallback.
 */
const props = defineProps<{
  entries: readonly SourceImportEntry[];
  cancellingJobId: string | null;
}>();

const emit = defineEmits<{
  cancelTask: [jobId: string];
  openTarget: [tableId: string];
}>();

const stateLabels: Record<SourceImportState, { label: string; kind: "default" | "info" | "success" | "warning" | "error" }> = {
  queued: { label: t("importManagement.state.queued"), kind: "default" },
  running: { label: t("importManagement.state.running"), kind: "info" },
  succeeded: { label: t("importManagement.state.succeeded"), kind: "success" },
  failed: { label: t("importManagement.state.failed"), kind: "error" },
  cancelled: { label: t("importManagement.state.cancelled"), kind: "warning" },
  interrupted: { label: t("importManagement.state.interrupted"), kind: "warning" },
  aborted: { label: t("importManagement.state.aborted"), kind: "default" },
  unknown: { label: t("importManagement.migration.state.unknown"), kind: "warning" },
};

const STAGE_LABELS: Record<string, string> = {
  preparing: t("importManagement.migration.stage.preparing"),
  schema: t("importManagement.migration.stage.schema"),
  constraints: t("importManagement.migration.stage.constraints"),
  records: t("importManagement.migration.stage.records"),
  relations: t("importManagement.migration.stage.relations"),
  attachments: t("importManagement.migration.stage.attachments"),
  settled: t("importManagement.migration.stage.settled"),
};

function stateOf(entry: SourceImportEntry) {
  return stateLabels[entry.state];
}

function stageLabel(stage: string): string {
  return STAGE_LABELS[stage] ?? stage;
}

/** Completion is a terminal receipt fact; an unfinished stage never shows it. */
function isCancellable(entry: SourceImportEntry): boolean {
  // Host overlays queued/running only for the current job; anything else is a
  // foreign or terminal task this page must not pretend to own.
  return entry.state === "queued" || entry.state === "running";
}

/** Tables whose schema batch committed; the unknown batch is excluded. */
function committedTableIds(entry: SourceImportEntry): Set<string> {
  const committed = new Set<string>();
  for (const batch of entry.batches) {
    // Only the unknown batch itself is excluded: an earlier committed schema
    // batch keeps its target openable even when later record batches are
    // pending confirmation.
    if (batch.tableId !== "" && batch.batchId !== entry.unknownBatch) committed.add(batch.tableId);
  }
  return committed;
}

function committedTargets(entry: SourceImportEntry) {
  const committed = committedTableIds(entry);
  return entry.targets.filter((target) => committed.has(target.tableId));
}

/** Fixed localized diagnostic copy; unknown codes get a safe generic line. */
function diagnosticLabel(code: string): string {
  const mapped = t(`importManagement.migration.diagnostic.${code}`);
  return mapped === `importManagement.migration.diagnostic.${code}`
    ? t("importManagement.migration.diagnostic.generic", { code })
    : mapped;
}

function formatTime(value: string | null): string {
  if (!value) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? value
    : new Intl.DateTimeFormat(getLocale(), { dateStyle: "short", timeStyle: "medium" }).format(parsed);
}

const sortedEntries = computed(() =>
  [...props.entries].sort((left, right) => right.startedAt.localeCompare(left.startedAt)));

/** Presentation-only expanded-detail state; services own no part of it. */
const expandedJobIds = ref(new Set<string>());

function isExpanded(jobId: string): boolean {
  return expandedJobIds.value.has(jobId);
}

function toggleExpanded(jobId: string): void {
  const next = new Set(expandedJobIds.value);
  if (next.has(jobId)) next.delete(jobId);
  else next.add(jobId);
  expandedJobIds.value = next;
}
</script>

<template>
  <section class="source-import-history" data-testid="source-import-history" aria-labelledby="source-import-history-title">
    <h3 id="source-import-history-title">{{ t("importManagement.migration.title") }}</h3>
    <p class="source-import-history-hint">{{ t("importManagement.migration.subtitle") }}</p>
    <ol v-if="sortedEntries.length > 0" class="source-import-list" data-testid="source-import-list">
      <li
        v-for="entry in sortedEntries"
        :key="entry.jobId"
        class="source-import-item"
        data-testid="source-import-row"
        :data-job-id="entry.jobId"
        :data-state="entry.state"
        :data-stage="entry.stage"
      >
        <div class="source-import-main">
          <div class="source-import-title">
            <span class="source-import-source"><NIcon :size="14"><CloudUpload /></NIcon></span>
            <strong :title="entry.jobId">{{ entry.sourceName }}</strong>
            <NTag size="small" :bordered="false">{{ entry.provider }}</NTag>
            <NTag size="small" :type="stateOf(entry).kind" :bordered="false">{{ stateOf(entry).label }}</NTag>
            <NTag
              v-if="entry.state !== 'succeeded'"
              size="small"
              type="info"
              :bordered="false"
            >{{ t("importManagement.migration.stage.current", { stage: stageLabel(entry.stage) }) }}</NTag>
          </div>
          <!-- Strict split: created + notSubmitted + unknownRecords === total. -->
          <p class="source-import-counts" data-testid="source-counts">
            <span data-testid="source-count-created">{{ t("importManagement.migration.count.created", { count: entry.created }) }}</span>
            <span data-testid="source-count-not-submitted">{{ t("importManagement.migration.count.notSubmitted", { count: entry.notSubmitted }) }}</span>
            <span data-testid="source-count-unknown">{{ t("importManagement.migration.count.unknown", { count: entry.unknownRecords }) }}</span>
            <span class="source-import-count-total" data-testid="source-count-total">{{ t("importManagement.migration.count.total", { count: entry.total }) }}</span>
          </p>
          <ul class="source-import-hints">
            <li v-if="entry.readWindow?.consistency === 'window'" data-testid="source-hint-window">
              {{ t("importManagement.migration.hint.window") }}
            </li>
            <li v-if="entry.snapshotFieldCount > 0 || entry.skippedFieldCount > 0" data-testid="source-hint-snapshot">
              {{ t("importManagement.migration.hint.snapshot", { snapshot: entry.snapshotFieldCount, skipped: entry.skippedFieldCount }) }}
            </li>
            <li v-if="entry.unknownBatch" data-testid="source-hint-unknown-batch">
              {{ t("importManagement.migration.hint.unknownBatch", { batchId: entry.unknownBatch }) }}
            </li>
            <li v-if="entry.state === 'failed'" data-testid="source-hint-failed">
              {{ t("importManagement.migration.hint.noRollback") }}
            </li>
            <li v-if="entry.state === 'unknown' || entry.state === 'interrupted'" data-testid="source-hint-unknown">
              {{ t("importManagement.migration.hint.unknownOutcome") }}
            </li>
          </ul>
          <template v-if="isExpanded(entry.jobId)">
            <dl class="source-import-detail">
              <div>
                <dt>{{ t("importManagement.migration.targets.title") }}</dt>
                <dd>
                  <span
                    v-for="target in committedTargets(entry)"
                    :key="target.tableId"
                    class="source-import-target"
                  >
                    <button
                      type="button"
                      class="source-import-target-button"
                      data-testid="source-target-open"
                      :data-table-id="target.tableId"
                      :data-collection="target.collection ?? target.tableId"
                      @click="emit('openTarget', target.collection ?? target.tableId)"
                    >
                      <NIcon :size="13"><Table2 /></NIcon>
                      <span>{{ target.name }}</span>
                    </button>
                  </span>
                  <span v-if="committedTargets(entry).length === 0" class="source-import-target-empty">
                    {{ t("importManagement.migration.targets.none") }}
                  </span>
                </dd>
              </div>
              <div v-if="entry.batches.length > 0">
                <dt>{{ t("importManagement.migration.batches.title") }}</dt>
                <dd>
                  <ul class="source-import-batches">
                    <li
                      v-for="batch in entry.batches"
                      :key="batch.batchId"
                      data-testid="source-batch"
                      :data-stage="batch.stage"
                    >
                      <strong>{{ stageLabel(batch.stage) }}</strong>
                      <span>{{ t("importManagement.migration.batches.counts", {
                        created: batch.created,
                        relations: batch.relationWrites,
                        attachments: batch.attachmentWrites,
                      }) }}</span>
                      <NTag v-if="batch.batchId === entry.unknownBatch" size="tiny" type="warning" :bordered="false">
                        {{ t("importManagement.migration.batches.unknown") }}
                      </NTag>
                    </li>
                  </ul>
                </dd>
              </div>
              <div v-if="entry.diagnostics.length > 0">
                <dt>{{ t("importManagement.migration.diagnostics.title", { count: entry.diagnostics.length }) }}</dt>
                <dd data-testid="source-diagnostics">
                  <span
                    v-for="diagnostic in entry.diagnostics"
                    :key="diagnostic.code"
                    class="source-import-diagnostic"
                    data-testid="source-diagnostic"
                    :data-code="diagnostic.code"
                  >{{ diagnosticLabel(diagnostic.code) }}</span>
                </dd>
              </div>
              <div><dt>{{ t("importManagement.entry.startedAt") }}</dt><dd>{{ formatTime(entry.startedAt) }}</dd></div>
              <div><dt>{{ t("importManagement.entry.finishedAt") }}</dt><dd>{{ formatTime(entry.finishedAt) }}</dd></div>
            </dl>
          </template>
        </div>
        <div class="source-import-actions">
          <NButton
            v-if="isCancellable(entry)"
            size="tiny"
            type="warning"
            secondary
            :loading="props.cancellingJobId === entry.jobId"
            data-testid="source-import-cancel"
            @click="emit('cancelTask', entry.jobId)"
          >
            {{ t("importManagement.entry.cancel") }}
          </NButton>
          <NButton
            size="tiny"
            tertiary
            :data-testid="`source-import-detail-${entry.jobId}`"
            :aria-expanded="isExpanded(entry.jobId)"
            @click="toggleExpanded(entry.jobId)"
          >
            <template #icon>
              <NIcon :size="13" :class="{ 'source-import-chevron--open': isExpanded(entry.jobId) }"><ChevronDown /></NIcon>
            </template>
            {{ t("importManagement.entry.detail") }}
          </NButton>
        </div>
      </li>
    </ol>
    <p v-else class="source-import-empty">{{ t("importManagement.migration.empty") }}</p>
  </section>
</template>

<style scoped>
.source-import-history { display: grid; gap: 10px; }
.source-import-history h3 {
  margin: 0;
  font-size: var(--vt-font-caption);
  font-weight: 650;
  letter-spacing: 0.02em;
  color: var(--vt-fg-muted);
  text-transform: uppercase;
}
.source-import-history-hint { margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.source-import-list { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.source-import-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 12px 14px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
}
.source-import-main { display: grid; flex: 1 1 auto; gap: 8px; min-width: 0; }
.source-import-title { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.source-import-source {
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  color: var(--vt-color-primary-500);
  border-radius: var(--vt-radius-md);
  background: var(--vt-color-primary-50);
}
.source-import-title strong {
  overflow: hidden;
  max-width: 300px;
  color: var(--vt-fg);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.source-import-counts {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin: 0;
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
  font-variant-numeric: tabular-nums;
}
.source-import-count-total { margin-left: auto; color: var(--vt-fg-muted); }
.source-import-hints {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.source-import-hints li {
  padding: 5px 10px;
  border-left: 2px solid var(--vt-color-warning);
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  background: var(--vt-bg-subtle);
  border-radius: 0 var(--vt-radius-md) var(--vt-radius-md) 0;
}
.source-import-detail { display: grid; gap: 8px; margin: 0; }
.source-import-detail > div { display: grid; gap: 4px; }
.source-import-detail dt { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.source-import-detail dd { margin: 0; color: var(--vt-fg); font-size: var(--vt-font-caption); }
.source-import-target { margin-right: 6px; }
.source-import-target-button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg);
  color: inherit;
  font-size: var(--vt-font-caption);
  cursor: pointer;
}
.source-import-target-button:hover { border-color: var(--vt-color-primary-300); }
.source-import-target-button span { font-weight: 600; }
.source-import-target-empty { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.source-import-batches { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.source-import-batches li {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--vt-fg);
  font-size: var(--vt-font-caption);
  font-variant-numeric: tabular-nums;
}
.source-import-batches strong { font-weight: 600; }
.source-import-diagnostic {
  display: inline-block;
  margin: 0 6px 4px 0;
  padding: 2px 8px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.source-import-actions { display: flex; flex: 0 0 auto; flex-direction: column; align-items: flex-end; gap: 6px; }
.source-import-chevron--open { transform: rotate(180deg); }
.source-import-empty { margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
</style>
