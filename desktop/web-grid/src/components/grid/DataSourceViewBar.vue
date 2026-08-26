<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NDropdown, NIcon, NInput, NModal, NSelect, NTag } from "naive-ui";
import {
  CalendarDays,
  Check,
  GalleryHorizontal,
  GanttChart,
  Kanban,
  MoreHorizontal,
  Plus,
  Table2,
} from "@lucide/vue";
import type { PresetEntry } from "@/contracts";
import { t } from "@/i18n";

const props = defineProps<{
  collection: string;
  views: readonly PresetEntry[];
  activeId: string | null;
  loading: boolean;
  dirty: boolean;
  error?: string | null;
  dateFields?: readonly { label: string; value: string }[];
  titleFields?: readonly { label: string; value: string }[];
  groupFields?: readonly { label: string; value: string }[];
  coverFields?: readonly { label: string; value: string }[];
}>();
interface CreateViewRequest {
  readonly name: string;
  readonly kind: "table" | "calendar" | "timeline" | "kanban" | "gallery";
  readonly dateField: string | null;
  readonly endDateField: string | null;
  readonly titleField: string | null;
  readonly groupField: string | null;
  readonly coverField: string | null;
}
const emit = defineEmits<{
  create: [request: CreateViewRequest];
  switch: [view: PresetEntry];
  save: [view: PresetEntry];
  duplicate: [view: PresetEntry, name: string];
  rename: [view: PresetEntry, name: string];
  delete: [view: PresetEntry];
  setDefault: [view: PresetEntry];
  reload: [];
}>();

const dialog = ref<"create" | "duplicate" | "rename" | "delete" | null>(null);
const input = ref("");
const target = ref<PresetEntry | null>(null);
const viewKind = ref<CreateViewRequest["kind"]>("table");
const dateField = ref<string | null>(null);
const endDateField = ref<string | null>(null);
const titleField = ref<string | null>(null);
const groupField = ref<string | null>(null);
const coverField = ref<string | null>(null);
const selectableDateFields = computed(() => [...(props.dateFields ?? [])]);
const selectableTitleFields = computed(() => [...(props.titleFields ?? [])]);
const selectableGroupFields = computed(() => [...(props.groupFields ?? [])]);
const selectableCoverFields = computed(() => [...(props.coverFields ?? [])]);
const isTemporalView = computed(() => viewKind.value === "calendar" || viewKind.value === "timeline");
const createConfigurationMissing = computed(() => (
  (isTemporalView.value && !dateField.value)
  || (viewKind.value === "kanban" && !groupField.value)
));
const options = [
  { label: t("views.action.save"), key: "save" },
  { label: t("views.action.duplicate"), key: "duplicate" },
  { label: t("views.action.rename"), key: "rename" },
  { label: t("views.action.default"), key: "default" },
  { label: t("views.action.delete"), key: "delete" },
];

watch(() => props.collection, () => {
  dialog.value = null;
  target.value = null;
  input.value = "";
});

function open(kind: typeof dialog.value, view: PresetEntry | null = null): void {
  if (props.loading || (view && view.collection !== props.collection)) return;
  dialog.value = kind;
  target.value = view;
  input.value = kind === "rename"
    ? view?.name ?? ""
    : kind === "duplicate"
      ? `${view?.name ?? ""} ${t("views.copySuffix")}`
      : "";
  if (kind === "create") {
    viewKind.value = "table";
    dateField.value = props.dateFields?.[0]?.value ?? null;
    endDateField.value = null;
    titleField.value = props.titleFields?.[0]?.value ?? null;
    groupField.value = props.groupFields?.[0]?.value ?? null;
    coverField.value = null;
  }
}

function menu(key: string, view: PresetEntry): void {
  if (props.loading || view.collection !== props.collection) return;
  if (key === "save") emit("save", view);
  else if (key === "default") emit("setDefault", view);
  else if (key === "duplicate" || key === "rename" || key === "delete") open(key, view);
}

function confirm(): void {
  if (props.loading || (target.value && target.value.collection !== props.collection)) return;
  if (dialog.value === "delete" && target.value) emit("delete", target.value);
  else {
    const name = input.value.trim();
    if (!name) return;
    if (dialog.value === "create") emit("create", {
      name,
      kind: viewKind.value,
      dateField: isTemporalView.value ? dateField.value : null,
      endDateField: viewKind.value === "timeline" ? endDateField.value : null,
      titleField: viewKind.value === "table" ? null : titleField.value,
      groupField: viewKind.value === "kanban" ? groupField.value : null,
      coverField: viewKind.value === "gallery" ? coverField.value : null,
    });
    else if (dialog.value === "duplicate" && target.value) emit("duplicate", target.value, name);
    else if (dialog.value === "rename" && target.value) emit("rename", target.value, name);
  }
  dialog.value = null;
}
</script>

