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
 * Brief-template deviation: the brief imports `useTableAdminStore` but only
 * uses `uiStore` for both the open flag and the target name (which matches
 * where `ui.openDelete(name)` actually writes — see uiStore.openDelete). The
 * admin-store import is dropped here because it was unused (and would trip
 * `noUnusedLocals`).
 */
import { NModal, NSpace, NButton } from "naive-ui";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const ui = useUiStore();

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
    <template #action>
      <NSpace justify="end">
        <NButton size="small" data-testid="delete-confirm-cancel" @click="emit('cancel')">
          {{ t("delete.cancel") }}
        </NButton>
        <NButton
          size="small"
          type="error"
          data-testid="delete-confirm-ok"
          @click="emit('confirm')"
        >
          {{ t("delete.confirm") }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>
