<script setup lang="ts">
import type { ReleaseNoteBlock } from "./releaseNoteMarkdown";
import ReleaseNoteRuns from "./ReleaseNoteRuns.vue";

defineProps<{ blocks: readonly ReleaseNoteBlock[] }>();
</script>

<template>
  <div class="release-note-body">
    <template v-for="(block, index) in blocks" :key="index">
      <h4 v-if="block.kind === 'heading' && block.level <= 2" class="release-note-body__heading">
        <ReleaseNoteRuns :runs="block.runs" />
      </h4>
      <p v-else-if="block.kind === 'heading'" class="release-note-body__section">
        <ReleaseNoteRuns :runs="block.runs" />
      </p>
      <ul v-else-if="block.kind === 'bullets'" class="release-note-body__list">
        <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
          <ReleaseNoteRuns :runs="item" />
        </li>
      </ul>
      <p v-else class="release-note-body__paragraph">
        <ReleaseNoteRuns :runs="block.runs" />
      </p>
    </template>
  </div>
</template>

<style scoped>
.release-note-body { display: flex; flex-direction: column; gap: 6px; }
.release-note-body__heading { margin: 4px 0 0; color: var(--vt-fg); font-size: var(--vt-font-body); font-weight: 620; }
.release-note-body__section {
  margin: 6px 0 0;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  font-weight: 650;
  text-transform: none;
}
.release-note-body__list { margin: 0; padding-left: 18px; }
.release-note-body__list li { color: var(--vt-fg-secondary); line-height: 1.65; }
.release-note-body__list li + li { margin-top: 2px; }
.release-note-body__paragraph { margin: 0; color: var(--vt-fg-secondary); line-height: 1.65; }
.release-note-body code {
  padding: 1px 5px;
  color: var(--vt-color-primary-700);
  font-size: var(--vt-font-caption);
  border-radius: 4px;
  background: color-mix(in srgb, var(--vt-color-primary-500) 9%, transparent);
}
:root.dark .release-note-body code { color: #8bb8ed; }
.release-note-body a { color: var(--vt-color-primary-500); text-decoration: none; }
.release-note-body a:hover { text-decoration: underline; }
</style>
