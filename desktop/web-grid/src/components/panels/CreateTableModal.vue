<script setup lang="ts">
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSpace } from "naive-ui";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const admin = useTableAdminStore();
const ui = useUiStore();

defineEmits<{
  submit: [];
  cancel: [];
}>();
</script>

<template>
  <NModal
    :show="ui.createModalOpen"
    preset="card"
    class="create-table-modal"
    :title="t('createTable.title')"
    style="width: min(560px, calc(100vw - 32px))"
    @update:show="(value: boolean) => !value && $emit('cancel')"
  >
    <NForm label-placement="top">
      <NFormItem :label="t('createTable.name')">
        <NInput
          v-model:value="admin.form.name"
          :maxlength="128"
          show-count
          :placeholder="t('createTable.name')"
          data-testid="create-table-name-input"
        />
      </NFormItem>
      <p class="name-hint">表名为 1–128 个字符，支持中文、字母、数字和符号，不能包含换行或控制字符；同一工作区内不能重名。</p>
      <NAlert type="info" :show-icon="false" class="next-step">
        创建后可继续添加字段，并设置字段类型、默认值和必填等规则。
      </NAlert>
      <p v-if="admin.error" class="form-error" role="alert" data-testid="create-table-error">
        {{ admin.error }}
      </p>
    </NForm>
    <template #action>
      <NSpace justify="end">
        <NButton size="small" data-testid="create-table-cancel" @click="$emit('cancel')">
          {{ t("createTable.cancel") }}
        </NButton>
        <NButton
          size="small"
          type="primary"
          :disabled="!admin.canSubmit"
          :loading="admin.phase === 'submitting'"
          data-testid="create-table-submit"
          @click="$emit('submit')"
        >
          {{ t("createTable.submit") }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.name-hint { margin: 0 0 12px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.6; }
.next-step {
  margin-top: var(--vt-space-2);
  line-height: 1.6;
}
.form-error {
  margin-top: var(--vt-space-3);
  color: var(--vt-color-danger);
  font-size: var(--vt-font-caption);
}
</style>
