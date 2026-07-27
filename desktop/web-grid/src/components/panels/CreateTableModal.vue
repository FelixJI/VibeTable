<script setup lang="ts">
/**
 * CreateTableModal — pure-presentation form for the create-table flow.
 *
 * Reads `tableAdminStore.form` (the store is the single source of truth for
 * form state — architecture fix #3: form state lives in the store, not the
 * DOM) and `uiStore.createModalOpen` for visibility. EMITS submit/cancel; it
 * does NOT call `tableAdminService` — `WorkspaceView` wires submit to
 * `service.createTable()`.
 *
 * The form's editable field rows come from `tableAdminStore.form.fields`. The
 * store mutators `addField()` / `removeField(i)` are called directly because
 * they are pure store mutations (no service involved). The submit button is
 * disabled unless `admin.canSubmit` is true (the store derives this from the
 * validation rules in `tableAdminValidation`).
 */
import { computed } from "vue";
import {
  NModal, NForm, NFormItem, NInput, NButton, NSpace, NSelect, NCheckbox,
} from "naive-ui";
import { Plus, Trash2 } from "lucide-vue-next";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { TABLE_FIELD_TYPES } from "@/contracts";
import type { TableFieldType } from "@/contracts";
import { t } from "@/i18n";
import { createSchemaFieldDraft } from "@/services/schemaFieldDraft";
import SchemaFieldEditor from "./SchemaFieldEditor.vue";

const admin = useTableAdminStore();
const ui = useUiStore();

defineProps<{
  formulaPreviews?: Readonly<Record<string, {
    readonly phase: "idle" | "loading" | "ready" | "error";
    readonly value: unknown;
    readonly error: string | null;
  }>>;
}>();

const emit = defineEmits<{
  submit: [];
  cancel: [];
  formulaPreview: [payload: {
    clientId: string;
    index: number;
    row: Readonly<Record<string, unknown>>;
  }];
}>();

const fieldTypeOptions = computed(() =>
  TABLE_FIELD_TYPES
    .filter((type) => type !== "decimal" && type !== "autoDate")
    .map((type) => ({
      label: t(`createTable.fieldType.${type}`),
      value: type,
    })),
);

const indexFieldOptions = computed(() =>
  admin.form.fields.map((field, index) => ({
    label: field.name.trim() || t("createTable.index.fieldFallback", { number: index + 1 }),
    value: field.clientId,
  })),
);

const indexTypeOptions = computed(() => [
  { label: t("createTable.index.normal"), value: "normal" },
  { label: t("createTable.index.unique"), value: "unique" },
]);

function changeFieldType(index: number, type: TableFieldType): void {
  const current = admin.form.fields[index];
  if (!current || current.type === type) return;
  admin.updateField(index, {
    ...createSchemaFieldDraft(type),
    clientId: current.clientId,
    name: current.name,
  });
}
</script>

