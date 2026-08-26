<script setup lang="ts">
import { onBeforeUnmount, onMounted } from "vue";
import { NIcon } from "naive-ui";
import { ExternalLink, Eye, FolderSearch, History, LocateFixed, Trash2 } from "@lucide/vue";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

const props = defineProps<{ entry: DocumentEntry; x: number; y: number }>();
const emit = defineEmits<{
  action: [action: DocumentCapability, entry: DocumentEntry];
  close: [];
}>();

const close = () => emit("close");
onMounted(() => {
  window.addEventListener("pointerdown", close);
  window.addEventListener("blur", close);
});
onBeforeUnmount(() => {
  window.removeEventListener("pointerdown", close);
  window.removeEventListener("blur", close);
});

function choose(action: DocumentCapability): void {
  emit("action", action, props.entry);
}
</script>

<template>
  <div
    class="document-menu"
    role="menu"
    :style="{ left: `${x}px`, top: `${y}px` }"
    data-testid="document-context-menu"
    @pointerdown.stop
    @keydown.esc="emit('close')"
  >
    <button v-if="entry.capabilities.includes('open')" role="menuitem" autofocus @click="choose('open')">
      <NIcon :size="15"><ExternalLink /></NIcon>{{ t("files.action.open") }}
    </button>
    <button v-if="entry.capabilities.includes('preview')" role="menuitem" @click="choose('preview')">
      <NIcon :size="15"><Eye /></NIcon>{{ t("files.action.preview") }}
    </button>
    <i v-if="entry.capabilities.includes('reveal') || entry.capabilities.includes('relink')"></i>
    <button v-if="entry.availability === 'missing' && entry.capabilities.includes('relink')" role="menuitem" @click="choose('relink')">
      <NIcon :size="15"><LocateFixed /></NIcon>{{ t("files.action.relink") }}
    </button>
    <button v-else-if="entry.capabilities.includes('reveal')" role="menuitem" @click="choose('reveal')">
      <NIcon :size="15"><FolderSearch /></NIcon>{{ t("files.action.reveal") }}
    </button>
    <button v-if="entry.capabilities.includes('history')" role="menuitem" @click="choose('history')">
      <NIcon :size="15"><History /></NIcon>{{ t("files.action.history") }}
    </button>
    <i v-if="entry.capabilities.includes('unlink')"></i>
    <button
      v-if="entry.capabilities.includes('unlink')"
      class="danger"
      data-testid="document-unlink"
      role="menuitem"
      @click="choose('unlink')"
    >
      <NIcon :size="15"><Trash2 /></NIcon>{{ t("files.action.unlink") }}
    </button>
  </div>
</template>

<style scoped>
.document-menu {
  position: fixed;
  z-index: 120;
  width: 196px;
  padding: 5px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg-elevated);
  box-shadow: var(--vt-shadow-3);
}
.document-menu button {
  display: flex;
  align-items: center;
  width: 100%;
  gap: 9px;
  min-height: 31px;
  padding: 0 8px;
  color: var(--vt-fg);
  text-align: left;
  border: 0;
  border-radius: var(--vt-radius-md);
  background: transparent;
  cursor: pointer;
}
.document-menu button:hover, .document-menu button:focus-visible { outline: none; background: var(--vt-bg-sunken); }
.document-menu button.danger { color: var(--vt-color-danger-600); }
.document-menu i { display: block; height: 1px; margin: 4px 6px; background: var(--vt-border); }
</style>
