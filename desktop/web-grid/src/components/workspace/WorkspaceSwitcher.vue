<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { NButton, NDropdown, NIcon, NTag } from "naive-ui";
import type { DropdownOption } from "naive-ui";
import { ChevronDown, CircleDotDashed, FolderKanban } from "lucide-vue-next";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { t } from "@/i18n";

const emit = defineEmits<{
  switch: [workspaceId: string];
  center: [];
}>();

const session = useWorkspaceSessionStore();
const announceStage = ref(false);
const root = ref<HTMLElement | null>(null);
const menuOpen = ref(false);
let stageTimer: ReturnType<typeof setTimeout> | null = null;

const stageLabel = computed(() => ({
  idle: "",
  protecting: t("workspaceV2.switch.protecting"),
  draining: t("workspaceV2.switch.draining"),
  stopping: t("workspaceV2.switch.stopping"),
  starting: t("workspaceV2.switch.starting"),
  binding: t("workspaceV2.switch.binding"),
  verifying: t("workspaceV2.switch.verifying"),
  rollingBack: t("workspaceV2.switch.rollingBack"),
}[session.sessionPhase]));

const statusLabel = computed(() => {
  if (session.isTransitioning) return t("workspaceV2.switch.switching");
  if (session.provisional) return t("workspaceV2.switch.provisional");
  if (!session.writable) return t("workspaceV2.switch.readOnly");
  if (session.activeWorkspace?.pendingSync) return t("workspaceV2.center.pendingSync");
  return t("workspaceV2.switch.protected");
});

const statusType = computed<"success" | "warning" | "default">(() => {
  if (session.provisional || session.activeWorkspace?.pendingSync) return "warning";
  if (!session.writable) return "default";
  return "success";
});

const options = computed<DropdownOption[]>(() => [
  ...session.workspaces.map((workspace) => ({
    key: workspace.workspaceId,
    label: workspace.displayName,
    disabled: workspace.workspaceId === session.activeWorkspaceId,
  })),
  { key: "__center__", label: t("workspaceV2.switch.openCenter") },
]);

watch(() => session.isTransitioning, (switching) => {
  if (stageTimer) clearTimeout(stageTimer);
  announceStage.value = false;
  if (switching) {
    stageTimer = setTimeout(() => { announceStage.value = true; }, 300);
  } else {
    requestAnimationFrame(() => {
      root.value
        ?.querySelector<HTMLButtonElement>(".switcher-trigger")
        ?.focus({ preventScroll: true });
    });
  }
});

onBeforeUnmount(() => {
  if (stageTimer) clearTimeout(stageTimer);
});

function select(key: string | number): void {
  if (key === "__center__") {
    emit("center");
    return;
  }
  const workspaceId = String(key);
  if (session.beginSwitch(workspaceId)) emit("switch", workspaceId);
}
</script>

<template>
  <div ref="root" class="workspace-switcher" data-testid="workspace-switcher">
    <NDropdown
      trigger="click"
      placement="bottom-start"
      :options="options"
      :disabled="session.isTransitioning"
      :show="menuOpen"
      @update:show="menuOpen = $event"
      @select="select"
    >
      <NButton
        quaternary
        size="small"
        class="switcher-trigger"
        :aria-label="t('workspaceV2.switch.aria', { name: session.activeWorkspace?.displayName ?? t('workspaceV2.switch.none') })"
        :aria-expanded="menuOpen"
      >
        <template #icon>
          <NIcon>
            <CircleDotDashed v-if="session.isTransitioning" class="switching-icon" />
            <FolderKanban v-else />
          </NIcon>
        </template>
        <span>{{ session.activeWorkspace?.displayName ?? t("workspaceV2.center.kicker") }}</span>
        <NIcon :size="13"><ChevronDown /></NIcon>
      </NButton>
    </NDropdown>
    <NTag
      v-if="session.hasOpenWorkspace"
      size="small"
      round
      :type="statusType"
      class="session-status"
    >
      {{ statusLabel }}
    </NTag>
    <span
      v-if="announceStage && session.isTransitioning"
      class="transition-stage"
      role="status"
      aria-live="polite"
    >
      {{ stageLabel }}
    </span>
  </div>
</template>

<style scoped>
.workspace-switcher { display: flex; align-items: center; gap: 7px; min-width: 0; }
.switcher-trigger { max-width: min(260px, 30vw); font-weight: 600; }
.switcher-trigger span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-status { flex: none; }
.transition-stage {
  max-width: min(320px, 32vw);
  overflow: hidden;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.switching-icon { animation: rotate 1s linear infinite; }
@keyframes rotate { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) {
  .switching-icon { animation: none; }
}
@media (max-width: 700px) {
  .session-status,
  .transition-stage { display: none; }
}
</style>
