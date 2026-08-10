<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { NAlert, NButton, NIcon, NTag } from "naive-ui";
import {
  ChevronDown,
  ChevronRight,
  GitBranch,
  RotateCcw,
  Sparkles,
} from "lucide-vue-next";
import { useUiStore } from "@/stores/uiStore";
import type { FileRevisionTreeProjection } from "@/stores/workspaceProtectionStore";
import type { FileRevisionV2 } from "@/contracts/workspaceV2";
import { t } from "@/i18n";
import type { DocumentDiffCompletedPayload } from "@/contracts";
import type { DocumentDiffPhase } from "@/stores/documentWorkspaceStore";

interface TreeRow {
  readonly revision: FileRevisionV2;
  readonly level: number;
  readonly hasChildren: boolean;
  readonly expanded: boolean;
  readonly effective: boolean;
  readonly onEffectivePath: boolean;
}

const props = withDefaults(defineProps<{
  tree: FileRevisionTreeProjection | null;
  busy: boolean;
  canCompare?: boolean;
  diffPhase?: DocumentDiffPhase;
  diffResult?: DocumentDiffCompletedPayload | null;
}>(), {
  canCompare: false,
  diffPhase: "idle",
  diffResult: null,
});
const emit = defineEmits<{
  restore: [revision: FileRevisionV2];
  upgrade: [revision: FileRevisionV2];
  activate: [revision: FileRevisionV2];
  compare: [revision: FileRevisionV2];
  cancelCompare: [];
}>();

const ui = useUiStore();
const expanded = ref<ReadonlySet<string>>(new Set());
const collapsed = ref<ReadonlySet<string>>(new Set());
const showAutosaves = ref(false);
const focusedRevisionId = ref<string | null>(null);

const childMap = computed(() => {
  const map = new Map<string | null, FileRevisionV2[]>();
  for (const revision of props.tree?.revisions ?? []) {
    const siblings = map.get(revision.parentRevisionId) ?? [];
    siblings.push(revision);
    map.set(revision.parentRevisionId, siblings);
  }
  for (const children of map.values()) {
    children.sort((left, right) => {
      const leftOrdinal = left.revisionOrdinal || Number.MAX_SAFE_INTEGER;
      const rightOrdinal = right.revisionOrdinal || Number.MAX_SAFE_INTEGER;
      return leftOrdinal - rightOrdinal
        || (left.localSequence ?? 0) - (right.localSequence ?? 0)
        || left.createdAt.localeCompare(right.createdAt)
        || left.revisionId.localeCompare(right.revisionId);
    });
  }
  return map;
});

const effectivePath = computed(() => {
  const path = new Set<string>();
  const revisions = new Map((props.tree?.revisions ?? []).map((revision) => [revision.revisionId, revision]));
  let cursor = props.tree?.effectiveRevisionId ?? null;
  while (cursor && !path.has(cursor)) {
    path.add(cursor);
    cursor = revisions.get(cursor)?.parentRevisionId ?? null;
  }
  return path;
});

const hasBranch = computed(() =>
  [...childMap.value.values()].some((children) => children.length > 1));

const rows = computed<TreeRow[]>(() => {
  const result: TreeRow[] = [];
  const walk = (revision: FileRevisionV2, level: number): void => {
    const children = childMap.value.get(revision.revisionId) ?? [];
    const forceVisible = isProvisional(revision)
      || revision.kind !== "autosave"
      || revision.revisionId === props.tree?.effectiveRevisionId
      || children.length > 1;
    if (showAutosaves.value || forceVisible) {
      const rowExpanded = expanded.value.has(revision.revisionId)
        || effectivePath.value.has(revision.revisionId);
      const visibleExpanded = children.length > 0
        && rowExpanded
        && !collapsed.value.has(revision.revisionId);
      result.push({
        revision,
        level,
        hasChildren: children.length > 0,
        expanded: visibleExpanded,
        effective: revision.revisionId === props.tree?.effectiveRevisionId,
        onEffectivePath: effectivePath.value.has(revision.revisionId),
      });
      if (visibleExpanded) {
        for (const child of children) walk(child, level + 1);
      }
      return;
    }
    for (const child of children) walk(child, level);
  };
  for (const root of childMap.value.get(null) ?? []) walk(root, 1);
  return result;
});

