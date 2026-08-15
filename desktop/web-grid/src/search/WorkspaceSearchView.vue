<script setup lang="ts">
import { computed, onMounted } from "vue";
import { NButton, NIcon, NInput, NInputNumber, NProgress, NSelect, NTag } from "naive-ui";
import { FileSearch, Paperclip, RefreshCw, Search, Table2 } from "lucide-vue-next";
import type { SearchFilter, SearchHit, SearchSort } from "@/contracts/generated/workbench";
import { t } from "@/i18n";
import { useWorkspaceSearchStore } from "./workspaceSearchStore";

const emit = defineEmits<{ open: [hit: SearchHit] }>();
const search = useWorkspaceSearchStore();

const kindOptions = computed(() => [
  { label: t("workspaceSearch.kind.all"), value: "all" },
  { label: t("workspaceSearch.kind.record"), value: "record" },
  { label: t("workspaceSearch.kind.attachment"), value: "attachment" },
  { label: t("workspaceSearch.kind.file"), value: "file" },
]);

const selectedKind = computed({
  get: () => {
    const filter = search.filters.find((item) => item.field === "kind" && item.operator === "eq");
    return typeof filter?.value === "string" ? filter.value : "all";
  },
  set: (kind: string) => {
    search.setFilter("kind", "eq", kind === "all" ? null : kind);
  },
});

function stringFilter(
  field: SearchFilter["field"],
  operator: SearchFilter["operator"],
) {
  return computed({
    get: () => String(search.filterValue(field, operator) ?? ""),
    set: (value: string) => search.setFilter(field, operator, value),
  });
}

function numberFilter(operator: "gte" | "lte") {
  return computed({
    get: () => {
      const value = search.filterValue("sizeBytes", operator);
      return typeof value === "number" ? value : null;
    },
    set: (value: number | null) => search.setFilter("sizeBytes", operator, value),
  });
}

const tableFilter = stringFilter("tableId", "contains");
const fieldFilter = stringFilter("fieldId", "contains");
const mimeFilter = stringFilter("mimeType", "contains");
const extensionFilter = computed({
  get: () => String(search.filterValue("extension", "eq") ?? ""),
  set: (value: string) => search.setFilter(
    "extension",
    "eq",
    value.trim().replace(/^\.+/u, ""),
  ),
});
const statusFilter = stringFilter("status", "eq");
const minimumSize = numberFilter("gte");
const maximumSize = numberFilter("lte");
const afterDate = stringFilter("revisionTime", "after");
const beforeDate = stringFilter("revisionTime", "before");

const statusOptions = computed(() => [
  "active", "indexed", "truncated", "unsupported", "failed",
  "passwordProtected", "noTextLayer", "resourceLimited", "cancelled", "deleted",
].map((value) => ({ label: t(`workspaceSearch.status.${value}`), value })));

const sortOptions = computed(() => [
  { label: t("workspaceSearch.sort.score"), value: "score" },
  { label: t("workspaceSearch.sort.revisionTime"), value: "revisionTime" },
  { label: t("workspaceSearch.sort.title"), value: "title" },
  { label: t("workspaceSearch.sort.sizeBytes"), value: "sizeBytes" },
]);

const selectedSort = computed({
  get: () => search.sorts[0]?.field ?? "score",
  set: (field: SearchSort["field"]) => {
    const direction: SearchSort["direction"] = field === "title" ? "asc" : "desc";
    search.sorts = [{ field, direction }];
  },
});

function dateInput(value: string): string {
  return value ? value.replace(/Z$/, "").slice(0, 16) : "";
}

function updateDate(operator: "after" | "before", event: Event): void {
  const value = (event.target as HTMLInputElement).value;
  search.setFilter("revisionTime", operator, value ? `${value}:00.000Z` : null);
}

const progress = computed(() => {
  if (!search.status.total) return 0;
  return Math.min(100, Math.round(search.status.processed * 100 / search.status.total));
});

function iconFor(hit: SearchHit) {
  if (hit.kind === "record") return Table2;
  if (hit.kind === "attachment") return Paperclip;
  return FileSearch;
}

function metadataValue(hit: SearchHit, key: string): string | null {
  const value = hit.metadata.find((item) => item.key === key)?.value;
  return value === null || value === undefined ? null : String(value);
}

function submit(): void {
  void search.search();
}

onMounted(() => void search.refreshStatus());
</script>

