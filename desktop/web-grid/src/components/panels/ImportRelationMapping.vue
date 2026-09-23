<script setup lang="ts">
import { computed } from "vue";
import { NAlert, NButton, NIcon, NSelect } from "naive-ui";
import { Link2, Plus, X } from "@lucide/vue";
import type { RelationImportOption } from "@/services/dataIoService";
import type { RelationMappingDraft } from "@/composables/useDataIoTask";
import { t } from "@/i18n";

const props = defineProps<{
  sourceColumns: readonly string[];
  options: readonly RelationImportOption[] | null;
  loading: boolean;
  error: string | null;
  modelValue: readonly RelationMappingDraft[];
  disabled: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [rows: readonly RelationMappingDraft[]];
}>();

interface SourceColumnOption {
  readonly label: string;
  readonly value: string;
  readonly disabled: boolean;
  readonly reason: string | null;
}

/**
 * Explicit mappings are keyed by the trimmed header, so duplicated or empty
 * headers cannot form an unambiguous mapping; they stay visible but disabled.
 */
const sourceOptions = computed<readonly SourceColumnOption[]>(() => {
  const counts = new Map<string, number>();
  for (const column of props.sourceColumns ?? []) {
    const key = column.trim();
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  const seen = new Set<string>();
  const result: SourceColumnOption[] = [];
  for (const column of props.sourceColumns ?? []) {
    const key = column.trim();
    if (seen.has(key)) continue;
    seen.add(key);
    const empty = key === "";
    const duplicate = (counts.get(key) ?? 0) > 1;
    const tooLong = [...key].length > 128;
    result.push({
      label: empty || duplicate || tooLong
        ? `${empty ? t("dataIo.import.mapping.emptySource") : column} · ${
          duplicate ? t("dataIo.import.mapping.duplicateSource")
            : tooLong ? t("dataIo.import.mapping.sourceTooLong") : t("dataIo.import.mapping.emptySource")
        }`
        : column,
      value: key,
      disabled: empty || duplicate || tooLong,
      reason: empty
        ? t("dataIo.import.mapping.emptySource")
        : duplicate ? t("dataIo.import.mapping.duplicateSource")
          : tooLong ? t("dataIo.import.mapping.sourceTooLong") : null,
    });
  }
  return result;
});

const unavailableSources = computed(() =>
  sourceOptions.value.filter((option) => option.disabled));

const configurableTargets = computed(() =>
  (props.options ?? []).filter((option) => option.matchFields.length > 0));

function usedInRows(rows: readonly RelationMappingDraft[], key: "sourceColumn" | "relationId"): Set<string> {
  return new Set(rows.map((row) => row[key]));
}

function targetOptions(rows: readonly RelationMappingDraft[]) {
  const used = usedInRows(rows, "relationId");
  return (props.options ?? []).map((option) => ({
    label: option.matchFields.length === 0
      ? `${option.sourceDisplayName} · ${t("dataIo.import.mapping.noMatchFields")}`
      : `${option.sourceDisplayName}（${option.targetDisplayName}）`,
    value: option.relationId,
    disabled: props.disabled || option.matchFields.length === 0 || used.has(option.relationId),
  }));
}

function rowSourceOptions(rows: readonly RelationMappingDraft[]) {
  const used = usedInRows(rows, "sourceColumn");
  return sourceOptions.value.map((option) => ({
    ...option,
    disabled: option.disabled || props.disabled || used.has(option.value),
  }));
}

function matchFieldOptions(relationId: string) {
  const option = (props.options ?? []).find((item) => item.relationId === relationId);
  return (option?.matchFields ?? []).map((field) => ({
    label: field.displayName,
    value: field.fieldId,
    disabled: props.disabled,
  }));
}

function updateRow(index: number, patch: Partial<RelationMappingDraft>): void {
  if (props.disabled) return;
  emit("update:modelValue", props.modelValue.map((row, position) =>
    position === index ? { ...row, ...patch } : row));
}

function onTargetChange(index: number, relationId: string): void {
  updateRow(index, { relationId, matchField: "" });
}

function removeRow(index: number): void {
  if (props.disabled) return;
  emit("update:modelValue", props.modelValue.filter((_, position) => position !== index));
}

const canAddRow = computed(() => {
  if (props.disabled || props.options === null) return false;
  const usedSources = usedInRows(props.modelValue, "sourceColumn");
  const usedTargets = usedInRows(props.modelValue, "relationId");
  const freeSource = sourceOptions.value.some((option) =>
    !option.disabled && !usedSources.has(option.value));
  const freeTarget = configurableTargets.value.some((option) =>
    !usedTargets.has(option.relationId));
  return freeSource && freeTarget && props.modelValue.length < 256;
});

function addRow(): void {
  if (!canAddRow.value) return;
  const usedSources = usedInRows(props.modelValue, "sourceColumn");
  const usedTargets = usedInRows(props.modelValue, "relationId");
  const source = sourceOptions.value.find((option) =>
    !option.disabled && !usedSources.has(option.value));
  const target = configurableTargets.value.find((option) =>
    !usedTargets.has(option.relationId));
  if (!source || !target) return;
  emit("update:modelValue", [...props.modelValue, {
    sourceColumn: source.value,
    relationId: target.relationId,
    matchField: target.matchFields[0].fieldId,
  }]);
}
</script>

<template>
  <section class="relation-mapping" data-testid="relation-mapping-section" :aria-label="t('dataIo.import.mapping.title')">
    <header>
      <span><NIcon :size="17"><Link2 /></NIcon>{{ t("dataIo.import.mapping.title") }}</span>
      <small>{{ t("dataIo.import.mapping.hint") }}</small>
    </header>

    <p v-if="loading" class="mapping-loading" data-testid="relation-mapping-loading">
      {{ t("dataIo.import.mapping.loading") }}
    </p>
    <NAlert v-else-if="error" type="warning" :show-icon="false">
      {{ t("dataIo.import.mapping.catalogError", { message: error }) }}
    </NAlert>
    <NAlert v-else-if="options !== null && options.length === 0" type="info" :show-icon="false" data-testid="relation-mapping-empty">
      {{ t("dataIo.import.mapping.empty") }}
    </NAlert>
    <NAlert v-else-if="options !== null && configurableTargets.length === 0" type="info" :show-icon="false" data-testid="relation-mapping-empty">
      {{ t("dataIo.import.mapping.noMatchFieldsAny") }}
    </NAlert>

    <NAlert v-if="unavailableSources.length" type="warning" :show-icon="false" data-testid="relation-mapping-unavailable-sources">
      <div v-for="source in unavailableSources" :key="source.value">{{ source.label }}</div>
    </NAlert>

    <div
      v-for="(row, index) in modelValue"
      :key="`${row.sourceColumn}-${row.relationId}-${index}`"
      class="mapping-row"
      data-testid="relation-mapping-row"
    >
      <label class="mapping-field">
        <span>{{ t("dataIo.import.mapping.sourceColumn") }}</span>
        <NSelect
          size="small"
          filterable
          :options="rowSourceOptions(modelValue)"
          :value="row.sourceColumn"
          :data-testid="`relation-mapping-source-${index}`"
          @update:value="updateRow(index, { sourceColumn: String($event) })"
        />
      </label>
      <label class="mapping-field">
        <span>{{ t("dataIo.import.mapping.targetField") }}</span>
        <NSelect
          size="small"
          :options="targetOptions(modelValue)"
          :value="row.relationId"
          :data-testid="`relation-mapping-target-${index}`"
          @update:value="onTargetChange(index, String($event))"
        />
      </label>
      <label class="mapping-field">
        <span>{{ t("dataIo.import.mapping.matchField") }}</span>
        <NSelect
          size="small"
          :options="matchFieldOptions(row.relationId)"
          :value="row.matchField || undefined"
          :placeholder="t('dataIo.import.mapping.matchFieldPlaceholder')"
          :data-testid="`relation-mapping-match-${index}`"
          @update:value="updateRow(index, { matchField: String($event) })"
        />
      </label>
      <NButton
        quaternary
        circle
        size="small"
        :disabled="disabled"
        :aria-label="t('dataIo.import.mapping.remove')"
        :data-testid="`relation-mapping-remove-${index}`"
        @click="removeRow(index)"
      >
        <template #icon><NIcon><X /></NIcon></template>
      </NButton>
    </div>

    <NButton
      v-if="options !== null"
      size="small"
      dashed
      :disabled="!canAddRow"
      data-testid="relation-mapping-add"
      @click="addRow"
    >
      <template #icon><NIcon><Plus /></NIcon></template>
      {{ t("dataIo.import.mapping.add") }}
    </NButton>
  </section>
</template>

<style scoped>
.relation-mapping { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
.relation-mapping > header { display: grid; gap: 2px; }
.relation-mapping > header span { display: inline-flex; align-items: center; gap: 7px; font-weight: 600; }
.relation-mapping > header small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.mapping-loading { padding: 8px 0; }
.mapping-row { display: grid; grid-template-columns: 1fr 1fr 1fr auto; gap: 8px; align-items: end; }
.mapping-field { display: grid; gap: 4px; }
.mapping-field > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
@media (max-width: 720px) {
  .mapping-row { grid-template-columns: 1fr; }
}
</style>
