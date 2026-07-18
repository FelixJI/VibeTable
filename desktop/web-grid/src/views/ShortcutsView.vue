<script setup lang="ts">
import { computed } from "vue";
import { NModal, NTag, NH3, NDivider } from "naive-ui";
import {
  SHORTCUTS,
  UNDO_LIMITATIONS_ZH,
  UNDO_LIMITATIONS_EN,
  type ShortcutCategory,
  type ShortcutDef,
} from "@/keyboard/shortcuts";
import { useUiStore } from "@/stores/uiStore";
import { getLocale, t } from "@/i18n";

const ui = useUiStore();

/**
 * Locale-aware accessors. The shortcuts table carries both `descriptionZh` and
 * `descriptionEn` (and `UNDO_LIMITATIONS_ZH` / `_EN` for the notes); pick the
 * one matching the active locale so the help page is not Chinese-only.
 */
function descriptionOf(sc: ShortcutDef): string {
  return getLocale() === "en-US" ? sc.descriptionEn : sc.descriptionZh;
}
const undoLimitations = computed<readonly string[]>(() =>
  getLocale() === "en-US" ? UNDO_LIMITATIONS_EN : UNDO_LIMITATIONS_ZH,
);

const grouped = computed(() => {
  const map = new Map<ShortcutCategory, typeof SHORTCUTS>();
  for (const sc of SHORTCUTS) {
    if (!map.has(sc.category)) map.set(sc.category, [] as unknown as typeof SHORTCUTS);
    (map.get(sc.category) as unknown[]).push(sc);
  }
  return map;
});

function categoryLabel(c: ShortcutCategory): string {
  return t(`shortcuts.category.${c}`);
}
</script>

<template>
  <NModal
    :show="ui.shortcutsOpen"
    @update:show="(v: boolean) => !v && ui.closeShortcuts()"
    preset="card"
    :title="t('shortcuts.title')"
    style="max-width: 640px;"
  >
    <div v-for="[cat, items] of grouped" :key="cat" class="shortcut-group">
      <NH3>{{ categoryLabel(cat) }}</NH3>
      <div v-for="sc in items" :key="sc.id" class="shortcut-row">
        <span class="shortcut-desc">{{ descriptionOf(sc) }}</span>
        <NTag v-for="(k, i) in [sc.keys]" :key="i" size="small" type="info">{{ k }}</NTag>
      </div>
    </div>
    <NDivider />
    <div class="notes">
      <NH3>{{ t('shortcuts.category.notes') }}</NH3>
      <ul>
        <li v-for="(note, i) in undoLimitations" :key="i">{{ note }}</li>
      </ul>
    </div>
  </NModal>
</template>

<style scoped>
.shortcut-group {
  margin-bottom: var(--vt-space-4);
}
.shortcut-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--vt-space-1) 0;
}
.shortcut-desc {
  color: var(--vt-fg);
}
.notes ul {
  padding-left: var(--vt-space-4);
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
</style>
