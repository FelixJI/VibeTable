<script setup lang="ts">
import { computed } from "vue";
import { NAlert, NButton, NInput, NPopconfirm, NSelect } from "naive-ui";
import type { NamedRevisionAction, NamedRevisionState } from "@/composables/useNamedRevisions";
import { t } from "@/i18n";
const props = withDefaults(defineProps<{ state: NamedRevisionState; fieldOptions?: readonly { label: string; value: string }[] }>(), { fieldOptions: () => [] });
const emit = defineEmits<{
  action: [action: NamedRevisionAction]; select: [id: string]; name: [name: string];
}>();
const choices = computed(() => props.state.versions.map((entry) => ({
  label: entry.name || entry.key || entry.id, value: entry.id,
})));
const fieldLabels = computed(() => new Map(props.fieldOptions.map((field) => [field.value, field.label])));
function displayValue(value: unknown): string {
  return value === null || value === undefined || value === "" ? t("named.emptyValue")
    : typeof value === "string" ? value : JSON.stringify(value);
}
</script>
<template>
  <section class="named-revisions" data-testid="named-revisions">
    <header><strong>{{ t('named.title') }}</strong>
      <NButton size="tiny" :disabled="state.loading" data-testid="named-reload" @click="emit('action', 'reload')">{{ t('named.reload') }}</NButton>
    </header>
    <p>{{ t('named.description') }}</p>
    <NAlert v-if="state.error" type="error" :title="state.error" data-testid="named-error" />
    <div class="named-row">
      <NInput :value="state.name" :maxlength="128" :disabled="state.loading" :input-props="{ 'aria-label': t('named.name') }" :placeholder="t('named.name')" data-testid="named-name" @update:value="emit('name', $event)" />
      <NButton :disabled="state.loading || !state.name.trim()" data-testid="named-create" @click="emit('action', 'create')">{{ t('named.create') }}</NButton>
    </div>
    <p v-if="!state.loading && !state.versions.length" role="status" data-testid="named-empty">{{ t('named.empty') }}</p>
    <NSelect :aria-label="t('named.select')" :value="state.selectedId || null" :options="choices" :disabled="state.loading" :placeholder="t('named.select')" data-testid="named-select" @update:value="emit('select', $event)" />
    <div class="named-row">
      <NButton :disabled="state.loading || !state.selectedId" data-testid="named-save" @click="emit('action', 'save')">{{ t('named.save') }}</NButton>
      <NButton :disabled="state.loading || !state.selectedId" data-testid="named-compare" @click="emit('action', 'compare')">{{ t('named.compare') }}</NButton>
      <NPopconfirm :positive-text="t('named.delete')" @positive-click="emit('action', 'delete')">
        <template #trigger><NButton :disabled="state.loading || !state.selectedId" data-testid="named-delete">{{ t('named.delete') }}</NButton></template>
        {{ t('named.deleteConfirm') }}
      </NPopconfirm>
    </div>
    <div v-if="state.comparison" data-testid="named-comparison">
      <p v-if="!Object.keys(state.comparison.differences).length" role="status" data-testid="named-no-differences">{{ t('named.noDifferences') }}</p>
      <dl v-else class="named-differences">
        <template v-for="(difference, field) in state.comparison.differences" :key="field">
          <dt>{{ fieldLabels.get(String(field)) ?? field }}</dt>
          <dd><span class="named-value-label">{{ t('named.current') }}</span><span>{{ displayValue(difference.main) }}</span>
            <span class="named-value-label">{{ t('named.target') }}</span><span>{{ displayValue(difference.version) }}</span></dd>
        </template>
      </dl>
      <NPopconfirm :positive-text="t('named.promote')" @positive-click="emit('action', 'promote')">
        <template #trigger><NButton type="warning" :disabled="state.loading" :aria-label="t('named.promote')" data-testid="named-promote">{{ t('named.promote') }}</NButton></template>
        {{ t('named.promoteConfirm') }}
      </NPopconfirm>
    </div>
  </section>
</template>
<style scoped>
.named-revisions { padding-bottom: 14px; margin-bottom: 14px; border-bottom: 1px solid var(--vt-border); }
header, .named-row { display: flex; align-items: center; gap: 8px; margin-block: 8px; }
header { justify-content: space-between; }
p, dd { color: var(--vt-text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.named-differences dt { font-weight: 600; margin-top: 12px; }
.named-differences dd { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 4px 12px; margin: 6px 0 12px; }
.named-value-label { color: var(--vt-text-secondary); }
.named-row > :first-child { min-width: 0; }
</style>
