<script setup lang="ts">
import { NDrawer, NDrawerContent } from "naive-ui";
import type { ColumnSchema, LookupDefinition, NormalizedRelationDescriptor } from "@/contracts";
import RecordFieldValue from "./RecordFieldValue.vue";
import { t } from "@/i18n";
defineProps<{
  row: Record<string, unknown> | null;
  columns: readonly ColumnSchema[];
  lookups?: readonly LookupDefinition[];
  relations?: readonly NormalizedRelationDescriptor[];
}>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <NDrawer :show="row !== null" width="min(520px, calc(100vw - 24px))" :mask-closable="true" @update:show="show => { if (!show) emit('close'); }">
    <NDrawerContent closable :title="t('recordDetails.title')" @close="emit('close')">
      <dl v-if="row" class="record-details" data-testid="record-details">
        <div v-for="column in columns" :key="column.name">
          <dt>{{ column.title }}</dt>
          <dd><RecordFieldValue :value="row[column.name]" :column="column" :row="row" :lookups="lookups" :relations="relations" /></dd>
        </div>
      </dl>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.record-details { margin: 0; }
.record-details > div { padding: 12px 0; border-bottom: 1px solid var(--vt-border); }
dt { margin-bottom: 6px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
dd { margin: 0; min-height: 20px; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
