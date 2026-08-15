<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NEmpty, NInput, NInputNumber, NModal, NSelect, NSpin, NSwitch } from "naive-ui";
import { Check, ExternalLink, Link2, Plus, Search, X } from "lucide-vue-next";
import type {
  NormalizedRelationDescriptor,
  FieldDefinitionV2,
  RelationTargetRef,
} from "@/contracts";
import { targetKey } from "@/stores/relationLookupStore";
import { t } from "@/i18n";

const props = defineProps<{
  show: boolean;
  descriptor: NormalizedRelationDescriptor | null;
  fieldLabel?: string | null;
  selected: readonly RelationTargetRef[];
  candidates: readonly RelationTargetRef[];
  total?: number;
  loading?: boolean;
  applying?: boolean;
  error?: string | null;
  query?: string;
  targetFields?: readonly FieldDefinitionV2[];
  targetRelations?: readonly NormalizedRelationDescriptor[];
  targetRelationOptions?: Readonly<Record<string, readonly RelationTargetRef[]>>;
  targetRelationLoading?: Readonly<Record<string, boolean>>;
  targetDisplayField?: string | null;
  createSchemaLoading?: boolean;
}>();

const emit = defineEmits<{
  close: [];
  search: [query: string];
  select: [target: RelationTargetRef];
  clear: [];
  apply: [];
  loadMore: [];
  create: [label: string];
  createFull: [values: Readonly<Record<string, unknown>>];
  searchCreateRelation: [field: string, query: string];
  fullCreateFallback: [];
  open: [target: RelationTargetRef];
}>();

const multi = computed(() => props.descriptor?.kind !== "m2o");
const fullCreateOpen = ref(false);
const fullValues = ref<Record<string, unknown>>({});
const writableCreateFields = computed(() => (props.targetFields ?? []).filter(field =>
  field.lifecycle.state === "active"
  && !["formula", "lookup", "autoDate", "file"].includes(field.logicalType)));
const unsupportedRequiredFields = computed(() => (props.targetFields ?? []).filter(field =>
  isRequired(field) && !hasDefault(field)
  && field.lifecycle.state === "active"
  && (field.logicalType === "file" || (field.logicalType === "relation" && !relationFor(field)))));
const canSubmitFull = computed(() => writableCreateFields.value
  .filter(field => isRequired(field) && !hasDefault(field))
  .every(field => hasInputValue(fullValues.value[field.identity.physicalName]))
  && !!props.targetDisplayField
  && hasInputValue(fullValues.value[props.targetDisplayField]));
const selectedKeys = computed(() => new Set(props.selected.map(targetKey)));
const title = computed(() => {
  const descriptor = props.descriptor;
  if (!descriptor) return t("relationEditor.title");
  const labels = {
    m2o: t("relationEditor.kind.m2o"),
    o2m: t("relationEditor.kind.o2m"),
    m2m: t("relationEditor.kind.m2m"),
  };
  return `${labels[descriptor.kind]} · ${props.fieldLabel || "关联字段"}`;
});
watch(() => props.show, show => {
  if (!show) {
    fullCreateOpen.value = false;
    fullValues.value = {};
  }
});

function beginFullCreate(): void {
  fullValues.value = props.targetDisplayField && props.query?.trim()
    ? { [props.targetDisplayField]: props.query.trim() }
    : {};
  fullCreateOpen.value = true;
}

function updateFullValue(field: string, value: unknown): void {
  fullValues.value = { ...fullValues.value, [field]: value };
}

function isRequired(field: FieldDefinitionV2): boolean {
  return field.value.required;
}

function hasDefault(field: FieldDefinitionV2): boolean {
  return field.value.default.enabled;
}

function hasInputValue(value: unknown): boolean {
  if (Array.isArray(value)) return value.length > 0;
  return value !== null && value !== undefined && value !== "";
}

interface SelectOptionEntry {
  readonly label: string;
  readonly value: string;
  readonly rawValue: unknown;
}

