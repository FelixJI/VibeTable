<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NIcon,
  NTag,
} from "naive-ui";
import { AlertTriangle, FileSpreadsheet, ShieldCheck, X } from "@lucide/vue";
import type { ImportCellDiagnostic, ImportPlanRow } from "@/contracts";
import type { ImportPreviewSession } from "@/services/dataIoService";
import { getLocale, t } from "@/i18n";

const props = defineProps<{
  session: ImportPreviewSession;
  applying: boolean;
  cancellable: boolean;
  cancelling: boolean;
  error: string | null;
}>();

const emit = defineEmits<{
  confirm: [];
  cancel: [];
  cancelTask: [];
}>();

const acknowledged = ref(false);

watch(() => props.session.plan.token.token, () => {
  acknowledged.value = false;
});

const diagnostics = computed(() => [
  ...props.session.plan.diagnostics,
  ...props.session.plan.rows.flatMap((row) => row.diagnostics),
]);

const previewRows = computed(() => props.session.plan.rows.slice(0, 5));
const previewFields = computed(() => {
  const fields = new Set<string>();
  for (const row of previewRows.value) {
    for (const field of Object.keys(row.values)) fields.add(field);
  }
  return [...fields].slice(0, 6);
});

const requiresAcknowledgement = computed(() =>
  props.session.plan.summary.warningCount > 0
  || props.session.plan.unmatchedColumns.length > 0,
);

const canConfirm = computed(() =>
  !props.applying
  && props.session.plan.summary.validRows > 0
  && props.session.plan.summary.errorRows === 0
  && (!requiresAcknowledgement.value || acknowledged.value),
);

