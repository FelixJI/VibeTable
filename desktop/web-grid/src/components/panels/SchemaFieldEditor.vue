<script setup lang="ts">
import { computed, watch } from "vue";
import type { SchemaFieldDraft } from "@/services/schemaFieldDraft";
import {
  createSchemaEnumOptionDraft,
  validateSchemaFieldDraft,
} from "@/services/schemaFieldDraft";
import { t } from "@/i18n";
import { Plus, Trash2 } from "lucide-vue-next";

const props = defineProps<{
  field: SchemaFieldDraft;
  index: number;
  serverErrors: Readonly<Record<string, string>>;
  formulaPreview?: {
    readonly phase: "idle" | "loading" | "ready" | "error";
    readonly value: unknown;
    readonly error: string | null;
  };
}>();

const emit = defineEmits<{
  formulaPreview: [payload: {
    clientId: string;
    index: number;
    row: Readonly<Record<string, unknown>>;
  }];
}>();

const prefix = computed(() => `fields[${props.index}]`);
const errors = computed(() => {
  const local = validateSchemaFieldDraft(props.field, props.index);
  const server = Object.entries(props.serverErrors)
    .filter(([path]) => path === prefix.value || path.startsWith(`${prefix.value}.`))
    .map(([path, message]) => ({ path, message }));
  return [...server, ...local.filter((item) => !(item.path in props.serverErrors))];
});
const numeric = computed(() =>
  props.field.type === "integer" || props.field.type === "float" || props.field.type === "decimal");
const text = computed(() =>
  ["shortText", "longText", "richText", "email", "url", "uuid", "hash", "secret"]
    .includes(props.field.type));
const previewRowError = computed(() => {
  if (props.field.type !== "formula") return null;
  try {
    const value = JSON.parse(props.field.formulaPreviewRowText) as unknown;
    return value && typeof value === "object" && !Array.isArray(value)
      ? null
      : t("schema.preview.object");
  } catch {
    return t("schema.preview.validJson");
  }
});

function errorLabel(path: string): string {
  if (path.includes(".formula.")) return t("schema.error.formula");
  if (path.includes(".relation.")) return t("schema.error.relation");
  if (path.includes(".lookup.")) return t("schema.error.lookup");
  if (path.includes(".attachmentPolicy.")) return t("schema.error.attachment");
  if (path.endsWith(".defaultValue")) return t("schema.defaultValue");
  if (path.includes(".constraints.")) return t("schema.error.constraint");
  return t("schema.error.field");
}

watch(
  [
    () => props.field.type,
    () => props.field.formulaSource,
    () => props.field.formulaResultType,
    () => props.field.formulaPreviewRowText,
  ],
  () => {
    if (props.field.type !== "formula"
        || !props.field.formulaSource.trim()
        || previewRowError.value) return;
    emit("formulaPreview", {
      clientId: props.field.clientId,
      index: props.index,
      row: JSON.parse(props.field.formulaPreviewRowText) as Readonly<Record<string, unknown>>,
    });
  },
);
</script>