function selectOptionEntries(field: FieldDefinitionV2): SelectOptionEntry[] {
  return (field.select?.options ?? []).filter(option => option.state === "active")
    .map((option, index) => ({
      label: option.label,
      value: `select:${field.identity.fieldId}:${index}`,
      rawValue: option.optionId,
    }));
}

function selectOptions(field: FieldDefinitionV2): Array<{ label: string; value: string }> {
  return selectOptionEntries(field).map(({ label, value }) => ({ label, value }));
}

function relationFor(field: FieldDefinitionV2): NormalizedRelationDescriptor | undefined {
  return props.targetRelations?.find(relation => relation.fieldRef === field.identity.physicalName);
}

function relationOptions(field: FieldDefinitionV2): Array<{ label: string; value: string }> {
  return (props.targetRelationOptions?.[field.identity.physicalName] ?? []).map(target => ({
    label: target.secondaryLabel ? `${target.label} · ${target.secondaryLabel}` : target.label,
    value: target.itemId,
  }));
}

function relationInputValue(field: FieldDefinitionV2): string | string[] | null {
  const value = fullValues.value[field.identity.physicalName];
  if (relationFor(field)?.kind === "m2o") return typeof value === "string" ? value : null;
  return Array.isArray(value) ? value.filter(item => typeof item === "string") : [];
}

function selectInputValue(field: FieldDefinitionV2): string | string[] | null {
  const value = fullValues.value[field.identity.physicalName];
  const entries = selectOptionEntries(field);
  if (field.logicalType === "multiSelect") {
    if (!Array.isArray(value)) return [];
    return value.flatMap(raw => {
      const entry = entries.find(candidate => sameOptionValue(candidate.rawValue, raw));
      return entry ? [entry.value] : [];
    });
  }
  return entries.find(candidate => sameOptionValue(candidate.rawValue, value))?.value ?? null;
}

function updateSelectValue(field: FieldDefinitionV2, value: unknown): void {
  const byKey = new Map(selectOptionEntries(field).map(entry => [entry.value, entry.rawValue]));
  if (field.logicalType === "multiSelect") {
    const selected = Array.isArray(value)
      ? value.flatMap(key => typeof key === "string" && byKey.has(key) ? [byKey.get(key)] : [])
      : [];
    updateFullValue(field.identity.physicalName, selected);
    return;
  }
  updateFullValue(field.identity.physicalName, typeof value === "string" && byKey.has(value)
    ? byKey.get(value)
    : null);
}

function sameOptionValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (left === null || right === null || typeof left !== "object" || typeof right !== "object") {
    return false;
  }
  try {
    return JSON.stringify(left) === JSON.stringify(right);
  } catch {
    return false;
  }
}

