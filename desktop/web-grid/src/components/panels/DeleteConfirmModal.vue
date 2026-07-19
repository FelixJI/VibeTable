<script setup lang="ts">
/**
 * DeleteConfirmModal — pure-presentation confirmation dialog for table delete.
 *
 * Reads `uiStore.deleteModalOpen` and `uiStore.deleteTarget` (the table name
 * being confirmed for delete — set by `ui.openDelete(name)` in WorkspaceView
 * when AppSidebar emits `requestDelete`). EMITS confirm/cancel; it does NOT
 * call `tableAdminService.deleteTable` — `WorkspaceView` wires confirm to
 * `service.deleteTable(ui.deleteTarget)`.
 *
 * The UI store owns visibility/target; the admin store owns in-flight/error
 * state so a rejected delete remains visible and retryable.
 */
import { NModal, NSpace, NButton } from "naive-ui";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { t } from "@/i18n";

const ui = useUiStore();
const admin = useTableAdminStore();

const emit = defineEmits<{ confirm: []; cancel: [] }>();
</script>

<template>
  <NModal
    :show="ui.deleteModalOpen"
    preset="card"
    :title="t('delete.title')"
    style="max-width: 420px"
  >
    <p data-testid="delete-confirm-message">
      {{ t("sidebar.delete.confirm", { name: ui.deleteTarget ?? "" }) }}
    </p>
    <p v-if="admin.error" class="delete-error" role="alert" data-testid="delete-error">
      {{ admin.error }}
    </p>
    <template #action>
      <NSpace justify="end">
        <NButton size="small" data-testid="delete-confirm-cancel" @click="emit('cancel')">
          {{ t("delete.cancel") }}
        </NButton>
        <NButton
          size="small"
          type="error"
          :loading="admin.phase === 'deleting'"
          data-testid="delete-confirm-ok"
          @click="emit('confirm')"
        >
          {{ t("delete.confirm") }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.delete-error {
  color: var(--vt-color-danger);
}
</style>
