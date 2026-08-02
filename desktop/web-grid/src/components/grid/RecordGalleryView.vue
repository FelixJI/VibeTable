<script setup lang="ts">
import { computed, ref } from "vue";
import { GalleryHorizontal } from "lucide-vue-next";
import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";
import { displayValue, metadataFields, rowTitle, safeImageUrl } from "./recordViewUtils";

const props = defineProps<{
  rows: readonly Record<string, unknown>[];
  schema: readonly ColumnSchema[];
  view: PresetView;
}>();

const details = computed(() => metadataFields(
  props.schema,
  [props.view.titleField, props.view.coverField],
  props.view.visibleFields,
));
const failedCovers = ref(new Set<string>());
const cards = computed(() => props.rows.map((row, index) => {
  const key = String(row.rowKey ?? `${index}-${rowTitle(row, props.view)}`);
  const cover = props.view.coverField ? safeImageUrl(row[props.view.coverField]) : null;
  return { row, key, cover };
}));
function markCoverFailed(key: string): void {
  failedCovers.value = new Set([...failedCovers.value, key]);
}
</script>

<template>
  <section class="record-gallery" data-testid="record-gallery-view">
    <header><div><strong>{{ t("views.kind.gallery") }}</strong><small>{{ t("views.gallery.summary", { count: rows.length }) }}</small></div></header>
    <div v-if="cards.length" class="gallery-grid">
      <article v-for="card in cards" :key="card.key" data-testid="gallery-card">
        <div class="gallery-cover">
          <img v-if="card.cover && !failedCovers.has(card.key)" :src="card.cover" alt="" @error="markCoverFailed(card.key)" />
          <span v-else data-testid="gallery-cover-placeholder"><GalleryHorizontal :size="28" /><small>{{ t("views.gallery.noCover") }}</small></span>
        </div>
        <div class="gallery-copy">
          <strong>{{ rowTitle(card.row, view) }}</strong>
          <dl v-if="details.length">
            <template v-for="field in details" :key="field.name">
              <dt>{{ field.title }}</dt><dd>{{ displayValue(card.row[field.name]) }}</dd>
            </template>
          </dl>
        </div>
      </article>
    </div>
    <div v-else class="view-empty"><GalleryHorizontal :size="30" /><strong>{{ t("views.gallery.empty") }}</strong><small>{{ t("views.gallery.emptyHint") }}</small></div>
  </section>
</template>

<style scoped>
.record-gallery { min-height: 0; flex: 1 1 auto; overflow: auto; padding: 16px 18px; background: var(--vt-bg-subtle); }
.record-gallery > header { margin-bottom: 12px; }
.record-gallery > header > div { display: grid; gap: 2px; }
.record-gallery > header strong { font-size: 17px; }
.record-gallery > header small { color: var(--vt-fg-muted); }
.gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.gallery-grid article { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); box-shadow: var(--vt-shadow-1); }
.gallery-cover { aspect-ratio: 16 / 9; overflow: hidden; background: linear-gradient(145deg, var(--vt-color-primary-50), var(--vt-bg-sunken)); }
.gallery-cover img { width: 100%; height: 100%; object-fit: cover; }
.gallery-cover > span { display: grid; height: 100%; place-items: center; align-content: center; gap: 6px; color: var(--vt-fg-muted); }
.gallery-copy { display: grid; gap: 10px; padding: 12px 13px; }
.gallery-copy > strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
dl { display: grid; grid-template-columns: minmax(52px, auto) minmax(0, 1fr); gap: 4px 8px; margin: 0; font-size: 11px; }
dt { color: var(--vt-fg-muted); }
dd { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.view-empty { display: grid; min-height: 260px; place-items: center; align-content: center; gap: 7px; color: var(--vt-fg-muted); text-align: center; }
</style>