watch(rows, (next) => {
  if (!next.some((row) => row.revision.revisionId === focusedRevisionId.value)) {
    focusedRevisionId.value =
      next.find((row) => row.effective)?.revision.revisionId
      ?? next[0]?.revision.revisionId
      ?? null;
  }
}, { immediate: true });

function toggle(revisionId: string): void {
  const nextExpanded = new Set(expanded.value);
  const nextCollapsed = new Set(collapsed.value);
  const row = rows.value.find((item) => item.revision.revisionId === revisionId);
  if (row?.expanded) {
    nextExpanded.delete(revisionId);
    nextCollapsed.add(revisionId);
  } else {
    nextCollapsed.delete(revisionId);
    nextExpanded.add(revisionId);
  }
  expanded.value = nextExpanded;
  collapsed.value = nextCollapsed;
}

function focusRow(index: number): void {
  const row = rows.value[index];
  if (!row) return;
  focusedRevisionId.value = row.revision.revisionId;
  void nextTick(() =>
    document.querySelector<HTMLElement>(`[data-revision-id="${row.revision.revisionId}"]`)?.focus());
}

function onKeydown(event: KeyboardEvent, row: TreeRow, index: number): void {
  if (event.key === "ArrowDown") focusRow(Math.min(rows.value.length - 1, index + 1));
  else if (event.key === "ArrowUp") focusRow(Math.max(0, index - 1));
  else if (event.key === "Home") focusRow(0);
  else if (event.key === "End") focusRow(rows.value.length - 1);
  else if (event.key === "ArrowRight" && row.hasChildren) {
    if (!row.expanded) toggle(row.revision.revisionId);
    else focusRow(index + 1);
  } else if (event.key === "ArrowLeft") {
    if (row.expanded) {
      toggle(row.revision.revisionId);
    } else if (row.revision.parentRevisionId) {
      for (let parentIndex = index - 1; parentIndex >= 0; parentIndex -= 1) {
        if ((rows.value[parentIndex]?.level ?? row.level) < row.level) {
          focusRow(parentIndex);
          break;
        }
      }
    } else {
      return;
    }
  }
  else return;
  event.preventDefault();
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(ui.locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function isProvisional(revision: FileRevisionV2): boolean {
  return revision.revisionOrdinal === 0;
}

function revisionLabel(revision: FileRevisionV2): string {
  if (revision.formalVersion !== null) return `V${revision.formalVersion}`;
  if (isProvisional(revision)) return `p${revision.localSequence}`;
  return `r${revision.revisionOrdinal}`;
}

const diffMessage = computed(() => {
  const result = props.diffResult;
  if (!result) return props.diffPhase === "failed"
    ? t("workspaceV2.fileTree.diff.failure.generic")
    : null;
  if (result.outcome === "identical") return t("workspaceV2.fileTree.diff.identical");
  if (result.outcome === "changed") return t("workspaceV2.fileTree.diff.changed");
  if (result.outcome === "changedWithDetails") {
    return t("workspaceV2.fileTree.diff.details", {
      added: result.addedLines ?? 0,
      removed: result.removedLines ?? 0,
    });
  }
  return t(`workspaceV2.fileTree.diff.failure.${result.failure ?? "generic"}`);
});
</script>

<template>
  <section class="file-revision-tree" data-testid="file-revision-tree">
    <header>
      <div>
        <strong>{{ t("workspaceV2.fileTree.title") }}</strong>
        <small>{{ t("workspaceV2.fileTree.hint") }}</small>
      </div>
      <NButton
        v-if="tree?.revisions.some(revision => revision.kind === 'autosave')"
        size="tiny"
        quaternary
        :aria-pressed="showAutosaves"
        @click="showAutosaves = !showAutosaves"
      >
        {{ showAutosaves ? t("workspaceV2.fileTree.hideAutosaves") : t("workspaceV2.fileTree.showAutosaves") }}
      </NButton>
    </header>

    <NAlert
      v-if="diffPhase === 'busy'"
      type="info"
      :show-icon="true"
      data-testid="diff-busy"
    >
      <div class="diff-busy-content">
        <span>{{ t("workspaceV2.fileTree.diff.busy") }}</span>
        <NButton size="tiny" data-testid="diff-cancel" @click="emit('cancelCompare')">
          {{ t("common.cancel") }}
        </NButton>
      </div>
    </NAlert>
    <NAlert
      v-else-if="diffMessage"
      :type="diffPhase === 'failed' ? 'warning' : 'success'"
      :show-icon="true"
      data-testid="diff-result"
    >
      {{ diffMessage }}
    </NAlert>

    <div v-if="!tree?.revisions.length" class="tree-empty">
      <GitBranch :size="23" />
      <strong>{{ t("workspaceV2.fileTree.empty") }}</strong>
      <p>{{ t("workspaceV2.fileTree.emptyHint") }}</p>
    </div>

    <div
      v-else
      class="tree-rows"
      role="tree"
      :aria-label="t('workspaceV2.fileTree.title')"
      :data-has-branch="hasBranch"
    >
      <div
        v-for="(row, index) in rows"
        :key="row.revision.revisionId"
        class="tree-row"
        :class="{
          effective: row.effective,
          provisional: isProvisional(row.revision),
          'effective-path': row.onEffectivePath,
        }"
        role="treeitem"
        :aria-level="row.level"
        :aria-expanded="row.hasChildren ? row.expanded : undefined"
        :aria-current="row.effective ? 'true' : undefined"
        :tabindex="focusedRevisionId === row.revision.revisionId ? 0 : -1"
        :data-revision-id="row.revision.revisionId"
        :style="{ '--tree-level': row.level }"
        @focus="focusedRevisionId = row.revision.revisionId"
        @keydown="onKeydown($event, row, index)"
      >
        <button
          class="tree-toggle"
          :class="{ hidden: !row.hasChildren }"
          :aria-label="row.expanded ? t('workspaceV2.fileTree.collapse') : t('workspaceV2.fileTree.expand')"
          :tabindex="-1"
          @click="toggle(row.revision.revisionId)"
          @keydown.stop
        >
          <ChevronDown v-if="row.expanded" :size="13" />
          <ChevronRight v-else :size="13" />
        </button>
        <span class="tree-node" aria-hidden="true"></span>
        <div class="tree-copy">
          <span>
            <strong>
              {{ revisionLabel(row.revision) }}
            </strong>
            <NTag v-if="row.effective" size="small" type="success">{{ t("workspaceV2.fileTree.effective") }}</NTag>
            <NTag v-if="isProvisional(row.revision)" size="small" type="warning">{{ t("workspaceV2.fileTree.provisional") }}</NTag>
            <NTag v-else-if="row.revision.kind === 'restore'" size="small" type="warning">{{ t("workspaceV2.fileTree.restored") }}</NTag>
            <NTag v-else-if="row.revision.kind === 'autosave'" size="small">{{ t("workspaceV2.fileTree.autosave") }}</NTag>
          </span>
          <small>{{ formatDate(row.revision.createdAt) }} · {{ row.revision.createdBy }}</small>
          <p v-if="row.revision.comment">{{ row.revision.comment }}</p>
        </div>
        <div class="tree-actions">
          <NButton
            v-if="canCompare && !isProvisional(row.revision) && !row.effective"
            size="tiny"
            quaternary
            :loading="diffPhase === 'busy'"
            :disabled="busy || diffPhase === 'busy'"
            data-testid="compare-revision"
            @click="emit('compare', row.revision)"
            @keydown.stop
          >
            {{ t("workspaceV2.fileTree.diff.compare") }}
          </NButton>
          <NButton
            v-if="!isProvisional(row.revision) && row.hasChildren"
            size="tiny"
            quaternary
            :disabled="busy"
            @click="emit('upgrade', row.revision)"
            @keydown.stop
          >
            <template #icon><NIcon><Sparkles /></NIcon></template>
            {{ t("workspaceV2.fileTree.upgrade") }}
          </NButton>
          <NButton
            v-else-if="!isProvisional(row.revision) && !row.effective"
            size="tiny"
            quaternary
            type="warning"
            :disabled="busy"
            @click="emit('restore', row.revision)"
            @keydown.stop
          >
            <template #icon><NIcon><RotateCcw /></NIcon></template>
            {{ t("workspaceV2.fileTree.restore") }}
          </NButton>
          <NButton
            v-if="!isProvisional(row.revision) && !row.hasChildren && !row.effective"
            size="tiny"
            quaternary
            :disabled="busy"
            @click="emit('activate', row.revision)"
            @keydown.stop
          >
            {{ t("workspaceV2.fileTree.activate") }}
          </NButton>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.file-revision-tree { min-height: 0; }
.file-revision-tree > header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
.file-revision-tree > .n-alert { margin: 10px 12px 0; }
.diff-busy-content { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.file-revision-tree > header > div { display: flex; min-width: 0; flex-direction: column; }
.file-revision-tree header strong { font-weight: 580; }
.file-revision-tree header small { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.tree-empty { display: flex; min-height: 240px; align-items: center; justify-content: center; flex-direction: column; color: var(--vt-fg-muted); text-align: center; }
.tree-empty strong { margin-top: 8px; color: var(--vt-fg-secondary); }
.tree-empty p { max-width: 280px; margin: 4px 0; font-size: var(--vt-font-caption); }
.tree-rows { padding: 10px 0 18px; }
.tree-row {
  position: relative;
  display: grid;
  grid-template-columns: 18px 14px minmax(100px, 1fr) auto;
  align-items: start;
  gap: 5px;
  min-height: 62px;
  padding: 8px 10px 8px calc(10px + (var(--tree-level) - 1) * 18px);
  outline: none;
}
.tree-row:focus-visible { box-shadow: inset 0 0 0 2px var(--vt-focus-ring); }
.tree-row.effective { background: var(--vt-color-primary-50); }
:root.dark .tree-row.effective { background: rgba(91, 139, 255, .12); }
.tree-toggle {
  display: grid;
  place-items: center;
  width: 18px;
  height: 18px;
  padding: 0;
  color: var(--vt-fg-muted);
  border: 0;
  border-radius: 4px;
  background: transparent;
  cursor: pointer;
}
.tree-toggle:hover { background: var(--vt-bg-sunken); }
.tree-toggle.hidden { visibility: hidden; }
.tree-node {
  position: relative;
  width: 9px;
  height: 9px;
  margin-top: 4px;
  border: 2px solid var(--vt-border-strong);
  border-radius: 50%;
  background: var(--vt-bg);
}
.tree-row.effective-path .tree-node { border-color: var(--vt-color-primary-500); }
.tree-row.effective-path .tree-node::after {
  position: absolute;
  top: 7px;
  left: 2px;
  width: 1px;
  height: 51px;
  background: var(--vt-color-primary-200);
  content: "";
}
.tree-copy { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.tree-copy > span { display: flex; align-items: center; flex-wrap: wrap; gap: 5px; }
.tree-copy strong { font-size: var(--vt-font-body); font-weight: 600; }
.tree-copy small,
.tree-copy p { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.tree-copy p { margin: 1px 0 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tree-actions { display: flex; align-items: center; justify-content: flex-end; gap: 2px; }
@media (max-width: 480px) {
  .tree-row { grid-template-columns: 16px 12px minmax(80px, 1fr); }
  .tree-actions { grid-column: 3; justify-content: flex-start; }
}
</style>
