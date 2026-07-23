<script setup lang="ts">
import { computed } from "vue";
import { useTableStore } from "@/stores/tableStore";

const store = useTableStore();
const groups = computed(() => store.lookupGroups);

function pathLabel(path: readonly unknown[]): string {
  return path.map((part) => {
    if (!part || typeof part !== "object") return String(part ?? "—");
    const record = part as Record<string, unknown>;
    return `${String(record.fieldRef ?? "group")}: ${String(record.key ?? "—")}`;
  }).join(" / ");
}

function aggregateLabel(values: Readonly<Record<string, unknown>>): string {
  return Object.entries(values)
    .map(([key, value]) => `${key}: ${String(value ?? "—")}`)
    .join(" · ");
}
</script>

<template>
  <section v-if="groups.length" class="lookup-groups" aria-label="服务端分组结果">
    <div class="lookup-groups__title">服务端分组</div>
    <ol class="lookup-groups__list">
      <li
        v-for="(group, index) in groups"
        :key="`${index}:${group.childCursor ?? ''}`"
        class="lookup-groups__item"
        :style="{ paddingInlineStart: `${Math.max(group.path.length - 1, 0) * 18 + 8}px` }"
        :data-child-cursor="group.childCursor ?? undefined"
      >
        <span class="lookup-groups__path">{{ pathLabel(group.path) }}</span>
        <span class="lookup-groups__count">{{ group.count }}</span>
        <span v-if="Object.keys(group.aggregates).length" class="lookup-groups__aggregates">
          {{ aggregateLabel(group.aggregates) }}
        </span>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.lookup-groups {
  flex: 0 0 auto;
  max-height: 180px;
  overflow: auto;
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
.lookup-groups__title {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: 6px 8px;
  color: var(--vt-fg-secondary);
  background: var(--vt-bg-subtle);
  font-size: var(--vt-font-caption);
  font-weight: 700;
}
.lookup-groups__list { margin: 0; padding: 0; list-style: none; }
.lookup-groups__item {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 28px;
  padding-block: 4px;
  padding-right: 8px;
  border-top: 1px solid color-mix(in srgb, var(--vt-border) 70%, transparent);
  font-size: var(--vt-font-caption);
}
.lookup-groups__path { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lookup-groups__count {
  flex: 0 0 auto;
  padding: 1px 6px;
  border-radius: 999px;
  color: var(--vt-fg-secondary);
  background: var(--vt-bg-sunken);
  font-variant-numeric: tabular-nums;
}
.lookup-groups__aggregates { margin-left: auto; color: var(--vt-fg-muted); white-space: nowrap; }
</style>
