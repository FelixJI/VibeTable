<script setup lang="ts">
import { NIcon } from "naive-ui";
import {
  File,
  FileArchive,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileType2,
  AlertTriangle,
} from "@lucide/vue";
import { computed, nextTick, ref, watch, type Component } from "vue";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

const props = defineProps<{
  entries: readonly DocumentEntry[];
  selectedHandles: readonly string[];
  primaryHandle: string | null;
}>();

const emit = defineEmits<{
  select: [index: number, options: { toggle: boolean; range: boolean }];
  open: [entry: DocumentEntry];
  preview: [entry: DocumentEntry];
  context: [entry: DocumentEntry, point: { x: number; y: number }];
  dragOut: [entry: DocumentEntry];
  selectAll: [];
}>();

const listElement = ref<HTMLElement | null>(null);
const focusedHandle = ref<string | null>(null);
const rovingHandle = computed(() => {
  if (focusedHandle.value && props.entries.some(
    (entry) => entry.entryHandle === focusedHandle.value,
  )) return focusedHandle.value;
  if (props.primaryHandle && props.entries.some(
    (entry) => entry.entryHandle === props.primaryHandle,
  )) return props.primaryHandle;
  return props.entries[0]?.entryHandle ?? null;
});

watch(
  [() => props.primaryHandle, () => props.entries],
  ([primaryHandle, entries]) => {
    if (primaryHandle && entries.some((entry) => entry.entryHandle === primaryHandle)) {
      focusedHandle.value = primaryHandle;
    } else if (!entries.some((entry) => entry.entryHandle === focusedHandle.value)) {
      focusedHandle.value = entries[0]?.entryHandle ?? null;
    }
  },
  { immediate: true },
);

function iconFor(entry: DocumentEntry): Component {
  const mime = entry.mimeType ?? "";
  if (mime.startsWith("image/")) return FileImage;
  if (mime.includes("spreadsheet") || mime.includes("excel") || /\.(csv|xlsx?)$/i.test(entry.displayName)) return FileSpreadsheet;
  if (mime.includes("zip") || /\.(zip|7z|rar)$/i.test(entry.displayName)) return FileArchive;
  if (mime.includes("pdf") || mime.startsWith("text/")) return FileText;
  if (/\.(docx?|pptx?)$/i.test(entry.displayName)) return FileType2;
  return File;
}

function onSelect(event: MouseEvent, index: number): void {
  emit("select", index, { toggle: event.ctrlKey || event.metaKey, range: event.shiftKey });
}

function availabilityLabel(entry: DocumentEntry): string {
  if (entry.status === "deleted") return t("files.deleted");
  if (entry.availability === "missing") return t("files.missing");
  if (entry.availability === "unmounted") return t("files.unmounted");
  if (entry.availability === "unmanaged") return t("files.unmanaged");
  if (entry.availability === "unsafe") return t("files.unsafe");
  return t("files.authority.workspace");
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatRevisionTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function onKeydown(event: KeyboardEvent, entry: DocumentEntry, index: number): void {
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const nextIndex = event.key === "ArrowDown"
      ? Math.min(props.entries.length - 1, index + 1)
      : Math.max(0, index - 1);
    if (nextIndex === index) return;
    const nextEntry = props.entries[nextIndex];
    if (!nextEntry) return;
    focusedHandle.value = nextEntry.entryHandle;
    emit("select", nextIndex, { toggle: false, range: false });
    void nextTick(() => {
      listElement.value?.querySelectorAll<HTMLElement>(".document-row")[nextIndex]?.focus();
    });
  } else if ((event.ctrlKey || event.metaKey) && event.key.toLocaleLowerCase() === "a") {
    event.preventDefault();
    emit("selectAll");
  } else if (event.key === "Enter" && entry.capabilities.includes("open")) {
    event.preventDefault();
    emit("open", entry);
  } else if (event.key === " ") {
    event.preventDefault();
    emit("select", index, { toggle: false, range: false });
    if (entry.capabilities.includes("preview")) emit("preview", entry);
  } else if (event.key === "F10" && event.shiftKey) {
    event.preventDefault();
    const target = event.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    emit("context", entry, { x: rect.left + 36, y: rect.top + 24 });
  }
}

