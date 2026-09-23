<script setup lang="ts">
import { computed } from "vue";
import { NAlert, NButton, NCard, NCheckbox, NIcon, NTag } from "naive-ui";
import { Download, Table2, X } from "@lucide/vue";
import type { ExportLookupPanelState } from "@/composables/useDataIoTask";
import { t } from "@/i18n";

const props = defineProps<{
  panel: ExportLookupPanelState;
  selectedIds: readonly string[];
}>();

const emit = defineEmits<{
  "update:selectedIds": [ids: readonly string[]];
  confirm: [];
  cancel: [];
}>();

const formatLabel = computed(() => props.panel.format.toUpperCase());

const canConfirm = computed(() => !props.panel.loading);

function isChecked(lookupId: string): boolean {
  return props.selectedIds.includes(lookupId);
}

function toggle(lookupId: string, checked: boolean): void {
  if (props.panel.loading) return;
  const current = new Set(props.selectedIds);
  if (checked) current.add(lookupId);
  else current.delete(lookupId);
  emit("update:selectedIds", [...current]);
}
</script>

<template>
  <div class="export-lookup-shell">
    <NCard
      class="export-lookup-panel"
      size="small"
      role="dialog"
      aria-modal="true"
      :aria-label="t('dataIo.export.lookup.title')"
      data-testid="export-lookup-panel"
    >
      <template #header>
        <div class="panel-title">
          <span class="file-mark"><NIcon :size="21"><Download /></NIcon></span>
          <span>
            <strong>{{ t("dataIo.export.lookup.title", { format: formatLabel }) }}</strong>
            <small>{{ panel.collection }}</small>
          </span>
        </div>
      </template>
      <template #header-extra>
        <NButton
          quaternary
          circle
          size="small"
          :aria-label="t('dataIo.export.lookup.cancel')"
          data-testid="export-lookup-cancel"
          @click="emit('cancel')"
        >
          <template #icon><NIcon><X /></NIcon></template>
        </NButton>
      </template>

      <div class="panel-body">
        <p class="hint">{{ t("dataIo.export.lookup.hint") }}</p>

        <NAlert v-if="panel.error" type="warning" :show-icon="false" data-testid="export-lookup-error">
          {{ t("dataIo.export.lookup.error", { message: panel.error }) }}
        </NAlert>

        <p v-if="panel.loading" class="state-text" data-testid="export-lookup-loading">
          {{ t("dataIo.export.lookup.loading") }}
        </p>
        <NAlert
          v-else-if="panel.options.length === 0"
          type="info"
          :show-icon="false"
          data-testid="export-lookup-empty"
        >
          {{ t("dataIo.export.lookup.empty") }}
        </NAlert>

        <ul v-else class="lookup-list">
          <li v-for="option in panel.options" :key="option.lookupId">
            <NCheckbox
              :checked="isChecked(option.lookupId)"
              :data-testid="`export-lookup-option-${option.lookupId}`"
              @update:checked="toggle(option.lookupId, $event)"
            >
              <span class="option-label">
                <NIcon :size="15"><Table2 /></NIcon>
                {{ option.displayName }}
                <NTag size="tiny" :bordered="false">{{ option.outputType }}</NTag>
                <small>{{ t("dataIo.export.lookup.readonlyHint") }}</small>
              </span>
            </NCheckbox>
          </li>
        </ul>
      </div>

      <template #action>
        <div class="panel-actions">
          <span>{{ t("dataIo.export.lookup.defaultNone") }}</span>
          <div>
            <NButton data-testid="export-lookup-cancel" @click="emit('cancel')">
              {{ t("dataIo.export.lookup.cancel") }}
            </NButton>
            <NButton
              type="primary"
              :disabled="!canConfirm"
              data-testid="export-lookup-confirm"
              @click="emit('confirm')"
            >
              {{ t("dataIo.export.lookup.confirm", { format: formatLabel }) }}
            </NButton>
          </div>
        </div>
      </template>
    </NCard>
  </div>
</template>

<style scoped>
.export-lookup-shell {
  position: fixed;
  inset: 0;
  z-index: 45;
  display: grid;
  place-items: center;
  padding: var(--vt-space-4);
  background: color-mix(in srgb, var(--vt-bg-sunken) 64%, transparent);
  backdrop-filter: blur(2px);
}
.export-lookup-panel { width: min(560px, calc(100vw - 32px)); box-shadow: var(--vt-shadow-3); }
.panel-title { display: flex; align-items: center; gap: var(--vt-space-3); }
.panel-title > span:last-child { display: grid; gap: 2px; }
.panel-title small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); font-weight: 400; }
.file-mark { display: grid; width: 36px; height: 36px; place-items: center; color: var(--vt-color-primary-600); border: 1px solid var(--vt-color-primary-200); border-radius: var(--vt-radius-md); background: var(--vt-color-primary-50); }
.panel-body { display: grid; gap: var(--vt-space-3); }
.hint { margin: 0; color: var(--vt-fg-secondary); }
.state-text { margin: 0; color: var(--vt-fg-muted); }
.lookup-list { display: grid; gap: 6px; max-height: 260px; margin: 0; padding: 4px; overflow-y: auto; list-style: none; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
.lookup-list li { padding: 6px 8px; }
.option-label { display: inline-flex; align-items: center; gap: 8px; }
.option-label small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.panel-actions { display: flex; align-items: center; justify-content: space-between; gap: var(--vt-space-3); }
.panel-actions > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.panel-actions > div { display: flex; gap: var(--vt-space-2); }
@media (max-width: 640px) {
  .panel-actions { align-items: stretch; flex-direction: column; }
  .panel-actions > div { justify-content: flex-end; }
}
</style>