function formatBytes(value: number | null): string {
  if (value === null) return t("dataIo.import.preview.sizeUnknown");
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB"];
  let amount = value / 1024;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 }).format(amount)} ${units[unit]}`;
}

function diagnosticLocation(item: ImportCellDiagnostic): string {
  const cell = t("dataIo.import.preview.location", {
    row: item.row,
    column: item.column,
  });
  return item.sheet ? `${item.sheet} · ${cell}` : cell;
}

function displayValue(row: ImportPlanRow, field: string): string {
  const value = row.values[field];
  if (value === null) return "null";
  if (value === undefined) return "—";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
</script>

<template>
  <div class="import-preview-shell">
    <NCard
      class="import-preview-panel"
      size="small"
      role="dialog"
      aria-modal="true"
      :aria-label="t('dataIo.import.preview.title')"
      data-testid="import-preview-panel"
    >
      <template #header>
        <div class="panel-title">
          <span class="file-mark"><NIcon :size="21"><FileSpreadsheet /></NIcon></span>
          <span>
            <strong>{{ t("dataIo.import.preview.title") }}</strong>
            <small>{{ session.grant.displayName }} · {{ formatBytes(session.grant.sizeBytes) }}</small>
          </span>
        </div>
      </template>
      <template #header-extra>
        <NButton
          quaternary
          circle
          size="small"
          :disabled="applying"
          :aria-label="t('dataIo.import.preview.cancel')"
          @click="emit('cancel')"
        >
          <template #icon><NIcon><X /></NIcon></template>
        </NButton>
      </template>

      <div class="preview-scroll">
        <section class="summary-grid" :aria-label="t('dataIo.import.preview.summary')">
          <div><span>{{ t("dataIo.import.preview.total") }}</span><strong>{{ session.plan.summary.totalRows }}</strong></div>
          <div class="valid"><span>{{ t("dataIo.import.preview.valid") }}</span><strong>{{ session.plan.summary.validRows }}</strong></div>
          <div :class="{ danger: session.plan.summary.errorRows > 0 }"><span>{{ t("dataIo.import.preview.errors") }}</span><strong>{{ session.plan.summary.errorRows }}</strong></div>
          <div :class="{ warning: session.plan.summary.warningRows > 0 }"><span>{{ t("dataIo.import.preview.warnings") }}</span><strong>{{ session.plan.summary.warningRows }}</strong></div>
        </section>

        <NAlert type="info" :show-icon="false" class="atomic-note">
          <span class="alert-line">
            <NIcon :size="17"><ShieldCheck /></NIcon>
            {{ t("dataIo.import.preview.atomicHint") }}
          </span>
        </NAlert>

        <NAlert
          v-if="session.plan.unmatchedColumns.length"
          type="warning"
          :title="t('dataIo.import.preview.unmatchedTitle')"
          class="unmatched-alert"
        >
          <p>{{ t("dataIo.import.preview.unmatchedHint") }}</p>
          <div class="tag-list">
            <NTag v-for="column in session.plan.unmatchedColumns" :key="column" size="small" type="warning">
              {{ column }}
            </NTag>
          </div>
        </NAlert>

        <section v-if="diagnostics.length" class="diagnostic-section" data-testid="import-diagnostics">
          <header>
            <span><NIcon :size="17"><AlertTriangle /></NIcon>{{ t("dataIo.import.preview.diagnostics") }}</span>
            <small>{{ diagnostics.length }}</small>
          </header>
          <div class="diagnostic-list">
            <article v-for="(item, index) in diagnostics" :key="`${item.row}-${item.column}-${item.code}-${index}`">
              <NTag :type="item.severity === 'error' ? 'error' : 'warning'" size="small">
                {{ item.severity === "error" ? t("dataIo.import.preview.error") : t("dataIo.import.preview.warning") }}
              </NTag>
              <div>
                <strong>{{ diagnosticLocation(item) }}</strong>
                <p>{{ item.message }}</p>
                <code v-if="item.originalValue">{{ item.originalValue }}</code>
              </div>
            </article>
          </div>
        </section>

        <section v-if="previewRows.length && previewFields.length" class="sample-section">
          <header>
            <strong>{{ t("dataIo.import.preview.sampleTitle") }}</strong>
            <small>{{ t("dataIo.import.preview.sampleHint") }}</small>
          </header>
          <div class="sample-table-wrap">
            <table>
              <thead><tr><th>{{ t("dataIo.import.preview.sourceRow") }}</th><th v-for="field in previewFields" :key="field">{{ field }}</th></tr></thead>
              <tbody>
                <tr v-for="row in previewRows" :key="row.sourceRow">
                  <th>{{ row.sourceRow }}</th>
                  <td v-for="field in previewFields" :key="field" :title="displayValue(row, field)">{{ displayValue(row, field) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <NAlert v-if="error" type="error" :show-icon="true" data-testid="import-apply-error">
          {{ error }}
        </NAlert>

        <NCheckbox
          v-if="requiresAcknowledgement"
          v-model:checked="acknowledged"
          data-testid="import-ack"
        >
          {{ t("dataIo.import.preview.ack") }}
        </NCheckbox>
      </div>

      <template #action>
        <div class="panel-actions">
          <span>{{ t("dataIo.import.preview.createOnly") }}</span>
          <div>
            <NButton
              data-testid="import-cancel"
              :loading="cancelling"
              :disabled="cancelling || (applying && !cancellable)"
              @click="applying ? emit('cancelTask') : emit('cancel')"
            >
              {{ applying ? t("dataIo.import.preview.cancelTask") : t("dataIo.import.preview.cancel") }}
            </NButton>
            <NButton
              type="primary"
              :loading="applying"
              :disabled="!canConfirm"
              data-testid="import-confirm"
              @click="emit('confirm')"
            >
              {{ t("dataIo.import.preview.confirm", { count: session.plan.summary.validRows }) }}
            </NButton>
          </div>
        </div>
      </template>
    </NCard>
  </div>
</template>

<style scoped>
.import-preview-shell {
  position: fixed;
  inset: 0;
  z-index: 45;
  display: grid;
  place-items: center;
  padding: var(--vt-space-4);
  background: color-mix(in srgb, var(--vt-bg-sunken) 64%, transparent);
  backdrop-filter: blur(2px);
}
.import-preview-panel { width: min(820px, calc(100vw - 32px)); max-height: min(760px, calc(100vh - 32px)); box-shadow: var(--vt-shadow-3); }
.panel-title { display: flex; align-items: center; gap: var(--vt-space-3); }
.panel-title > span:last-child { display: grid; gap: 2px; }
.panel-title small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); font-weight: 400; }
.file-mark { display: grid; width: 36px; height: 36px; place-items: center; color: var(--vt-color-primary-600); border: 1px solid var(--vt-color-primary-200); border-radius: var(--vt-radius-md); background: var(--vt-color-primary-50); }
.preview-scroll { display: grid; max-height: calc(100vh - 220px); gap: var(--vt-space-3); overflow-y: auto; padding-right: 2px; }
.summary-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1px; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-border); }
.summary-grid > div { display: grid; gap: 4px; padding: 12px 14px; background: var(--vt-bg); }
.summary-grid span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.summary-grid strong { font-size: 22px; line-height: 1; font-variant-numeric: tabular-nums; }
.summary-grid .valid strong { color: var(--vt-color-success); }
.summary-grid .danger strong { color: var(--vt-color-danger); }
.summary-grid .warning strong { color: var(--vt-color-warning); }
.alert-line { display: inline-flex; align-items: center; gap: 8px; line-height: 1.55; }
.unmatched-alert p { margin: 0 0 8px; }
.tag-list { display: flex; flex-wrap: wrap; gap: 6px; }
.diagnostic-section, .sample-section { display: grid; gap: 8px; }
.diagnostic-section > header, .sample-section > header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.diagnostic-section > header span { display: inline-flex; align-items: center; gap: 7px; font-weight: 600; }
.diagnostic-section header small, .sample-section header small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.diagnostic-list { display: grid; max-height: 180px; overflow-y: auto; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
.diagnostic-list article { display: grid; grid-template-columns: auto 1fr; gap: 10px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
.diagnostic-list article:last-child { border-bottom: 0; }
.diagnostic-list strong { font-size: var(--vt-font-caption); }
.diagnostic-list p { margin: 2px 0 0; color: var(--vt-fg-secondary); }
.diagnostic-list code { display: inline-block; max-width: 100%; margin-top: 5px; overflow: hidden; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-overflow: ellipsis; white-space: nowrap; }
.sample-table-wrap { overflow-x: auto; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
table { width: 100%; border-collapse: collapse; font-size: var(--vt-font-caption); }
th, td { max-width: 220px; padding: 8px 10px; overflow: hidden; border-right: 1px solid var(--vt-border); border-bottom: 1px solid var(--vt-border); text-align: left; text-overflow: ellipsis; white-space: nowrap; }
thead th { color: var(--vt-fg-muted); background: var(--vt-bg-subtle); font-weight: 600; }
tbody th { color: var(--vt-fg-muted); font-variant-numeric: tabular-nums; }
tr:last-child > * { border-bottom: 0; }
tr > *:last-child { border-right: 0; }
.panel-actions { display: flex; align-items: center; justify-content: space-between; gap: var(--vt-space-3); }
.panel-actions > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.panel-actions > div { display: flex; gap: var(--vt-space-2); }
@media (max-width: 640px) {
  .summary-grid { grid-template-columns: repeat(2, 1fr); }
  .panel-actions { align-items: stretch; flex-direction: column; }
  .panel-actions > div { justify-content: flex-end; }
}
</style>