<template>
  <div class="data-source-view-shell">
    <NAlert v-if="error" type="warning" data-testid="view-operation-error">
      <div class="view-error-content">
        <span>{{ error }}</span>
        <NButton size="tiny" data-testid="view-reload" @click="emit('reload')">
          {{ t("views.action.reload") }}
        </NButton>
      </div>
    </NAlert>
    <nav class="data-source-view-bar" :aria-label="t('views.ariaLabel')" data-testid="data-source-view-bar">
    <div class="view-context"><NIcon :size="15"><Table2 /></NIcon><span>{{ t("views.tableType") }}</span></div>
    <div class="view-scroll">
      <button v-if="views.length === 0" class="view-tab active" type="button" data-testid="view-all-records">
        <NIcon :size="14"><Check /></NIcon>{{ t("views.allRecords") }}
      </button>
      <div v-for="view in views" :key="view.id" class="view-tab-wrap">
        <button class="view-tab" :class="{ active: activeId === view.id }" type="button" :disabled="loading" :data-testid="`view-tab-${view.id}`" @click="emit('switch', view)">
          <NIcon v-if="activeId === view.id" :size="14"><Check /></NIcon>
          <span>{{ view.name }}</span>
          <NTag v-if="view.view.isDefault" size="tiny" :bordered="false">{{ t("views.default") }}</NTag>
          <i v-if="dirty && activeId === view.id" :title="t('views.unsaved')"></i>
        </button>
        <NDropdown trigger="click" :disabled="loading" :options="options" @select="key => menu(String(key), view)">
          <NButton quaternary size="tiny" :disabled="loading" :aria-label="t('views.actionsFor', { name: view.name })" :data-testid="`view-actions-${view.id}`"><template #icon><NIcon><MoreHorizontal /></NIcon></template></NButton>
        </NDropdown>
      </div>
    </div>
    <NButton size="tiny" quaternary :disabled="loading || !collection" data-testid="view-create" @click="open('create')">
      <template #icon><NIcon><Plus /></NIcon></template>{{ t("views.create") }}
    </NButton>
    <NModal :show="dialog !== null" preset="card" class="view-dialog" :title="dialog ? t(`views.dialog.${dialog}`) : ''" @update:show="show => { if (!show) dialog = null }">
      <p v-if="dialog === 'delete'">{{ t("views.deleteConfirm", { name: target?.name ?? '' }) }}</p>
      <div v-else class="view-dialog-fields">
        <NInput v-model:value="input" autofocus :placeholder="t('views.namePlaceholder')" @keyup.enter="confirm" />
        <template v-if="dialog === 'create'">
          <div class="view-kind-options" :aria-label="t('views.kind.label')">
            <button type="button" :class="{ active: viewKind === 'table' }" :aria-pressed="viewKind === 'table'" data-testid="view-kind-table" @click="viewKind = 'table'"><Table2 :size="17" />{{ t("views.kind.table") }}</button>
            <button type="button" :class="{ active: viewKind === 'calendar' }" :aria-pressed="viewKind === 'calendar'" data-testid="view-kind-calendar" :disabled="!dateFields?.length" @click="viewKind = 'calendar'"><CalendarDays :size="17" />{{ t("views.kind.calendar") }}</button>
            <button type="button" :class="{ active: viewKind === 'timeline' }" :aria-pressed="viewKind === 'timeline'" data-testid="view-kind-timeline" :disabled="!dateFields?.length" @click="viewKind = 'timeline'"><GanttChart :size="17" />{{ t("views.kind.timeline") }}</button>
            <button type="button" :class="{ active: viewKind === 'kanban' }" :aria-pressed="viewKind === 'kanban'" data-testid="view-kind-kanban" :disabled="!groupFields?.length" @click="viewKind = 'kanban'"><Kanban :size="17" />{{ t("views.kind.kanban") }}</button>
            <button type="button" :class="{ active: viewKind === 'gallery' }" :aria-pressed="viewKind === 'gallery'" data-testid="view-kind-gallery" @click="viewKind = 'gallery'"><GalleryHorizontal :size="17" />{{ t("views.kind.gallery") }}</button>
          </div>
          <div v-if="isTemporalView" class="view-field-options">
            <label><span>{{ t("views.field.date") }}</span><NSelect v-model:value="dateField" :options="selectableDateFields" /></label>
            <label v-if="viewKind === 'timeline'"><span>{{ t("views.field.endDate") }}</span><NSelect v-model:value="endDateField" :options="selectableDateFields" clearable /></label>
            <label><span>{{ t("views.field.title") }}</span><NSelect v-model:value="titleField" :options="selectableTitleFields" clearable /></label>
          </div>
          <div v-else-if="viewKind === 'kanban'" class="view-field-options">
            <label><span>{{ t("views.field.group") }}</span><NSelect v-model:value="groupField" :options="selectableGroupFields" data-testid="view-kanban-group-field" /></label>
            <label><span>{{ t("views.field.title") }}</span><NSelect v-model:value="titleField" :options="selectableTitleFields" clearable data-testid="view-kanban-title-field" /></label>
          </div>
          <div v-else-if="viewKind === 'gallery'" class="view-field-options">
            <label><span>{{ t("views.field.cover") }}</span><NSelect v-model:value="coverField" :options="selectableCoverFields" clearable data-testid="view-gallery-cover-field" /></label>
            <label><span>{{ t("views.field.title") }}</span><NSelect v-model:value="titleField" :options="selectableTitleFields" clearable data-testid="view-gallery-title-field" /></label>
          </div>
          <small v-if="isTemporalView && !dateFields?.length" class="view-kind-hint">{{ t("views.kind.noDateFields") }}</small>
          <small v-if="viewKind === 'kanban' && !groupFields?.length" class="view-kind-hint">{{ t("views.kind.noGroupFields") }}</small>
        </template>
      </div>
      <template #footer><div class="dialog-actions"><NButton @click="dialog = null">{{ t("common.cancel") }}</NButton><NButton :type="dialog === 'delete' ? 'error' : 'primary'" :disabled="dialog !== 'delete' && (!input.trim() || (dialog === 'create' && createConfigurationMissing))" data-testid="view-dialog-confirm" @click="confirm">{{ t("common.confirm") }}</NButton></div></template>
    </NModal>
    </nav>
  </div>
