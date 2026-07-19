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
import { NModal, NForm, NFormItem, NInput, NButton, NSpace, NSelect } from "naive-ui";
import { Plus, Trash2 } from "lucide-vue-next";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { TABLE_FIELD_TYPES } from "@/contracts";
import { t } from "@/i18n";

const admin = useTableAdminStore();
const ui = useUiStore();

const emit = defineEmits<{
  submit: [];
  cancel: [];
}>();

const fieldTypeOptions = TABLE_FIELD_TYPES.map((tp) => ({ label: tp, value: tp }));
</script>

<template>
  <NModal
    :show="ui.createModalOpen"
    preset="card"
    :title="t('createTable.title')"
    style="max-width: 520px"
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
      <div
        v-for="(field, idx) in admin.form.fields"
        :key="idx"
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
          v-model:value="field.type"
          :options="fieldTypeOptions"
          style="width: 140px"
          :data-testid="`create-table-field-type-${idx}`"
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
      </div>
      <NButton size="small" dashed data-testid="create-table-add-field" @click="admin.addField()">
        <template #icon><Plus /></template>
        {{ t("createTable.addField") }}
      </NButton>
      <p class="identifier-hint" data-testid="physical-name-hint">
        {{ t("createTable.identifierHint") }}
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
  grid-template-columns: 1fr 140px auto;
  gap: var(--vt-space-2);
  align-items: center;
  margin-bottom: var(--vt-space-2);
}

.identifier-hint {
  margin: var(--vt-space-3) 0 0;
  color: var(--vt-text-secondary);
  font-size: 12px;
}

.form-error {
  margin: var(--vt-space-2) 0 0;
  color: var(--vt-color-danger);
  font-size: 12px;
}
</style>
