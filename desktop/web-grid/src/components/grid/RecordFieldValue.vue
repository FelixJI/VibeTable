<script setup lang="ts">
import { computed } from "vue";
import type { ColumnSchema, LookupDefinition } from "@/contracts";
import { enumDisplayParts } from "@/grid/commonFieldDisplay";
import { displayValue } from "./recordViewUtils";
const props = defineProps<{ value: unknown; column: ColumnSchema; lookups?: readonly LookupDefinition[] }>();
const parts = computed(() => props.column.display?.kind === "select" && props.value != null && props.value !== ""
  && (!Array.isArray(props.value) || props.value.length) ? enumDisplayParts(props.value, props.column.enumOptions) : []);
</script>
<template>
  <span v-if="parts.length"><span v-for="(part, index) in parts" :key="index"><span v-if="index">、</span><span :style="{ borderBottom: part.color ? `3px solid ${part.color}` : undefined, marginRight: '6px' }">{{ part.text }}</span></span></span>
  <span v-else>{{ displayValue(value, column, lookups) }}</span>
</template>
