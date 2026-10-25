<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NInput } from "naive-ui";
import type { ColumnSchema } from "@/contracts";
import { t } from "@/i18n";

const props = defineProps<{ columns: readonly ColumnSchema[] }>();
const emit = defineEmits<{ edit: [fieldId: string]; create: [] }>();
const search = ref("");
const types = new Set([
  "text", "integer", "decimal", "boolean", "date", "datetime", "time", "json",
  "relation", "lookup", "formula", "attachment", "select", "multiSelect",
  "autoNumber", "autoDate", "editor", "email", "url",
]);
const fields = computed(() => props.columns.filter(column =>
  column.title.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase())));
function typeLabel(column: ColumnSchema): string {
  const type = column.kind && column.kind !== "scalar" && column.kind !== "system"
    ? column.kind : column.display?.kind ?? column.dataType;
  const known = types.has(type) ? type : types.has(column.dataType) ? column.dataType : null;
  return known ? t(`fieldManager.type.${known}`) : type;
}
function canConfigure(column: ColumnSchema): boolean {
  return !!column.fieldId && column.fieldId !== "id" && column.name !== "id"
    && column.kind !== "system";
}
</script>

<template>
  <section class="field-management" data-testid="field-management-view" :aria-label="t('fieldManager.title')">
    <header><div><h2>{{ t('fieldManager.title') }}</h2><p>{{ t('fieldManager.description', { count: columns.length }) }}</p></div><NButton type="primary" data-testid="field-manager-create" @click="emit('create')">{{ t('fieldManager.create') }}</NButton></header>
    <NInput v-model:value="search" :placeholder="t('fieldManager.search')" :aria-label="t('fieldManager.search')" clearable />
    <div class="field-list">
      <table><thead><tr><th>{{ t('createTable.fieldName') }}</th><th>{{ t('fieldManager.type') }}</th><th>{{ t('schema.required') }}</th><th>{{ t('fieldManager.access') }}</th><th>{{ t('fieldManager.actions') }}</th></tr></thead>
        <tbody><tr v-for="column in fields" :key="column.name">
          <td>{{ column.title }}</td><td>{{ typeLabel(column) }}</td><td>{{ t(column.nullable === false ? 'fieldManager.yes' : 'fieldManager.no') }}</td><td>{{ t(column.editable ? 'fieldManager.editable' : 'workspaceV2.switch.readOnly') }}</td>
          <td><NButton size="small" :disabled="!canConfigure(column)" :aria-label="t('fieldManager.edit', { name: column.title })" @click="canConfigure(column) && column.fieldId && emit('edit', column.fieldId)">{{ t('nav.settings') }}</NButton></td>
        </tr></tbody>
      </table>
      <p v-if="!fields.length" class="empty">{{ t(columns.length ? 'fieldManager.noMatch' : 'fieldManager.empty') }}</p>
    </div>
  </section>
</template>

<style scoped>
.field-management { display: flex; flex: 1; min-width: 0; min-height: 0; flex-direction: column; gap: 16px; padding: 20px; overflow: hidden; }
header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
h2 { margin: 0; font-size: 18px; } p { margin: 4px 0 0; color: var(--vt-fg-muted); }
.field-list { min-height: 0; overflow: auto; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); }
table { width: 100%; border-collapse: collapse; text-align: left; white-space: nowrap; }
th, td { padding: 10px 16px; border-bottom: 1px solid var(--vt-border); }
th { position: sticky; top: 0; background: var(--vt-bg-subtle); } tr:last-child td { border-bottom: 0; }
.empty { padding: 24px; text-align: center; }
</style>