function onQuery(value: string): void {
  emit("search", value);
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="title"
    class="relation-editor"
    :mask-closable="false"
    :auto-focus="true"
    :trap-focus="true"
    :close-on-esc="true"
    @update:show="value => { if (!value) emit('close') }"
  >
    <div v-if="descriptor" class="relation-editor__body">
      <div class="relation-editor__meta">
        <span class="relation-editor__kind">{{ descriptor.preset }}</span>
        <span>{{ multi ? t("relationEditor.applyTogether") : t("relationEditor.saveImmediately") }}</span>
        <span v-if="descriptor.selfRelation">{{ t("relationEditor.selfRelation") }}</span>
      </div>

      <NInput
        :value="query"
        clearable
        :placeholder="t('relationEditor.searchPlaceholder')"
        :input-props="{ 'aria-label': t('relationEditor.searchLabel') }"
        @update:value="onQuery"
      >
        <template #prefix><Search :size="14" /></template>
      </NInput>

      <NButton
        v-if="query?.trim() && descriptor.quickCreateEligible"
        secondary
        type="primary"
        :loading="applying"
        data-testid="relation-create-target"
        @click="emit('create', query.trim())"
      >
        <template #icon><Plus :size="14" /></template>
        {{ t("relationEditor.create", { label: query.trim() }) }}
      </NButton>

      <div
        v-if="query?.trim() && !descriptor.quickCreateEligible"
        class="relation-editor__full-create"
      >
        <p>{{ descriptor.quickCreateReason || "目标表有其他必填字段，需要填写完整记录。" }}</p>
        <NButton
          v-if="unsupportedRequiredFields.length"
          secondary
          data-testid="relation-full-create-fallback"
          @click="emit('fullCreateFallback')"
        >前往目标表完整编辑</NButton>
        <NButton
          v-else
          secondary
          type="primary"
          :loading="createSchemaLoading"
          data-testid="relation-full-create-target"
          @click="beginFullCreate"
        >填写完整记录</NButton>
        <div v-if="fullCreateOpen" class="relation-editor__full-fields">
          <label v-for="field in writableCreateFields" :key="field.identity.fieldId">
            <span>{{ field.displayName }}<b v-if="isRequired(field)"> *</b></span>
            <NSelect
              v-if="field.logicalType === 'relation'"
              :data-testid="`relation-full-create-${field.identity.physicalName}`"
              :value="relationInputValue(field)"
              :multiple="relationFor(field)?.kind !== 'm2o'"
              filterable
              remote
              :loading="targetRelationLoading?.[field.identity.physicalName] ?? false"
              :options="relationOptions(field)"
              placeholder="选择关联记录"
              @search="query => emit('searchCreateRelation', field.identity.physicalName, query)"
              @update:value="updateFullValue(field.identity.physicalName, $event)"
            />
            <NSwitch
              v-else-if="field.logicalType === 'bool'"
              :data-testid="`relation-full-create-${field.identity.physicalName}`"
              :value="Boolean(fullValues[field.identity.physicalName])"
              @update:value="updateFullValue(field.identity.physicalName, $event)"
            />
            <NInputNumber
              v-else-if="field.logicalType === 'number'"
              :data-testid="`relation-full-create-${field.identity.physicalName}`"
              :value="typeof fullValues[field.identity.physicalName] === 'number' ? Number(fullValues[field.identity.physicalName]) : null"
              @update:value="updateFullValue(field.identity.physicalName, $event)"
            />
            <NSelect
              v-else-if="field.logicalType === 'select' || field.logicalType === 'multiSelect'"
              :data-testid="`relation-full-create-${field.identity.physicalName}`"
              :value="selectInputValue(field)"
              :multiple="field.logicalType === 'multiSelect'"
              :options="selectOptions(field)"
              @update:value="updateSelectValue(field, $event)"
            />
            <NInput
              v-else
              :data-testid="`relation-full-create-${field.identity.physicalName}`"
              :value="String(fullValues[field.identity.physicalName] ?? '')"
              @update:value="updateFullValue(field.identity.physicalName, $event)"
            />
          </label>
          <NButton
            type="primary"
            :disabled="!canSubmitFull"
            :loading="applying"
            data-testid="relation-full-create-submit"
            @click="emit('createFull', fullValues)"
          >创建并关联</NButton>
        </div>
      </div>

      <div v-if="selected.length" class="relation-editor__selected">
        <div v-for="target in selected" :key="targetKey(target)" class="relation-editor__selected-row">
          <span class="relation-editor__token">
            {{ target.label }}
          </span>
          <NButton
            quaternary
            size="tiny"
            :aria-label="t('relationEditor.remove')"
            @click="multi ? emit('select', target) : emit('clear')"
          >
            <X :size="13" />
          </NButton>
        </div>
      </div>

      <NSpin :show="loading">
        <div class="relation-editor__results">
          <div
            v-for="target in candidates"
            :key="targetKey(target)"
            class="relation-editor__candidate-row"
          >
            <button
              type="button"
              class="relation-editor__candidate"
              :class="{ 'relation-editor__candidate--selected': selectedKeys.has(targetKey(target)) }"
              :aria-pressed="selectedKeys.has(targetKey(target))"
              @click="emit('select', target)"
            >
              <Link2 :size="14" />
              <span class="relation-editor__candidate-label">
                <span>
                  {{ target.label }}
                </span>
                <small v-if="target.secondaryLabel">{{ target.secondaryLabel }}</small>
              </span>
              <Check v-if="selectedKeys.has(targetKey(target))" :size="14" />
            </button>
            <NButton
              quaternary
              circle
              size="tiny"
              :aria-label="`打开 ${target.label}`"
              data-testid="relation-open-target"
              @click.stop="emit('open', target)"
            >
              <ExternalLink :size="13" />
            </NButton>
          </div>
          <NEmpty
            v-if="!loading && candidates.length === 0"
            :description="t('relationEditor.empty')"
            size="small"
          />
          <NButton
            v-if="!loading && candidates.length < (total ?? 0)"
            quaternary
            size="small"
            data-testid="relation-load-more"
            @click="emit('loadMore')"
          >
            加载更多（{{ candidates.length }} / {{ total }}）
          </NButton>
        </div>
      </NSpin>

      <p v-if="error" class="relation-editor__error" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <div class="relation-editor__footer">
        <NButton v-if="descriptor?.nullable && !multi" quaternary @click="emit('clear')">
          {{ t("relationEditor.clear") }}
        </NButton>
        <span class="relation-editor__spacer"></span>
        <NButton @click="emit('close')">{{ t("relationEditor.cancel") }}</NButton>
        <NButton v-if="multi" type="primary" :loading="applying" @click="emit('apply')">
          {{ t("relationEditor.apply", { count: selected.length }) }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style>
.relation-editor { width: min(620px, calc(100vw - 32px)); }
.relation-editor__body { display: grid; gap: var(--vt-space-3); }
.relation-editor__meta {
  display: flex; gap: var(--vt-space-2); align-items: center;
  color: var(--vt-fg-muted); font-size: var(--vt-font-caption);
}
.relation-editor__kind {
  padding: 1px 7px; border-radius: 999px;
  color: var(--vt-fg-accent-strong); background: var(--vt-color-primary-50);
  font-weight: 700; letter-spacing: .04em; text-transform: uppercase;
}
.relation-editor__selected { display: grid; gap: 6px; }
.relation-editor__full-create { display: grid; gap: 8px; padding: 10px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
.relation-editor__full-create p { margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.relation-editor__full-fields { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 10px; }
.relation-editor__full-fields label { display: grid; gap: 5px; font-size: var(--vt-font-caption); }
.relation-editor__full-fields > button { grid-column: 1 / -1; justify-self: end; }
.relation-editor__selected-row {
  display: grid; grid-template-columns: minmax(120px, 1fr) auto;
  gap: var(--vt-space-2); align-items: center;
  padding: 6px 8px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.relation-editor__token { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.relation-editor__results {
  display: grid; gap: 3px; min-height: 100px; max-height: 280px; padding: 2px; overflow: auto;
}
.relation-editor__candidate-row {
  display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 4px;
}
.relation-editor__candidate {
  display: grid; grid-template-columns: 18px 1fr 18px; align-items: center; gap: 7px;
  width: 100%; min-height: 34px; padding: 5px 9px;
  border: 1px solid transparent; border-radius: var(--vt-radius-sm);
  color: var(--vt-fg); background: transparent; text-align: left; cursor: pointer;
  transition: background var(--vt-duration-fast) var(--vt-ease), border-color var(--vt-duration-fast) var(--vt-ease);
}
.relation-editor__candidate:hover { background: var(--vt-bg-subtle); }
.relation-editor__candidate-label { display: grid; min-width: 0; }
.relation-editor__candidate-label > span,
.relation-editor__candidate-label > small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.relation-editor__candidate-label > small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.relation-editor__candidate--selected {
  border-color: color-mix(in srgb, var(--vt-color-primary-500) 35%, var(--vt-border));
  background: color-mix(in srgb, var(--vt-color-primary-500) 7%, var(--vt-bg));
}
.relation-editor__error { margin: 0; color: var(--vt-color-danger); font-size: var(--vt-font-caption); }
.relation-editor__footer { display: flex; align-items: center; gap: var(--vt-space-2); }
.relation-editor__spacer { flex: 1; }
</style>