<template>
  <NModal
    :show="ui.createModalOpen"
    preset="card"
    class="create-table-modal"
    :title="t('createTable.title')"
    style="width: min(820px, calc(100vw - 32px)); max-width: 820px; max-height: calc(100vh - 32px)"
    @update:show="(v: boolean) => !v && emit('cancel')"
  >
    <NForm label-placement="top">
      <NFormItem :label="t('createTable.name')">
        <NInput
          v-model:value="admin.form.name"
          :maxlength="128"
          :placeholder="t('createTable.name')"
          data-testid="create-table-name-input"
        />
      </NFormItem>
      <section
        v-if="admin.autoDateProducerEnabled"
        class="system-fields"
        aria-labelledby="create-table-system-fields-title"
      >
        <div>
          <h3 id="create-table-system-fields-title">{{ t("schema.autoDate.systemFields") }}</h3>
          <p>{{ t("schema.autoDate.systemFieldsHelp") }}</p>
        </div>
        <NCheckbox
          v-model:checked="admin.form.includeCreatedAt"
          data-testid="create-table-include-created-at"
        >
          <span class="system-field-option">
            <strong>{{ t("schema.autoDate.createdAt") }}</strong>
            <small>{{ t("schema.autoDate.createdAt.help") }}</small>
          </span>
        </NCheckbox>
        <NCheckbox
          v-model:checked="admin.form.includeUpdatedAt"
          data-testid="create-table-include-updated-at"
        >
          <span class="system-field-option">
            <strong>{{ t("schema.autoDate.updatedAt") }}</strong>
            <small>{{ t("schema.autoDate.updatedAt.help") }}</small>
          </span>
        </NCheckbox>
      </section>
      <div
        v-for="(field, idx) in admin.form.fields"
        :key="field.clientId"
        class="field-row"
        data-testid="create-table-field-row"
      >
        <NInput
          v-model:value="field.name"
          :placeholder="t('createTable.fieldName')"
          :maxlength="128"
          :data-testid="`create-table-field-name-${idx}`"
        />
        <NSelect
          :value="field.type"
          :options="fieldTypeOptions"
          filterable
          style="width: 100%"
          :data-testid="`create-table-field-type-${idx}`"
          @update:value="changeFieldType(idx, $event as TableFieldType)"
        />
        <NButton
          size="small"
          quaternary
          :disabled="admin.form.fields.length <= 1"
          :data-testid="`create-table-remove-field-${idx}`"
          @click="admin.removeField(idx)"
        >
          <template #icon><Trash2 :size="14" /></template>
        </NButton>
        <SchemaFieldEditor
          :field="field"
          :index="idx"
          :server-errors="admin.serverFieldErrors"
          :formula-preview="formulaPreviews?.[field.clientId]"
          @formula-preview="emit('formulaPreview', $event)"
        />
      </div>
      <NButton size="small" dashed data-testid="create-table-add-field" @click="admin.addField()">
        <template #icon><Plus /></template>
        {{ t("createTable.addField") }}
      </NButton>
      <section class="index-section" aria-labelledby="create-table-index-title">
        <div class="section-heading">
          <div>
            <h3 id="create-table-index-title">{{ t("createTable.index.title") }}</h3>
            <p>{{ t("createTable.index.hint") }}</p>
          </div>
          <NButton
            size="small"
            dashed
            data-testid="create-table-add-index"
            @click="admin.addIndex()"
          >
            <template #icon><Plus :size="14" /></template>
            {{ t("createTable.index.add") }}
          </NButton>
        </div>
        <div
          v-for="(indexDraft, index) in admin.form.indexes"
          :key="indexDraft.clientId"
          class="index-row"
          data-testid="create-table-index-row"
        >
          <NInput
            v-model:value="indexDraft.name"
            :placeholder="t('createTable.index.namePlaceholder')"
            :data-testid="`create-table-index-name-${index}`"
          />
          <NSelect
            v-model:value="indexDraft.fieldClientIds"
            multiple
            filterable
            max-tag-count="responsive"
            :options="indexFieldOptions"
            :placeholder="t('createTable.index.fieldsPlaceholder')"
            :data-testid="`create-table-index-fields-${index}`"
          />
          <NSelect
            :value="indexDraft.unique ? 'unique' : 'normal'"
            :options="indexTypeOptions"
            :data-testid="`create-table-index-type-${index}`"
            @update:value="indexDraft.unique = $event === 'unique'"
          />
          <NButton
            size="small"
            quaternary
            :aria-label="t('createTable.index.remove')"
            :data-testid="`create-table-remove-index-${index}`"
            @click="admin.removeIndex(index)"
          >
            <template #icon><Trash2 :size="14" /></template>
          </NButton>
          <p
            v-for="item in admin.localIndexErrors.filter((error) =>
              error.path === `indexes[${index}]`
                || error.path.startsWith(`indexes[${index}].`))"
            :key="`${item.path}:${item.message}`"
            class="index-error"
            role="alert"
            :data-testid="`create-table-index-error-${index}`"
          >
            <span>{{ item.message }}</span>
            <code>{{ item.path }}</code>
          </p>
        </div>
        <p v-if="admin.form.indexes.length === 0" class="index-empty">
          {{ t("createTable.index.empty") }}
        </p>
      </section>
      <p class="identifier-hint" data-testid="physical-name-hint">
        {{ t("createTable.identifierHint") }}
      </p>
      <p class="identifier-hint" data-testid="field-type-hint">
        {{ t("createTable.fieldTypeHint") }}
      </p>
      <p v-if="admin.error" class="form-error" role="alert" data-testid="create-table-error">
        {{ admin.error }}
      </p>
    </NForm>
    <template #action>
      <NSpace justify="end">
        <NButton size="small" data-testid="create-table-cancel" @click="emit('cancel')">
          {{ t("createTable.cancel") }}
        </NButton>
        <NButton
          size="small"
          type="primary"
          :disabled="!admin.canSubmit"
          :loading="admin.phase === 'submitting'"
          data-testid="create-table-submit"
          @click="emit('submit')"
        >
          {{ t("createTable.submit") }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.field-row {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) minmax(230px, .9fr) auto;
  gap: 10px;
  align-items: center;
  margin-bottom: 10px;
  padding: 12px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
  box-shadow: var(--vt-shadow-1);
}