<template>
  <section class="field-config" :data-field-type="field.type">
    <div class="toggles">
      <label><input v-model="field.required" type="checkbox" /> {{ t("schema.required") }}</label>
      <label><input v-model="field.nullable" type="checkbox" /> {{ t("schema.nullable") }}</label>
      <label><input v-model="field.unique" type="checkbox" /> {{ t("schema.unique") }}</label>
    </div>
    <div v-if="text" class="config-grid">
      <label>{{ t("schema.minLength") }}<input v-model.number="field.minLength" type="number" min="0" /></label>
      <label>{{ t("schema.maxLength") }}<input v-model.number="field.maxLength" type="number" min="0" /></label>
      <label class="wide">{{ t("schema.pattern") }}<input v-model="field.pattern" type="text" /></label>
    </div>
    <div v-if="numeric" class="config-grid">
      <label>{{ t("schema.min") }}<input v-model.number="field.min" type="number" /></label>
      <label>{{ t("schema.max") }}<input v-model.number="field.max" type="number" /></label>
      <label v-if="field.type === 'decimal'">
        {{ t("schema.precision") }}
        <input v-model.number="field.precision" type="number" min="1" max="30" :data-testid="`field-precision-${index}`" />
      </label>
      <label v-if="field.type === 'decimal'">
        {{ t("schema.scale") }}
        <input v-model.number="field.scale" type="number" min="0" max="30" :data-testid="`field-scale-${index}`" />
      </label>
    </div>
    <section
      v-if="field.type === 'select' || field.type === 'multiSelect'"
      class="enum-config"
      :aria-label="t('schema.options')"
    >
      <div class="enum-heading">
        <div>
          <strong>{{ t("schema.options") }}</strong>
          <span>{{ t("schema.enumValueHint") }}</span>
        </div>
        <button
          type="button"
          class="compact-action"
          :data-testid="`field-enum-add-option-${index}`"
          @click="field.enumOptions.push(createSchemaEnumOptionDraft())"
        >
          <Plus :size="13" aria-hidden="true" />
          {{ t("schema.addOption") }}
        </button>
      </div>
      <div
        v-for="(option, optionIndex) in field.enumOptions"
        :key="option.clientId"
        class="enum-option-row"
        data-testid="field-enum-option-row"
      >
        <label>
          {{ t("schema.optionValue") }}
          <input
            v-model="option.valueText"
            :aria-label="t('schema.optionValueNumbered', { number: optionIndex + 1 })"
            :placeholder="t('schema.optionValuePlaceholder')"
            :data-testid="`field-enum-option-value-${index}-${optionIndex}`"
          />
        </label>
        <label>
          {{ t("schema.optionDisplayName") }}
          <input
            v-model="option.displayName"
            :aria-label="t('schema.optionDisplayNameNumbered', { number: optionIndex + 1 })"
            :placeholder="t('schema.optionDisplayNamePlaceholder')"
            :data-testid="`field-enum-option-display-${index}-${optionIndex}`"
          />
        </label>
        <button
          type="button"
          class="icon-action"
          :disabled="field.enumOptions.length <= 1"
          :aria-label="t('schema.removeOptionNumbered', { number: optionIndex + 1 })"
          :data-testid="`field-enum-remove-option-${index}-${optionIndex}`"
          @click="field.enumOptions.splice(optionIndex, 1)"
        >
          <Trash2 :size="14" aria-hidden="true" />
        </button>
      </div>
      <div v-if="field.type === 'multiSelect'" class="selection-bounds">
        <label>
          {{ t("schema.minSelected") }}
          <input
            v-model.number="field.enumMinSelected"
            type="number"
            min="0"
            :max="field.enumOptions.length"
            :data-testid="`field-enum-min-selected-${index}`"
          />
        </label>
        <label>
          {{ t("schema.maxSelected") }}
          <input
            v-model.number="field.enumMaxSelected"
            type="number"
            min="0"
            :max="field.enumOptions.length"
            :data-testid="`field-enum-max-selected-${index}`"
          />
        </label>
      </div>
      <p v-else class="enum-single-hint">
        {{ t("schema.singleSelectBounds") }}
      </p>
    </section>
    <label v-if="field.type === 'json' || field.type === 'geoJson'" class="single">
      JSON Schema
      <textarea v-model="field.jsonSchemaText" :data-testid="`field-json-schema-${index}`" placeholder='{"type":"object"}' />
    </label>
    <div v-if="field.type === 'formula'" class="config-grid">
      <label class="wide">
        {{ t("schema.formula") }}
        <textarea v-model="field.formulaSource" :data-testid="`field-formula-source-${index}`" placeholder="quantity * unit_price" />
      </label>
      <label>{{ t("schema.resultType") }}
        <select v-model="field.formulaResultType">
          <option value="integer">{{ t("createTable.fieldType.integer") }}</option><option value="float">{{ t("createTable.fieldType.float") }}</option>
          <option value="boolean">{{ t("createTable.fieldType.boolean") }}</option><option value="shortText">{{ t("createTable.fieldType.shortText") }}</option>
          <option value="dateTime">{{ t("createTable.fieldType.dateTime") }}</option>
        </select>
      </label>
      <label class="wide">
        {{ t("schema.previewInput") }}
        <textarea
          v-model="field.formulaPreviewRowText"
          :data-testid="`field-formula-preview-row-${index}`"
          placeholder='{"quantity":2,"unit_price":6.5}'
        />
      </label>
      <p v-if="previewRowError" class="field-error" :data-testid="`field-formula-preview-error-${index}`">
        {{ previewRowError }}
      </p>
      <div
        v-else
        class="formula-preview"
        :data-state="formulaPreview?.phase ?? 'idle'"
        :data-testid="`field-formula-preview-${index}`"
      >
        <span v-if="formulaPreview?.phase === 'loading'">{{ t("schema.preview.loading") }}</span>
        <span v-else-if="formulaPreview?.phase === 'error'" class="preview-error">
          {{ formulaPreview.error }}
        </span>
        <template v-else-if="formulaPreview?.phase === 'ready'">
          <span>{{ t("schema.preview.authoritative") }}</span>
          <code>{{ JSON.stringify(formulaPreview.value) }}</code>
        </template>
        <span v-else>{{ t("schema.preview.hint") }}</span>
      </div>
    </div>
    <div v-if="field.type === 'relation'" class="config-grid">
      <label class="wide">{{ t("schema.targetTable") }}<input v-model="field.targetTableId" :data-testid="`field-relation-target-${index}`" /></label>
      <label>{{ t("schema.cardinality") }}<select v-model="field.cardinality"><option value="one">{{ t("schema.one") }}</option><option value="many">{{ t("schema.many") }}</option></select></label>
      <label>{{ t("schema.deletePolicy") }}<select v-model="field.deletePolicy"><option value="restrict">{{ t("schema.restrict") }}</option><option value="cascade">{{ t("schema.cascade") }}</option><option value="setNull">{{ t("schema.setNull") }}</option></select></label>
    </div>
    <div v-if="field.type === 'lookup'" class="config-grid">
      <label>{{ t("schema.relationField") }}<input v-model="field.relationFieldId" :data-testid="`field-lookup-relation-${index}`" /></label>
      <label>{{ t("schema.targetField") }}<input v-model="field.targetFieldId" /></label>
      <label>{{ t("schema.aggregate") }}<select v-model="field.aggregate"><option value="none">{{ t("schema.none") }}</option><option value="first">{{ t("schema.first") }}</option><option value="count">{{ t("schema.count") }}</option><option value="sum">{{ t("schema.sum") }}</option><option value="min">{{ t("schema.minimum") }}</option><option value="max">{{ t("schema.maximum") }}</option></select></label>
      <label>{{ t("schema.lookupOutputType") }}
        <select v-model="field.lookupOutputType" :data-testid="`field-lookup-output-type-${index}`">
          <option value="shortText">{{ t("createTable.fieldType.shortText") }}</option>
          <option value="longText">{{ t("createTable.fieldType.longText") }}</option>
          <option value="integer">{{ t("createTable.fieldType.integer") }}</option>
          <option value="float">{{ t("createTable.fieldType.float") }}</option>
          <option value="decimal">{{ t("createTable.fieldType.decimal") }}</option>
          <option value="boolean">{{ t("createTable.fieldType.boolean") }}</option>
          <option value="date">{{ t("createTable.fieldType.date") }}</option>
          <option value="dateTime">{{ t("createTable.fieldType.dateTime") }}</option>
          <option value="time">{{ t("createTable.fieldType.time") }}</option>
          <option value="email">{{ t("createTable.fieldType.email") }}</option>
          <option value="url">{{ t("createTable.fieldType.url") }}</option>
          <option value="uuid">{{ t("createTable.fieldType.uuid") }}</option>
          <option value="select">{{ t("createTable.fieldType.select") }}</option>
          <option value="multiSelect">{{ t("createTable.fieldType.multiSelect") }}</option>
          <option value="json">{{ t("createTable.fieldType.json") }}</option>
          <option value="geoPoint">{{ t("createTable.fieldType.geoPoint") }}</option>
          <option value="geoJson">{{ t("createTable.fieldType.geoJson") }}</option>
          <option value="list">{{ t("createTable.fieldType.list") }}</option>
        </select>
      </label>
    </div>
    <div v-if="field.type === 'file'" class="config-grid">
      <label>{{ t("schema.maxFiles") }}<input v-model.number="field.maxFiles" type="number" min="1" :data-testid="`field-attachment-max-files-${index}`" /></label>
      <label>{{ t("schema.maxBytes") }}<input v-model.number="field.maxBytesPerFile" type="number" min="1" /></label>
      <label class="wide">{{ t("schema.allowedMime") }}<input v-model="field.allowedMimeTypesText" /></label>
      <label class="wide">
        {{ t("schema.thumbnailVariants") }}
        <input
          v-model="field.thumbnailVariantsText"
          :placeholder="t('schema.thumbnailVariantsPlaceholder')"
          :data-testid="`field-attachment-thumbnails-${index}`"
        />
      </label>
      <label><input v-model="field.protected" type="checkbox" /> {{ t("schema.protected") }}</label>
    </div>
    <label
      v-if="!['formula', 'lookup', 'autoDate', 'hash', 'secret', 'file', 'relation'].includes(field.type)"
      class="single"
    >
      {{ t("schema.defaultJson") }}
      <input v-model="field.defaultText" :placeholder="t('schema.defaultPlaceholder')" />
    </label>
    <div
      v-for="error in errors"
      :key="`${error.path}:${error.message}`"
      class="field-error"
      :data-testid="`field-error-${index}`"
    >
      <strong>{{ errorLabel(error.path) }}</strong>
      <span>{{ error.message }}</span>
      <details>
        <summary>{{ t("schema.details") }}</summary>
        <code>{{ error.path }}</code>
      </details>
    </div>
  </section>
