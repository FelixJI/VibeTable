<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted } from "vue";
import type { Component, HTMLAttributes } from "vue";
import { NDropdown, NIcon, type DropdownOption } from "naive-ui";
import { ExternalLink, Eye, FolderSearch, History, LocateFixed, Trash2 } from "@lucide/vue";
import type { DocumentCapability, DocumentEntry } from "@/stores/documentWorkspaceStore";
import { t } from "@/i18n";

const props = defineProps<{ entry: DocumentEntry; x: number; y: number }>();
const emit = defineEmits<{
  action: [action: DocumentCapability, entry: DocumentEntry];
  close: [];
}>();

// The menu teleports to document.body and NDropdown only reacts to
// in-document interactions, so a native window blur must still dismiss it.
const close = () => emit("close");
onMounted(() => {
  window.addEventListener("blur", close);
});
onBeforeUnmount(() => {
  window.removeEventListener("blur", close);
});

function menuIcon(icon: Component): () => ReturnType<typeof h> {
  return () => h(NIcon, { size: 15 }, { default: () => h(icon) });
}

const dangerProps = {
  role: "menuitem",
  class: "document-menu-danger",
  "data-testid": "document-unlink",
} as HTMLAttributes;

const options = computed<DropdownOption[]>(() => {
  const capabilities = props.entry.capabilities;
  const items: DropdownOption[] = [];
  if (capabilities.includes("open")) {
    items.push({ key: "open", label: t("files.action.open"), icon: menuIcon(ExternalLink), props: { role: "menuitem" } });
  }
  if (capabilities.includes("preview")) {
    items.push({ key: "preview", label: t("files.action.preview"), icon: menuIcon(Eye), props: { role: "menuitem" } });
  }
  if (capabilities.includes("reveal") || capabilities.includes("relink")) {
    items.push({ key: "divider-location", type: "divider" });
    if (props.entry.availability === "missing" && capabilities.includes("relink")) {
      items.push({ key: "relink", label: t("files.action.relink"), icon: menuIcon(LocateFixed), props: { role: "menuitem" } });
    } else if (capabilities.includes("reveal")) {
      items.push({ key: "reveal", label: t("files.action.reveal"), icon: menuIcon(FolderSearch), props: { role: "menuitem" } });
    }
  }
  if (capabilities.includes("history")) {
    items.push({ key: "history", label: t("files.action.history"), icon: menuIcon(History), props: { role: "menuitem" } });
  }
  if (capabilities.includes("unlink")) {
    items.push({ key: "divider-unlink", type: "divider" });
    items.push({ key: "unlink", label: t("files.action.unlink"), icon: menuIcon(Trash2), props: dangerProps });
  }
  return items;
});

function onSelect(key: string | number): void {
  emit("action", key as DocumentCapability, props.entry);
}
</script>

<template>
  <NDropdown
    :show="true"
    trigger="manual"
    placement="bottom-start"
    :x="x"
    :y="y"
    :options="options"
    data-testid="document-context-menu"
    role="menu"
    @select="onSelect"
    @update:show="(value: boolean) => { if (!value) emit('close'); }"
    @clickoutside="emit('close')"
  />
</template>

<style>
/* NDropdown teleports the menu to document.body, so the danger variant of the
   unlink entry needs a global rule to reach it. */
.n-dropdown .n-dropdown-option-body.document-menu-danger,
.n-dropdown .n-dropdown-option-body.document-menu-danger:hover {
  color: var(--vt-color-danger-600);
}
</style>