.identifier-hint {
  margin: var(--vt-space-3) 0 0;
  color: var(--vt-text-secondary);
  font-size: 12px;
}

.system-fields {
  display: grid;
  grid-template-columns: minmax(220px, 1fr) auto auto;
  gap: 12px;
  align-items: center;
  margin-bottom: 12px;
  padding: 12px;
  border: 1px solid var(--vt-border-subtle);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.system-fields h3,
.system-fields p { margin: 0; }
.system-fields h3 { font-size: 14px; }
.system-fields p {
  margin-top: 3px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.system-field-option {
  display: inline-grid;
  gap: 2px;
  max-width: 260px;
  vertical-align: top;
}
.system-field-option small {
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}

.index-section {
  display: grid;
  gap: 10px;
  margin-top: var(--vt-space-4);
  padding-top: var(--vt-space-4);
  border-top: 1px solid var(--vt-border-subtle);
}

.section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.section-heading h3 {
  margin: 0;
  color: var(--vt-fg);
  font-size: 14px;
  font-weight: 650;
}

.section-heading p,
.index-empty {
  margin: 3px 0 0;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}

.index-row {
  display: grid;
  grid-template-columns: minmax(150px, .7fr) minmax(240px, 1.3fr) 112px auto;
  gap: 8px;
  align-items: center;
  padding: 10px;
  border: 1px solid var(--vt-border-subtle);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}

.index-error {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin: 0;
  padding: 7px 9px;
  color: var(--vt-color-danger-600);
  border: 1px solid color-mix(in srgb, var(--vt-color-danger-500) 25%, var(--vt-border));
  border-radius: var(--vt-radius-sm);
  background: color-mix(in srgb, var(--vt-color-danger-500) 6%, var(--vt-bg));
  font-size: var(--vt-font-caption);
}

.index-error code {
  color: var(--vt-fg-muted);
  font: 11px/1.4 Consolas, monospace;
}

.form-error {
  margin: var(--vt-space-2) 0 0;
  color: var(--vt-color-danger);
  font-size: 12px;
}

:global(.create-table-modal.n-card) {
  display: flex;
  overflow: hidden;
  flex-direction: column;
  border: 1px solid var(--vt-border);
  background: var(--vt-bg-elevated);
  box-shadow: var(--vt-shadow-3);
}
:global(.create-table-modal .n-card-header) {
  flex: 0 0 auto;
  padding: 16px 20px;
  border-bottom: 1px solid var(--vt-border);
}
:global(.create-table-modal .n-card__content) {
  min-height: 0;
  padding: 16px 20px;
  overflow-y: auto;
}
:global(.create-table-modal .n-card__action) {
  flex: 0 0 auto;
  padding: 12px 20px;
  border-top: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
@media (max-width: 680px) {
  .field-row { grid-template-columns: minmax(0, 1fr) auto; }
  .field-row > :nth-child(2) { grid-column: 1 / 2; }
  .field-row > :nth-child(3) { grid-column: 2; grid-row: 1 / 3; }
  .section-heading { align-items: flex-start; }
  .index-row { grid-template-columns: minmax(0, 1fr) auto; }
  .index-row > :nth-child(2),
  .index-row > :nth-child(3) { grid-column: 1 / -1; }
  .index-row > :nth-child(4) { grid-column: 2; grid-row: 1; }
  .index-error { align-items: flex-start; flex-direction: column; }
}
</style>
