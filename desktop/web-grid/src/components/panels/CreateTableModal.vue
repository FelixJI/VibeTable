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
          :placeholder="t('createTable.name')"
          data-testid="create-table-name-input"
        />
      </NFormItem>
      <NAlert type="info" :show-icon="false" class="next-step">
        数据表创建后会立即打开统一字段设置抽屉。字段身份、物理名、默认值与约束均由
        Schema v2 计划分配，不再在建表窗口生成。
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