</template>

<style scoped>
.field-config {
  grid-column: 1 / -1;
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--vt-border-subtle);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.toggles {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 18px;
  color: var(--vt-fg-secondary);
  font-size: var(--vt-font-caption);
}
.config-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.enum-config {
  display: grid;
  gap: 8px;
  padding: 10px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg);
}
.enum-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.enum-heading > div { display: grid; gap: 2px; }
.enum-heading strong { color: var(--vt-fg); font-size: var(--vt-font-caption); }
.enum-heading span,
.enum-single-hint { color: var(--vt-fg-muted); font-size: 11px; font-weight: 400; }
.enum-option-row {
  display: grid;
  grid-template-columns: minmax(0, .85fr) minmax(0, 1.15fr) 30px;
  gap: 8px;
  align-items: end;
}
.selection-bounds {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 150px));
  gap: 8px;
  padding-top: 2px;
}
.compact-action,
.icon-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  min-height: 28px;
  padding: 4px 8px;
  color: var(--vt-fg-accent);
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-sm);
  background: var(--vt-bg);
  font: 600 var(--vt-font-caption)/1 var(--vt-font-family);
  cursor: pointer;
}
.icon-action {
  width: 30px;
  padding: 0;
  color: var(--vt-fg-muted);
}
.compact-action:hover,
.icon-action:hover:not(:disabled) {
  color: var(--vt-fg-accent);
  border-color: var(--vt-color-primary-300);
  background: var(--vt-color-primary-50);
}
.compact-action:focus-visible,
.icon-action:focus-visible {
  outline: 2px solid var(--vt-color-primary-500);
  outline-offset: 2px;
}
.icon-action:disabled { opacity: .42; cursor: not-allowed; }
.enum-single-hint { margin: 0; }
label {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 5px;
  color: var(--vt-fg-secondary);
  font-size: var(--vt-font-caption);
  font-weight: 600;
}
.toggles label {
  min-width: max-content;
  flex-direction: row;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.toggles input {
  width: 15px;
  height: 15px;
  accent-color: var(--vt-color-primary-500);
}
.wide { grid-column: 1 / -1; }
input:not([type="checkbox"]), select, textarea {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  min-height: 32px;
  padding: 6px 9px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-sm);
  outline: 0;
  color: var(--vt-fg);
  background: var(--vt-bg);
  font: 12px/1.45 var(--vt-font-family);
  transition: border-color var(--vt-duration-fast) var(--vt-ease),
    box-shadow var(--vt-duration-fast) var(--vt-ease);
}
input:not([type="checkbox"]):focus, select:focus, textarea:focus {
  border-color: var(--vt-color-primary-500);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--vt-color-primary-500) 14%, transparent);
}
textarea { min-height: 68px; resize: vertical; }
.field-error {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: start;
  gap: 7px;
  margin: 0;
  padding: 7px 9px;
  color: var(--vt-color-danger-600);
  border: 1px solid color-mix(in srgb, var(--vt-color-danger-500) 25%, var(--vt-border));
  border-radius: var(--vt-radius-sm);
  background: color-mix(in srgb, var(--vt-color-danger-500) 6%, var(--vt-bg));
  font-size: var(--vt-font-caption);
}
.field-error strong { white-space: nowrap; }
.field-error details { color: var(--vt-fg-muted); font-size: 11px; }
.field-error summary { cursor: pointer; white-space: nowrap; }
.field-error code { display: block; margin-top: 4px; font-family: Consolas, monospace; }
.formula-preview { grid-column: 1 / -1; display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 34px; padding: 7px 9px; color: var(--vt-fg-muted); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); background: var(--vt-bg); font-size: var(--vt-font-caption); }
.formula-preview code { color: var(--vt-fg-accent); font: 600 12px/1.4 Consolas, monospace; }
.formula-preview .preview-error { color: var(--vt-color-danger); }
@media (max-width: 640px) {
  .config-grid { grid-template-columns: 1fr; }
  .enum-option-row { grid-template-columns: minmax(0, 1fr) 30px; }
  .enum-option-row > :nth-child(2) { grid-column: 1; }
  .enum-option-row > :nth-child(3) { grid-column: 2; grid-row: 1 / 3; }
  .selection-bounds { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .wide { grid-column: auto; }
  .field-error { grid-template-columns: 1fr; }
}
</style>