<template>
  <main class="search-workspace" data-testid="workspace-search-view">
    <header class="search-hero">
      <div>
        <p class="eyebrow">{{ t("workspaceSearch.eyebrow") }}</p>
        <h1>{{ t("workspaceSearch.title") }}</h1>
        <p>{{ t("workspaceSearch.description") }}</p>
      </div>
      <div class="index-state" :data-state="search.status.state">
        <span></span>
        <div>
          <strong>{{ t(`workspaceSearch.state.${search.status.state}`) }}</strong>
          <small>{{ t("workspaceSearch.generation", { generation: search.status.generation }) }}</small>
        </div>
        <NButton
          size="small"
          quaternary
          data-testid="workspace-search-rebuild"
          @click="search.rebuilding ? search.cancelRebuild() : search.rebuild()"
        >
          <template #icon><NIcon><RefreshCw /></NIcon></template>
          {{ search.rebuilding ? t("workspaceSearch.cancel") : t("workspaceSearch.rebuild") }}
        </NButton>
      </div>
    </header>

    <section class="search-console" aria-labelledby="workspace-search-heading">
      <h2 id="workspace-search-heading" class="sr-only">{{ t("workspaceSearch.form") }}</h2>
      <form class="search-line" @submit.prevent="submit">
        <NInput
          v-model:value="search.query"
          size="large"
          clearable
          autofocus
          :placeholder="t('workspaceSearch.placeholder')"
          data-testid="workspace-search-input"
        >
          <template #prefix><NIcon :size="19"><Search /></NIcon></template>
        </NInput>
        <NButton
          attr-type="submit"
          type="primary"
          size="large"
          :disabled="!search.canSearch"
          :loading="search.searching"
          data-testid="workspace-search-submit"
        >
          {{ t("workspaceSearch.submit") }}
        </NButton>
      </form>

      <div class="search-controls">
        <div class="segmented" :aria-label="t('workspaceSearch.logic')">
          <button
            v-for="value in (['and', 'or'] as const)"
            :key="value"
            type="button"
            :class="{ active: search.logic === value }"
            :aria-pressed="search.logic === value"
            @click="search.logic = value"
          >
            {{ value.toUpperCase() }}
          </button>
        </div>
        <NSelect
          v-model:value="selectedKind"
          class="kind-select"
          data-testid="workspace-search-kind"
          :options="kindOptions"
          :aria-label="t('workspaceSearch.kind.label')"
        />
        <NSelect
          v-model:value="selectedSort"
          class="sort-select"
          data-testid="workspace-search-sort"
          :options="sortOptions"
          :aria-label="t('workspaceSearch.sort.label')"
        />
        <label class="scope-toggle">
          <input v-model="search.scope" data-testid="workspace-search-scope-current" type="radio" value="current" />
          <span>{{ t("workspaceSearch.scope.current") }}</span>
        </label>
        <label class="scope-toggle">
          <input v-model="search.scope" data-testid="workspace-search-scope-history" type="radio" value="history" />
          <span>{{ t("workspaceSearch.scope.history") }}</span>
        </label>
      </div>

      <details class="advanced-filters" data-testid="workspace-search-filters">
        <summary>{{ t("workspaceSearch.filters.title") }}</summary>
        <div class="filter-grid">
          <label>
            <span>{{ t("workspaceSearch.filters.table") }}</span>
            <NInput v-model:value="tableFilter" data-testid="workspace-search-filter-table" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.field") }}</span>
            <NInput v-model:value="fieldFilter" data-testid="workspace-search-filter-field" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.mime") }}</span>
            <NInput v-model:value="mimeFilter" data-testid="workspace-search-filter-mime" placeholder="application/pdf" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.extension") }}</span>
            <NInput v-model:value="extensionFilter" data-testid="workspace-search-filter-extension" placeholder="pdf" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.minimumSize") }}</span>
            <NInputNumber v-model:value="minimumSize" data-testid="workspace-search-filter-minimum-size" :min="0" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.maximumSize") }}</span>
            <NInputNumber v-model:value="maximumSize" data-testid="workspace-search-filter-maximum-size" :min="0" clearable />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.after") }}</span>
            <input
              class="native-input"
              type="datetime-local"
              data-testid="workspace-search-filter-after"
              :value="dateInput(afterDate)"
              @input="updateDate('after', $event)"
            />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.before") }}</span>
            <input
              class="native-input"
              type="datetime-local"
              data-testid="workspace-search-filter-before"
              :value="dateInput(beforeDate)"
              @input="updateDate('before', $event)"
            />
          </label>
          <label>
            <span>{{ t("workspaceSearch.filters.status") }}</span>
            <NSelect v-model:value="statusFilter" data-testid="workspace-search-filter-status" clearable :options="statusOptions" />
          </label>
        </div>
      </details>

      <NProgress
        v-if="search.status.state === 'building'"
        type="line"
        :percentage="progress"
        :indicator-placement="'inside'"
        processing
      />
      <p v-if="search.errorCode" class="search-error" role="alert">
        {{ t("workspaceSearch.error", { code: search.errorCode }) }}
      </p>
    </section>

    <section v-if="search.hits.length" class="result-section" aria-live="polite">
      <div class="result-heading">
        <strong>{{ t("workspaceSearch.results", { count: search.hits.length }) }}</strong>
        <span>{{ t("workspaceSearch.resultHint") }}</span>
      </div>
      <ol class="result-list">
        <li v-for="hit in search.hits" :key="hit.hitId">
          <button
            class="result-card"
            type="button"
            data-testid="workspace-search-result"
            :data-kind="hit.kind"
            @click="emit('open', hit)"
            @keydown.enter.prevent="emit('open', hit)"
          >
            <span class="result-icon"><NIcon :size="19"><component :is="iconFor(hit)" /></NIcon></span>
            <span class="result-copy">
              <span class="result-title">
                <strong>{{ hit.title }}</strong>
                <NTag size="small" :bordered="false">{{ t(`workspaceSearch.kind.${hit.kind}`) }}</NTag>
              </span>
              <span v-if="hit.snippet" class="result-snippet">{{ hit.snippet }}</span>
              <span class="result-meta">
                <span v-if="metadataValue(hit, 'relativePath')">{{ metadataValue(hit, "relativePath") }}</span>
                <span>{{ new Date(hit.revisionTime).toLocaleString() }}</span>
                <span v-if="search.scope === 'history'">{{ hit.sourceRevision }}</span>
              </span>
            </span>
            <span class="result-score">{{ hit.score.toFixed(2) }}</span>
          </button>
        </li>
      </ol>
      <NButton
        v-if="search.nextCursor"
        block
        secondary
        :loading="search.searching"
        data-testid="workspace-search-more"
        @click="search.search({ append: true })"
      >
        {{ t("workspaceSearch.more") }}
      </NButton>
    </section>

    <section v-else-if="search.query && !search.searching && !search.errorCode" class="empty-state">
      <NIcon :size="30"><Search /></NIcon>
      <strong>{{ t("workspaceSearch.empty.title") }}</strong>
      <span>{{ t("workspaceSearch.empty.description") }}</span>
    </section>
    <section v-else-if="!search.query" class="empty-state empty-state--initial">
      <NIcon :size="30"><FileSearch /></NIcon>
      <strong>{{ t("workspaceSearch.initial.title") }}</strong>
      <span>{{ t("workspaceSearch.initial.description") }}</span>
    </section>
  </main>