</template>

<style scoped>
.data-source-view-shell { display: flex; min-width: 0; flex-direction: column; }
.view-error-content { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.data-source-view-bar { display: flex; min-width: 0; min-height: 40px; align-items: center; gap: 8px; padding: 5px 10px; border-bottom: 1px solid var(--vt-border); background: color-mix(in srgb, var(--vt-bg-subtle) 76%, var(--vt-bg)); }
.view-context { display: flex; flex: 0 0 auto; align-items: center; gap: 5px; padding-right: 8px; color: var(--vt-fg-muted); border-right: 1px solid var(--vt-border); font-size: var(--vt-font-caption); }
.view-scroll { display: flex; min-width: 0; flex: 1 1 auto; align-items: center; gap: 4px; overflow-x: auto; scrollbar-width: thin; }
.view-tab-wrap { display: flex; flex: 0 0 auto; align-items: center; border: 1px solid transparent; border-radius: var(--vt-radius-md); }
.view-tab-wrap:has(.view-tab.active) { border-color: color-mix(in srgb, var(--vt-color-primary-500) 24%, var(--vt-border)); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.view-tab { display: flex; height: 28px; max-width: 220px; align-items: center; gap: 5px; padding: 0 8px; overflow: hidden; color: var(--vt-fg-muted); border: 0; border-radius: var(--vt-radius-sm); background: transparent; font: inherit; font-size: var(--vt-font-caption); cursor: pointer; white-space: nowrap; }
.view-tab:hover { color: var(--vt-fg); background: var(--vt-bg-sunken); }
.view-tab.active { color: var(--vt-fg-accent-strong); font-weight: 600; }
.view-tab span { overflow: hidden; text-overflow: ellipsis; }
.view-tab i { width: 6px; height: 6px; flex: 0 0 auto; border-radius: 50%; background: var(--vt-color-warning); }
.dialog-actions { display: flex; justify-content: flex-end; gap: 8px; }
.view-dialog-fields { display: grid; gap: 14px; }
.view-kind-options { display: grid; grid-template-columns: repeat(5, minmax(76px, 1fr)); gap: 8px; }
.view-kind-options button { display: flex; min-height: 64px; flex-direction: column; align-items: center; justify-content: center; gap: 6px; color: var(--vt-fg-muted); border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg); cursor: pointer; }
.view-kind-options button.active { color: var(--vt-fg-accent-strong); border-color: var(--vt-color-primary-500); background: var(--vt-color-primary-50); }
.view-kind-options button:disabled { opacity: .45; cursor: not-allowed; }
.view-field-options { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.view-field-options label { display: grid; gap: 5px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.view-kind-hint { color: var(--vt-fg-muted); }
:global(.view-dialog) { width: min(560px, calc(100vw - 28px)); }
@media (max-width: 720px) {
  .data-source-view-bar { flex-wrap: wrap; }
  .view-context { border-right: 0; }
  .view-scroll { order: 3; flex-basis: 100%; padding-bottom: 2px; }
  .view-kind-options { grid-template-columns: repeat(2, 1fr); }
}
</style>
