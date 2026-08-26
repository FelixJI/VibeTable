<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { GridStack, type GridStackNode } from "gridstack";
import "gridstack/dist/gridstack.min.css";
import type { DashboardPanel, PanelPosition, ProductPanelType } from "@/dashboard";
import type { DashboardManifestEntryPayload } from "@/contracts";
import { adjustPositionWithKeyboard, type LayoutArrow } from "@/dashboard";
import type { DashboardPanelData } from "@/stores/dashboardStore";
import DashboardPanelView from "./DashboardPanel.vue";
import { t } from "@/i18n";

const props = defineProps<{
  panels: readonly DashboardPanel[];
  data: Readonly<Record<string, DashboardPanelData>>;
  editing: boolean;
  manifest: Partial<Record<ProductPanelType, DashboardManifestEntryPayload>>;
}>();
const emit = defineEmits<{
  layout: [panelId: string, position: PanelPosition];
  remove: [panelId: string];
  edit: [panel: DashboardPanel];
  refresh: [panelId: string];
  exportCsv: [panel: DashboardPanel];
  exportPng: [panel: DashboardPanel];
  select: [panel: DashboardPanel, value: unknown];
  visibility: [panelIds: readonly string[]];
}>();
const root = ref<HTMLElement | null>(null);
const visible = ref(new Set<string>());
let grid: GridStack | null = null;
let resizeObserver: ResizeObserver | null = null;
let intersectionObserver: IntersectionObserver | null = null;
let narrow = false;

function initialize(): void {
  if (!root.value) return;
  grid?.destroy(false);
  const initializedGrid = GridStack.init({
    column: 12,
    cellHeight: 72,
    margin: 8,
    float: false,
    animate: true,
    disableDrag: !props.editing,
    disableResize: !props.editing,
    draggable: { handle: ".panel-drag" },
    resizable: { handles: "e,se,s,sw,w" },
  }, root.value);
  if (!initializedGrid) return;
  grid = initializedGrid;
  initializedGrid.on("change", (_event, items) => {
    if (!props.editing || narrow) return;
    for (const item of items) emitNode(item);
  });
  observeVisibility();
  adaptColumns(root.value.clientWidth);
}

function emitNode(item: GridStackNode): void {
  const panelId = item.el?.getAttribute("data-panel-id");
  if (!panelId || item.x === undefined || item.y === undefined ||
      item.w === undefined || item.h === undefined) return;
  emit("layout", panelId, { x: item.x, y: item.y, width: item.w, height: item.h });
}

function adaptColumns(width: number): void {
  const nextNarrow = width < 760;
  if (nextNarrow === narrow || !grid) return;
  narrow = nextNarrow;
  grid.column(narrow ? 1 : 12, "list");
  grid.enableMove(props.editing && !narrow);
  grid.enableResize(props.editing && !narrow);
}

function keyboardLayout(event: KeyboardEvent, panel: DashboardPanel): void {
  if (!props.editing || !panel.editable || !event.altKey ||
      !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
  event.preventDefault();
  const position = adjustPositionWithKeyboard(panel.position, event.key as LayoutArrow, event.shiftKey);
  const element = root.value?.querySelector<HTMLElement>(`[data-panel-id="${CSS.escape(panel.id)}"]`);
  if (element && grid && !narrow) {
    grid.update(element, { x: position.x, y: position.y, w: position.width, h: position.height });
  }
  emit("layout", panel.id, position);
}

function minimum(panel: DashboardPanel): { width: number; height: number } {
  if (panel.productType === "custom" || panel.productType === "unknown") return { width: 1, height: 1 };
  const value = props.manifest[panel.productType]?.minSize;
  return { width: value?.width ?? 1, height: value?.height ?? 1 };
}

function observeVisibility(): void {
  intersectionObserver?.disconnect();
  intersectionObserver = new IntersectionObserver((entries) => {
    const next = new Set(visible.value);
    for (const entry of entries) {
      const panelId = (entry.target as HTMLElement).dataset.panelId;
      if (!panelId) continue;
      if (entry.isIntersecting) next.add(panelId);
      else next.delete(panelId);
    }
    visible.value = next;
    emit("visibility", [...next]);
  }, { root: root.value?.parentElement, rootMargin: "300px" });
  root.value?.querySelectorAll<HTMLElement>(".grid-stack-item").forEach((item) => intersectionObserver?.observe(item));
}

watch(() => props.editing, async (editing) => {
  await nextTick();
  if (!editing) initialize();
  else {
    grid?.enableMove(!narrow);
    grid?.enableResize(!narrow);
  }
});
watch(() => props.panels.map((panel) => panel.id).join("|"), async () => {
  await nextTick();
  initialize();
});

onMounted(() => {
  initialize();
  if (root.value) {
    resizeObserver = new ResizeObserver((entries) => adaptColumns(entries[0]?.contentRect.width ?? 0));
    resizeObserver.observe(root.value);
  }
});
onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  intersectionObserver?.disconnect();
  grid?.destroy(false);
  grid = null;
});
</script>

<template>
  <div ref="root" class="grid-stack dashboard-grid" :class="{ 'dashboard-grid--editing': editing }" data-testid="dashboard-grid">
    <div
      v-for="panel in panels"
      :key="panel.id"
      class="grid-stack-item"
      :data-panel-id="panel.id"
      :gs-id="panel.id"
      :gs-x="panel.position.x"
      :gs-y="panel.position.y"
      :gs-w="panel.position.width"
      :gs-h="panel.position.height"
      :gs-min-w="minimum(panel).width"
      :gs-min-h="minimum(panel).height"
      :gs-no-move="!editing || !panel.editable"
      :gs-no-resize="!editing || !panel.editable"
      :tabindex="editing && panel.editable ? 0 : undefined"
      :aria-label="editing && panel.editable ? t('dashboard.layout.keyboard', { name: panel.name }) : undefined"
      @keydown="keyboardLayout($event, panel)"
    >
      <div class="grid-stack-item-content">
        <DashboardPanelView
          :panel="panel"
          :data="data[panel.id] ?? { state: 'idle', rows: [], truncated: false, maxPoints: 1, updatedAt: null, error: null }"
          :editing="editing"
          :visible="visible.has(panel.id)"
          @remove="emit('remove', $event)"
          @edit="emit('edit', $event)"
          @refresh="emit('refresh', $event)"
          @export-csv="emit('exportCsv', $event)"
          @export-png="emit('exportPng', $event)"
          @select="(value) => emit('select', panel, value)"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.dashboard-grid { min-height:100%; padding:8px; }
.grid-stack-item-content { inset:4px; overflow:visible; }
.dashboard-grid--editing :deep(.dashboard-panel) { border-color:color-mix(in srgb,var(--vt-color-primary-500) 55%,var(--vt-border)); }
@media (max-width:760px) { .dashboard-grid { padding:2px; } }
</style>