function onDragStart(event: DragEvent, entry: DocumentEntry, index: number): void {
  if (!entry.capabilities.includes("dragOut")) {
    event.preventDefault();
    return;
  }
  if (props.primaryHandle !== entry.entryHandle) {
    emit("select", index, { toggle: false, range: false });
  }
  // Never place paths or the capability handle in browser drag data. The host
  // receives the opaque intent and starts the real native FileDrop operation.
  emit("dragOut", entry);
  event.preventDefault();
}
</script>

<template>
  <div ref="listElement" class="document-list" role="grid" :aria-label="t('files.listLabel')">
    <div class="document-head" role="row">
      <span role="columnheader">{{ t("files.column.name") }}</span>
      <span role="columnheader">{{ t("files.column.size") }}</span>
      <span role="columnheader">{{ t("files.column.revisionTime") }}</span>
      <span role="columnheader">{{ t("files.column.version") }}</span>
    </div>
    <button
      v-for="(entry, index) in entries"
      :key="entry.entryHandle"
      type="button"
      role="row"
      class="document-row"
      :class="{
        'document-row--selected': selectedHandles.includes(entry.entryHandle),
        'document-row--missing': !['available', 'remote'].includes(entry.availability),
      }"
      :aria-selected="selectedHandles.includes(entry.entryHandle)"
      :tabindex="entry.entryHandle === rovingHandle ? 0 : -1"
      :draggable="entry.capabilities.includes('dragOut')"
      :title="entry.capabilities.includes('dragOut') ? t('files.dragOut.hint') : undefined"
      :data-testid="`document-row-${entry.entryHandle}`"
      @click="onSelect($event, index)"
      @focus="focusedHandle = entry.entryHandle"
      @dblclick="entry.capabilities.includes('open') && emit('open', entry)"
      @keydown="onKeydown($event, entry, index)"
      @contextmenu.prevent="emit('context', entry, { x: $event.clientX, y: $event.clientY })"
      @dragstart="onDragStart($event, entry, index)"
    >
      <span class="document-name" role="gridcell">
        <NIcon :size="18" class="file-icon"><component :is="iconFor(entry)" /></NIcon>
        <span class="name-copy">
          <strong>{{ entry.displayName }}</strong>
          <small>
            <NIcon v-if="!['available', 'remote'].includes(entry.availability)" :size="12"><AlertTriangle /></NIcon>{{ entry.relativePath }} · {{ availabilityLabel(entry) }}
          </small>
        </span>
      </span>
      <span role="gridcell">{{ formatSize(entry.sizeBytes) }}</span>
      <span role="gridcell">{{ formatRevisionTime(entry.effectiveRevisionCreatedAt) }}</span>
      <span role="gridcell">{{ entry.versionLabel ?? "—" }}</span>
    </button>
  </div>
</template>

<style scoped>
.document-list { min-width: 480px; color: var(--vt-fg); }
.document-head, .document-row {
  display: grid;
  grid-template-columns: minmax(260px, 1fr) 90px 170px 80px;
  align-items: center;
}
.document-head {
  position: sticky;
  z-index: 2;
  top: 0;
  min-height: 32px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
.document-head > span, .document-row > span { padding: 0 10px; }
.document-row {
  width: 100%;
  min-height: 40px;
  padding: 0;
  color: var(--vt-fg-secondary);
  font-size: var(--vt-font-caption);
  text-align: left;
  border: 0;
  border-bottom: 1px solid color-mix(in srgb, var(--vt-border) 70%, transparent);
  background: var(--vt-bg);
  cursor: default;
}
.document-row:hover { background: var(--vt-bg-subtle); }
.document-row[draggable="true"] { cursor: grab; }
.document-row--selected, .document-row--selected:hover { background: var(--vt-color-primary-50); }
:root.dark .document-row--selected, :root.dark .document-row--selected:hover { background: rgba(91, 139, 255, 0.13); }
.document-row:focus-visible { position: relative; z-index: 1; outline: 2px solid var(--vt-color-primary-500); outline-offset: -2px; }
.document-name { display: flex; align-items: center; gap: 9px; min-width: 0; }
.file-icon { flex: 0 0 auto; color: var(--vt-color-primary-500); }
.name-copy { display: flex; flex-direction: column; min-width: 0; padding: 4px 0; }
.name-copy strong { overflow: hidden; color: var(--vt-fg); font-size: var(--vt-font-body); font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
.name-copy small { display: flex; align-items: center; gap: 3px; color: var(--vt-fg-muted); line-height: 1.2; }
.document-row--missing .file-icon, .document-row--missing .name-copy small { color: var(--vt-color-warning); }
</style>