</template>

<style scoped>
.search-workspace { height: 100%; overflow: auto; padding: clamp(24px, 4vw, 54px); color: var(--vt-fg); background: radial-gradient(circle at 72% -20%, color-mix(in srgb, var(--vt-color-primary-500) 13%, transparent), transparent 42%), var(--vt-bg); }
.search-hero { display: flex; justify-content: space-between; gap: 32px; max-width: 1120px; margin: 0 auto 24px; }
.search-hero h1 { margin: 3px 0 6px; font-size: clamp(28px, 3vw, 42px); letter-spacing: -.04em; }
.search-hero p { max-width: 680px; margin: 0; color: var(--vt-text-secondary); }
.eyebrow { color: var(--vt-color-primary-500) !important; font-size: 11px; font-weight: 800; letter-spacing: .16em; text-transform: uppercase; }
.index-state { display: flex; align-items: center; align-self: flex-end; gap: 10px; min-width: 255px; padding: 11px 12px; border: 1px solid var(--vt-border); border-radius: 14px; background: color-mix(in srgb, var(--vt-bg) 85%, white); box-shadow: var(--vt-shadow-sm); }
.index-state > span { width: 8px; height: 8px; border-radius: 50%; background: #8b949e; }
.index-state[data-state="ready"] > span { background: #22a06b; box-shadow: 0 0 0 4px #22a06b22; }
.index-state[data-state="building"] > span { background: #e5a50a; box-shadow: 0 0 0 4px #e5a50a22; }
.index-state[data-state="failed"] > span, .index-state[data-state="degraded"] > span { background: #d84a4a; }
.index-state div { display: grid; flex: 1; }
.index-state small { color: var(--vt-text-secondary); }
.search-console, .result-section { max-width: 1120px; margin: 0 auto; }
.search-console { padding: 18px; border: 1px solid var(--vt-border); border-radius: 18px; background: color-mix(in srgb, var(--vt-bg) 94%, white); box-shadow: 0 16px 44px #0c1c3010; }
.search-line { display: grid; grid-template-columns: 1fr auto; gap: 10px; }
.search-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-top: 13px; }
.segmented { display: flex; padding: 3px; border: 1px solid var(--vt-border); border-radius: 9px; }
.segmented button { border: 0; border-radius: 6px; padding: 5px 12px; color: var(--vt-text-secondary); background: transparent; cursor: pointer; font: inherit; font-weight: 700; }
.segmented button.active { color: var(--vt-color-primary-500); background: color-mix(in srgb, var(--vt-color-primary-500) 12%, transparent); }
.kind-select { width: 150px; }
.sort-select { width: 165px; }
.scope-toggle { display: flex; align-items: center; gap: 6px; color: var(--vt-text-secondary); cursor: pointer; }
.advanced-filters { margin-top: 13px; border-top: 1px solid var(--vt-border); padding-top: 10px; }
.advanced-filters summary { width: max-content; color: var(--vt-color-primary-500); cursor: pointer; font-size: 12px; font-weight: 700; }
.filter-grid { display: grid; grid-template-columns: repeat(3, minmax(180px, 1fr)); gap: 12px; margin-top: 12px; }
.filter-grid label { display: grid; gap: 5px; color: var(--vt-text-secondary); font-size: 11px; font-weight: 700; }
.native-input { min-height: 34px; padding: 0 10px; border: 1px solid var(--vt-border); border-radius: 3px; color: var(--vt-fg); background: var(--vt-bg); font: inherit; }
.search-error { margin: 13px 0 0; color: var(--vt-color-danger-600); }
.result-section { margin-top: 22px; }
.result-heading { display: flex; justify-content: space-between; margin: 0 2px 10px; color: var(--vt-text-secondary); font-size: 12px; }
.result-heading strong { color: var(--vt-fg); }
.result-list { display: grid; gap: 8px; margin: 0 0 12px; padding: 0; list-style: none; }
.result-card { display: grid; grid-template-columns: auto 1fr auto; gap: 13px; width: 100%; padding: 15px; text-align: left; border: 1px solid var(--vt-border); border-radius: 14px; color: inherit; background: var(--vt-bg); cursor: pointer; transition: transform .14s ease, border-color .14s ease, box-shadow .14s ease; }
.result-card:hover, .result-card:focus-visible { border-color: color-mix(in srgb, var(--vt-color-primary-500) 50%, var(--vt-border)); box-shadow: 0 10px 30px #0c1c3012; transform: translateY(-1px); outline: none; }
.result-icon { display: grid; place-items: center; width: 38px; height: 38px; border-radius: 11px; color: var(--vt-color-primary-500); background: color-mix(in srgb, var(--vt-color-primary-500) 10%, transparent); }
.result-copy, .result-title, .result-meta { display: flex; min-width: 0; }
.result-copy { flex-direction: column; gap: 5px; }
.result-title { align-items: center; gap: 8px; }
.result-title strong, .result-snippet { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.result-snippet { color: var(--vt-text-secondary); }
.result-meta { gap: 12px; color: var(--vt-fg-muted); font-size: 11px; }
.result-score { font-variant-numeric: tabular-nums; color: var(--vt-text-secondary); font-size: 11px; }
.empty-state { display: grid; place-items: center; gap: 7px; max-width: 1120px; min-height: 230px; margin: 22px auto 0; border: 1px dashed var(--vt-border); border-radius: 18px; color: var(--vt-text-secondary); }
.empty-state strong { color: var(--vt-fg); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); }
@media (max-width: 760px) { .search-workspace { padding: 20px 14px; } .search-hero { flex-direction: column; } .index-state { align-self: stretch; } .search-line { grid-template-columns: 1fr; } .filter-grid { grid-template-columns: 1fr; } .result-heading span { display: none; } }
</style>
