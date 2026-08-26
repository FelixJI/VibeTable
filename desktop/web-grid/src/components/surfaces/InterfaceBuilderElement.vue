<script setup lang="ts">
import { NButton, NIcon } from "naive-ui";
import { Plus, Trash2 } from "@lucide/vue";
import type { InterfaceElement } from "@/contracts/generated/workbench";

defineOptions({ name: "InterfaceBuilderElement" });
defineProps<{ element: InterfaceElement; selectedId: string | null }>();
const emit = defineEmits<{
  select: [elementId: string];
  remove: [elementId: string];
  addChild: [elementId: string];
}>();
function forwardSelect(id: string): void { emit("select", id); }
function forwardRemove(id: string): void { emit("remove", id); }
function forwardAdd(id: string): void { emit("addChild", id); }
</script>

<template>
  <article
    class="builder-element"
    :class="[
      `builder-element--${element.kind}`,
      `builder-width--${element.width}`,
      { selected: selectedId === element.elementId },
    ]"
    :data-testid="`interface-builder-element-${element.elementId}`"
    tabindex="0"
    @click.stop="emit('select', element.elementId)"
    @keydown.enter.stop="emit('select', element.elementId)"
  >
    <header>
      <span>{{ element.kind }}</span>
      <strong>{{ element.text || element.bindingId || element.actionId || element.elementId }}</strong>
      <NButton
        v-if="['section', 'columns', 'tabs'].includes(element.kind)"
        quaternary
        size="tiny"
        aria-label="添加子元素"
        @click.stop="emit('addChild', element.elementId)"
      ><NIcon><Plus /></NIcon></NButton>
      <NButton quaternary size="tiny" type="error" aria-label="删除元素" @click.stop="emit('remove', element.elementId)"><NIcon><Trash2 /></NIcon></NButton>
    </header>
    <div v-if="element.children.length" class="builder-children">
      <InterfaceBuilderElement
        v-for="child in element.children"
        :key="child.elementId"
        :element="child"
        :selected-id="selectedId"
        @select="forwardSelect"
        @remove="forwardRemove"
        @add-child="forwardAdd"
      />
    </div>
    <p v-else-if="['section', 'columns', 'tabs'].includes(element.kind)" class="drop-hint">添加子元素</p>
  </article>
</template>

<style scoped>
.builder-element { grid-column:span 12; min-width:0; border:1px solid var(--vt-border); border-radius:9px; background:var(--vt-bg); outline:none; transition:border-color .12s,box-shadow .12s; }
.builder-element:hover,.builder-element:focus-visible { border-color:var(--vt-color-primary-300); }.builder-element.selected { border-color:var(--vt-color-primary-500); box-shadow:0 0 0 2px color-mix(in srgb,var(--vt-color-primary-500) 18%,transparent); }
.builder-width--half { grid-column:span 6; }.builder-width--third { grid-column:span 4; }
.builder-element>header { display:grid; grid-template-columns:auto 1fr auto auto; align-items:center; gap:7px; min-height:38px; padding:4px 7px 4px 10px; }
.builder-element>header span { padding:2px 6px; border-radius:4px; background:var(--vt-bg-subtle); color:var(--vt-fg-muted); font:600 10px/1.4 ui-monospace,monospace; text-transform:uppercase; }.builder-element>header strong { overflow:hidden; font-size:12px; text-overflow:ellipsis; white-space:nowrap; }
.builder-children { display:grid; grid-template-columns:repeat(12,minmax(0,1fr)); gap:9px; padding:9px; border-top:1px dashed var(--vt-border); background:var(--vt-bg-sunken); }.drop-hint { margin:0; padding:12px; border-top:1px dashed var(--vt-border); color:var(--vt-fg-muted); font-size:11px; text-align:center; }
@media (max-width:720px) { .builder-width--half,.builder-width--third { grid-column:span 12; } }
</style>
