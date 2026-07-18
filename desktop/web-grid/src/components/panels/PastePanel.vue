<script setup lang="ts">
/**
 * PastePanel — pure-presentation panel for the two-phase paste flow.
 *
 * Reads `pasteStore` (phase, plan, result, summaryText, acked) and `uiStore`
 * (pastePanelOpen) and EMITS user intent (confirm / cancel). It does NOT call
 * `pasteService` — `WorkspaceView` translates `confirm` into the apply call.
 *
 * Note: the brief template used an "overflow" phase for a redirect UI; the real
 * `pasteStore.PastePhase` includes "overflow" too (set when `plan.overflow`
 * is true), so that branch renders the redirect hint as designed.
 */
import { computed } from "vue";
import { NCard, NButton, NSpace, NCheckbox, NTag, NText } from "naive-ui";
import { usePasteStore } from "@/stores/pasteStore";
import { useUiStore } from "@/stores/uiStore";
import { errorsByRow } from "@/stores/pasteFlowHelpers";
import { t } from "@/i18n";

const paste = usePasteStore();
const ui = useUiStore();

const emit = defineEmits<{
  confirm: [];
  cancel: [];
}>();

const titleKey = computed(() => {
  if (paste.phase === "applied") return "paste.title.result";
  if (paste.phase === "error") return "paste.title.error";
  return "paste.title.preview";
});

const diagnostics = computed(() => errorsByRow(paste.plan));

const hasWarning = computed(() =>
  diagnostics.value.some((g) => g.diagnostics.some((d) => d.severity === "warning")),
);

const canConfirm = computed(() => paste.phase === "previewing" && paste.acked);
</script>

<template>
  <NCard
    v-if="ui.pastePanelOpen"
    class="paste-panel"
    :bordered="true"
    size="small"
    data-testid="paste-panel"
  >
    <template #header>{{ t(titleKey) }}</template>
    <template #header-extra>
      <NButton size="tiny" quaternary data-testid="paste-close" @click="emit('cancel')">×</NButton>
    </template>

    <div class="paste-body">
      <NText v-if="paste.summaryText" depth="3" data-testid="paste-summary">
        {{ paste.summaryText }}
      </NText>

      <div v-if="paste.phase === 'overflow'" class="paste-overflow" data-testid="paste-overflow">
        {{ t("paste.overflow") }}
      </div>

      <div v-if="diagnostics.length" class="paste-diagnostics" data-testid="paste-diagnostics">
        <div v-for="g of diagnostics" :key="g.rowIndex">
          <NTag
            v-for="(d, i) in g.diagnostics"
            :key="i"
            :type="d.severity === 'error' ? 'error' : 'warning'"
            size="small"
          >
            {{ t("paste.diagnostic.rowCol", { row: g.rowIndex + 1, col: d.columnIndex + 1, message: d.message }) }}
          </NTag>
        </div>
      </div>

      <NCheckbox
        v-if="hasWarning"
        :checked="paste.acked"
        data-testid="paste-ack"
        @update:checked="paste.toggleAck()"
      >
        {{ t("paste.ack") }}
      </NCheckbox>
    </div>

    <template #action>
      <NSpace justify="end">
        <NButton size="small" data-testid="paste-cancel" @click="emit('cancel')">
          {{ t("paste.cancel") }}
        </NButton>
        <NButton
          size="small"
          type="primary"
          :disabled="!canConfirm"
          data-testid="paste-confirm"
          @click="emit('confirm')"
        >
          {{ t("paste.confirm") }}
        </NButton>
      </NSpace>
    </template>
  </NCard>
</template>

<style scoped>
.paste-panel {
  position: fixed;
  right: var(--vt-space-4);
  bottom: var(--vt-space-4);
  width: 360px;
  max-height: 60vh;
  z-index: 30;
  box-shadow: var(--vt-shadow-3);
}
.paste-diagnostics {
  max-height: 200px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--vt-space-1);
  margin: var(--vt-space-2) 0;
}
.paste-overflow {
  background: rgba(255, 166, 0, 0.15);
  border: 1px solid var(--vt-color-warning);
  border-radius: var(--vt-radius-sm);
  padding: var(--vt-space-2);
  color: var(--vt-color-warning);
}
</style>
