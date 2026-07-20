<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  NButton,
  NDynamicTags,
  NIcon,
  NInput,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSwitch,
  NTag,
  NTooltip,
} from "naive-ui";
import {
  Braces,
  Check,
  ChevronRight,
  HelpCircle,
  Database,
  Download,
  Info,
  Keyboard,
  Copy,
  Palette,
  SlidersHorizontal,
  RefreshCw,
  Search,
  Tags,
  Upload,
  X,
} from "lucide-vue-next";
import brandIconUrl from "@/assets/brand/vibetable.png";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import type { IdentifierMappingImportItem } from "@/contracts";
import type { DensityMode, StartupPage, ThemeMode } from "@/stores/uiStore";
import type { Locale } from "@/i18n";
import { t } from "@/i18n";

type Section = "general" | "mapping" | "source" | "interaction" | "about";

const ui = useUiStore();
const workspace = useWorkspaceStore();
const mappings = useIdentifierMappingStore();
const current = ref<Section>("general");
const emit = defineEmits<{
  reconnect: [];
  openHelp: [];
  openAdmin: [];
  loadMappings: [];
  saveMappingAliases: [mappingId: string, aliases: readonly string[]];
  importMappings: [mappings: readonly IdentifierMappingImportItem[]];
  reconcileMappings: [];
}>();
const mappingQuery = ref("");
const editingMappingId = ref<string | null>(null);
const aliasDraft = ref<string[]>([]);
const importInput = ref<HTMLInputElement | null>(null);
const transferMessage = ref<string | null>(null);

const filteredMappings = computed(() => {
  const needle = mappingQuery.value.trim().toLocaleLowerCase();
  if (!needle) return mappings.mappings;
  return mappings.mappings.filter((item) =>
    [item.displayName, item.physicalName, item.parentPhysicalName ?? "", ...item.aliases]
      .some((value) => value.toLocaleLowerCase().includes(needle)),
  );
});

watch(current, (section) => {
  if (section === "mapping") emit("loadMappings");
});

function beginAliasEdit(mappingId: string, aliases: readonly string[]): void {
  editingMappingId.value = mappingId;
  aliasDraft.value = [...aliases];
}

function saveAliases(): void {
  if (!editingMappingId.value) return;
  emit("saveMappingAliases", editingMappingId.value, aliasDraft.value);
  editingMappingId.value = null;
}

async function copyPhysicalKey(value: string): Promise<void> {
  try {
    await navigator.clipboard?.writeText?.(value);
    transferMessage.value = t("settings.mapping.copied");
  } catch {
    transferMessage.value = t("settings.mapping.copyFailed");
  }
}

function exportMappings(): void {
  const portable = {
    format: "vibetable-identifier-map",
    version: 1,
    exportedAt: new Date().toISOString(),
    mappings: mappings.mappings.map((item) => ({
      entityKind: item.entityKind,
      parentPhysicalName: item.parentPhysicalName ?? null,
      physicalName: item.physicalName,
      displayName: item.displayName,
      aliases: [...item.aliases],
    })),
  };
  const url = URL.createObjectURL(new Blob(
    [JSON.stringify(portable, null, 2)], { type: "application/json" },
  ));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `vibetable-identifier-map-${new Date().toISOString().slice(0, 10)}.json`;
  anchor.click();
  URL.revokeObjectURL(url);
  transferMessage.value = t("settings.mapping.exported");
}

async function onImportFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  if (file.size > 4 * 1024 * 1024) {
    transferMessage.value = t("settings.mapping.fileTooLarge");
    return;
  }
  try {
    const parsed = JSON.parse(await file.text()) as { mappings?: unknown };
    if (!Array.isArray(parsed.mappings)) throw new Error("missing mappings");
    emit("importMappings", parsed.mappings as readonly IdentifierMappingImportItem[]);
    transferMessage.value = t("settings.mapping.importing");
  } catch {
    transferMessage.value = t("settings.mapping.invalidFile");
  }
}

const sections = [
  { key: "general" as const, icon: Palette, label: "settings.general" },
  { key: "mapping" as const, icon: Braces, label: "settings.mapping" },
  { key: "source" as const, icon: Database, label: "settings.source" },
  { key: "interaction" as const, icon: SlidersHorizontal, label: "settings.interaction" },
  { key: "about" as const, icon: Info, label: "settings.about" },
];

const themeOptions = computed(() => [
  { label: t("toolbar.theme.system"), value: "system" },
  { label: t("toolbar.theme.light"), value: "light" },
  { label: t("toolbar.theme.dark"), value: "dark" },
]);
const localeOptions = computed(() => [
  { label: "简体中文", value: "zh-CN" },
  { label: "English", value: "en-US" },
]);
const startupOptions = computed(() => [
  { label: t("nav.home"), value: "home" },
  { label: t("nav.tables"), value: "tables" },
]);

const connectionDetail = computed(() => {
  if (workspace.phase === "opened") {
    return t("settings.source.connectedDetail", { count: workspace.collections.length });
  }
  if (workspace.phase === "failed") return workspace.lastError || t("connection.failed");
  if (workspace.phase === "opening") return t("connection.connecting");
  return t("connection.waiting");
});
</script>

<template>
  <section class="settings-view" data-testid="settings-view">
    <aside class="settings-nav">
      <div class="settings-nav-title">
        <span>{{ t("settings.title") }}</span>
        <small>{{ t("settings.subtitle") }}</small>
      </div>
      <button
        v-for="section in sections"
        :key="section.key"
        type="button"
        :class="{ active: current === section.key }"
        :data-testid="`settings-nav-${section.key}`"
        @click="current = section.key"
      >
        <NIcon :size="16"><component :is="section.icon" /></NIcon>
        {{ t(section.label) }}
      </button>
    </aside>

    <main class="settings-content">
      <div class="settings-inner">
        <template v-if="current === 'general'">
          <header><h1>{{ t("settings.general") }}</h1><p>{{ t("settings.general.description") }}</p></header>
          <section class="setting-card">
            <div class="setting-row">
              <div><strong>{{ t("settings.language") }}</strong><small>{{ t("settings.language.hint") }}</small></div>
              <NSelect
                :value="ui.locale"
                :aria-label="t('settings.language')"
                :options="localeOptions"
                class="setting-control"
                @update:value="ui.setLanguage($event as Locale)"
              />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.theme") }}</strong><small>{{ t("settings.theme.hint") }}</small></div>
              <NSelect
                :value="ui.themeMode"
                :aria-label="t('settings.theme')"
                :options="themeOptions"
                class="setting-control"
                @update:value="ui.setThemeMode($event as ThemeMode)"
              />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.startup") }}</strong><small>{{ t("settings.startup.hint") }}</small></div>
              <NSelect
                :value="ui.startupPage"
                :aria-label="t('settings.startup')"
                :options="startupOptions"
                class="setting-control"
                @update:value="ui.setStartupPage($event as StartupPage)"
              />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.quote") }}</strong><small>{{ t("settings.quote.hint") }}</small></div>
              <NSwitch :value="ui.showDailyQuote" :aria-label="t('settings.quote')" @update:value="ui.setShowDailyQuote" />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.calendar") }}</strong><small>{{ t("settings.calendar.hint") }}</small></div>
              <NSwitch :value="ui.showMiniCalendar" :aria-label="t('settings.calendar')" @update:value="ui.setShowMiniCalendar" />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.density") }}</strong><small>{{ t("settings.density.hint") }}</small></div>
              <NRadioGroup
                :value="ui.density"
                :aria-label="t('settings.density')"
                size="small"
                @update:value="ui.setDensity($event as DensityMode)"
              >
                <NRadioButton value="comfortable">{{ t("settings.density.comfortable") }}</NRadioButton>
                <NRadioButton value="compact">{{ t("settings.density.compact") }}</NRadioButton>
              </NRadioGroup>
            </div>
          </section>
        </template>

        <template v-else-if="current === 'mapping'">
          <header><h1>{{ t("settings.mapping") }}</h1><p>{{ t("settings.mapping.description") }}</p></header>
          <section class="mapping-workbench">
            <div class="mapping-toolbar">
              <NInput v-model:value="mappingQuery" clearable :input-props="{ 'aria-label': t('settings.mapping.search') }" :placeholder="t('settings.mapping.search')">
                <template #prefix><NIcon :size="15"><Search /></NIcon></template>
              </NInput>
              <NTooltip><template #trigger><NButton quaternary circle :aria-label="t('settings.mapping.reconcile')" :loading="mappings.phase === 'reconciling'" @click="emit('reconcileMappings')"><template #icon><NIcon><RefreshCw /></NIcon></template></NButton></template>{{ t("settings.mapping.reconcile") }}</NTooltip>
              <NTooltip><template #trigger><NButton quaternary circle :aria-label="t('settings.mapping.import')" @click="importInput?.click()"><template #icon><NIcon><Upload /></NIcon></template></NButton></template>{{ t("settings.mapping.import") }}</NTooltip>
              <NTooltip><template #trigger><NButton quaternary circle :aria-label="t('settings.mapping.export')" :disabled="!mappings.mappings.length" @click="exportMappings"><template #icon><NIcon><Download /></NIcon></template></NButton></template>{{ t("settings.mapping.export") }}</NTooltip>
              <input ref="importInput" class="sr-only" type="file" accept="application/json,.json" @change="onImportFile" />
            </div>
            <p v-if="transferMessage" class="mapping-message">{{ transferMessage }}</p>
            <p v-if="mappings.error" class="mapping-error">{{ mappings.error }}</p>
            <div v-if="mappings.phase === 'loading'" class="mapping-state">{{ t("settings.mapping.loading") }}</div>
            <div v-else-if="filteredMappings.length === 0" class="mapping-state">{{ t("settings.mapping.empty") }}</div>
            <div v-else class="mapping-list">
              <article v-for="item in filteredMappings" :key="item.id" class="mapping-item">
                <div class="mapping-kind" :class="`mapping-kind--${item.entityKind}`"><NIcon :size="15"><Tags /></NIcon></div>
                <div class="mapping-body">
                  <div class="mapping-title"><strong>{{ item.displayName }}</strong><NTag size="tiny" :bordered="false">{{ item.entityKind === 'collection' ? t('settings.mapping.table') : t('settings.mapping.field') }}</NTag><NTag v-if="item.status !== 'active'" size="tiny" type="warning">{{ item.status }}</NTag></div>
                  <button class="physical-key" type="button" @click="copyPhysicalKey(item.physicalName)"><code>{{ item.physicalName }}</code><NIcon :size="13"><Copy /></NIcon></button>
                  <div v-if="editingMappingId === item.id" class="alias-editor">
                    <NDynamicTags v-model:value="aliasDraft" />
                    <NButton size="tiny" type="primary" :loading="mappings.phase === 'saving'" @click="saveAliases"><template #icon><NIcon><Check /></NIcon></template>{{ t("settings.mapping.save") }}</NButton>
                    <NButton size="tiny" quaternary @click="editingMappingId = null"><template #icon><NIcon><X /></NIcon></template>{{ t("settings.mapping.cancel") }}</NButton>
                  </div>
                  <button v-else class="alias-line" type="button" @click="beginAliasEdit(item.id, item.aliases)">
                    <span>{{ t("settings.mapping.aliases") }}</span>
                    <template v-if="item.aliases.length"><NTag v-for="alias in item.aliases" :key="alias" size="tiny">{{ alias }}</NTag></template>
                    <small v-else>{{ t("settings.mapping.noAliases") }}</small>
                  </button>
                </div>
              </article>
            </div>
            <footer class="mapping-footer"><span>{{ t("settings.mapping.physicalLocked") }}</span><NButton size="small" @click="emit('openAdmin')">{{ t("nav.directus") }}</NButton></footer>
          </section>
        </template>

        <template v-else-if="current === 'source'">
          <header><h1>{{ t("settings.source") }}</h1><p>{{ t("settings.source.description") }}</p></header>
          <section class="setting-card">
            <div class="setting-row setting-row--tall">
              <div><strong>Directus</strong><small>{{ connectionDetail }}</small></div>
              <ConnectionPill @reconnect="emit('reconnect')" />
            </div>
            <div class="source-note">
              <NIcon :size="17"><Database /></NIcon>
              <p>{{ t("settings.source.automatic") }}</p>
            </div>
          </section>
        </template>

        <template v-else-if="current === 'interaction'">
          <header><h1>{{ t("settings.interaction") }}</h1><p>{{ t("settings.interaction.description") }}</p></header>
          <section class="setting-card">
            <div class="setting-row">
              <div><strong>{{ t("settings.adminFloating") }}</strong><small>{{ t("settings.adminFloating.hint") }}</small></div>
              <NSwitch :value="ui.adminFloatingButton" :aria-label="t('settings.adminFloating')" @update:value="ui.setAdminFloatingButton" />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.adminConfirm") }}</strong><small>{{ t("settings.adminConfirm.hint") }}</small></div>
              <NSwitch :value="ui.adminConfirmClose" :aria-label="t('settings.adminConfirm')" @update:value="ui.setAdminConfirmClose" />
            </div>
            <div class="setting-row">
              <div><strong>{{ t("settings.adminRelease") }}</strong><small>{{ t("settings.adminRelease.hint") }}</small></div>
              <NSwitch :value="ui.adminReleaseWhenIdle" :aria-label="t('settings.adminRelease')" @update:value="ui.setAdminReleaseWhenIdle" />
            </div>
            <button type="button" class="setting-action" @click="emit('openHelp')">
              <span class="action-icon"><NIcon :size="17"><Keyboard /></NIcon></span>
              <span><strong>{{ t("settings.shortcuts") }}</strong><small>{{ t("settings.shortcuts.hint") }}</small></span>
              <NIcon :size="16"><ChevronRight /></NIcon>
            </button>
            <div class="source-note">
              <NIcon :size="17"><HelpCircle /></NIcon>
              <p>{{ t("settings.interaction.note") }}</p>
            </div>
          </section>
        </template>

        <template v-else>
          <header><h1>{{ t("settings.about") }}</h1><p>{{ t("settings.about.description") }}</p></header>
          <section class="about-card">
            <img class="about-logo" :src="brandIconUrl" alt="" aria-hidden="true" />
            <div><strong>VibeTable</strong><small>{{ t("settings.about.tagline") }}</small></div>
            <span class="desktop-badge">{{ t("settings.about.desktop") }}</span>
          </section>
        </template>
      </div>
    </main>
  </section>
</template>

<style scoped>
.settings-view { display: flex; height: 100%; min-width: 0; background: var(--vt-bg); }
.settings-nav {
  display: flex;
  flex: 0 0 228px;
  flex-direction: column;
  gap: 3px;
  padding: 18px 12px;
  border-right: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
.settings-nav-title { display: flex; flex-direction: column; padding: 2px 9px 16px; }
.settings-nav-title span { font-size: var(--vt-font-title); font-weight: 600; }
.settings-nav-title small { color: var(--vt-fg-muted); }
.settings-nav button {
  display: flex;
  align-items: center;
  gap: 9px;
  height: 34px;
  padding: 0 10px;
  color: var(--vt-fg-secondary);
  text-align: left;
  border: 0;
  border-radius: var(--vt-radius-md);
  background: transparent;
  cursor: pointer;
}
.settings-nav button:hover { color: var(--vt-fg); background: var(--vt-bg-sunken); }
.settings-nav button.active { color: var(--vt-color-primary-500); background: var(--vt-color-primary-50); }
:root.dark .settings-nav button.active { background: rgba(91, 139, 255, 0.14); }
.settings-content { flex: 1; min-width: 0; overflow: auto; padding: 36px clamp(28px, 6vw, 72px); }
.settings-inner { max-width: 760px; }
header { margin-bottom: 22px; }
h1 { margin: 0 0 5px; font-size: 22px; font-weight: 650; letter-spacing: -0.015em; }
header p { margin: 0; color: var(--vt-fg-muted); }
.setting-card { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); }
.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 28px;
  min-height: 68px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--vt-border);
}
.setting-row:last-child { border-bottom: 0; }
.setting-row--tall { min-height: 82px; }
.setting-row > div { display: flex; flex-direction: column; }
.setting-row strong, .setting-action strong { font-weight: 500; }
.setting-row small, .setting-action small { color: var(--vt-fg-muted); }
.setting-control { width: 170px; }
.mapping-visual {
  display: flex;
  align-items: center;
  gap: 9px;
  margin: 16px;
  padding: 14px;
  color: var(--vt-fg-secondary);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.mapping-workbench { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.mapping-toolbar { display: grid; grid-template-columns: minmax(180px, 1fr) repeat(3, 32px); gap: 6px; padding: 12px; border-bottom: 1px solid var(--vt-border); }
.mapping-list { max-height: 520px; overflow: auto; }
.mapping-item { display: grid; grid-template-columns: 30px 1fr; gap: 10px; padding: 12px 14px; border-bottom: 1px solid var(--vt-border); }
.mapping-kind { display: grid; place-items: center; width: 28px; height: 28px; color: var(--vt-color-primary-500); border-radius: 6px; background: var(--vt-color-primary-50); }
.mapping-kind--field { color: var(--vt-fg-muted); background: var(--vt-bg-subtle); }
.mapping-body { min-width: 0; }
.mapping-title { display: flex; align-items: center; gap: 7px; }
.mapping-title strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 550; }
.physical-key { display: inline-flex; align-items: center; gap: 5px; max-width: 100%; margin: 4px 0 7px; padding: 0; color: var(--vt-fg-muted); border: 0; background: transparent; cursor: pointer; }
.physical-key code { overflow: hidden; text-overflow: ellipsis; font-size: 11px; }
.alias-line { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; width: 100%; padding: 0; color: var(--vt-fg-muted); text-align: left; border: 0; background: transparent; cursor: pointer; }
.alias-line > span { font-size: var(--vt-font-caption); }
.alias-line small { color: var(--vt-fg-placeholder); }
.alias-editor { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.alias-editor :deep(.n-dynamic-tags) { flex: 1 1 260px; }
.mapping-state { padding: 42px 18px; color: var(--vt-fg-muted); text-align: center; }
.mapping-message, .mapping-error { margin: 0; padding: 8px 12px; font-size: var(--vt-font-caption); border-bottom: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.mapping-error { color: var(--vt-color-danger-500, #d03050); }
.mapping-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 12px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); background: var(--vt-bg-subtle); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); }
.mapping-visual code { color: var(--vt-color-primary-500); font-family: Consolas, monospace; }
.read-only-badge, .desktop-badge {
  padding: 3px 8px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  border: 1px solid var(--vt-border);
  border-radius: 999px;
  background: var(--vt-bg-subtle);
}
.metric { font-size: 20px; font-weight: 600; font-variant-numeric: tabular-nums; }
.source-note { display: flex; gap: 9px; margin: 16px; padding: 12px; color: var(--vt-fg-muted); border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.source-note p { margin: 0; }
.setting-action {
  display: grid;
  grid-template-columns: 34px 1fr auto;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 14px 16px;
  color: var(--vt-fg);
  text-align: left;
  border: 0;
  border-bottom: 1px solid var(--vt-border);
  background: transparent;
  cursor: pointer;
}
.setting-action:hover { background: var(--vt-bg-subtle); }
.setting-action > span:nth-child(2) { display: flex; flex-direction: column; }
.action-icon { display: grid; place-items: center; width: 32px; height: 32px; color: var(--vt-color-primary-500); border-radius: var(--vt-radius-md); background: var(--vt-color-primary-50); }
.about-card { display: flex; align-items: center; gap: 14px; padding: 20px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); }
.about-card > div:nth-child(2) { display: flex; flex: 1; flex-direction: column; }
.about-card small { color: var(--vt-fg-muted); }
.about-logo { width: 40px; height: 40px; border-radius: 10px; object-fit: cover; box-shadow: 0 5px 16px rgba(36, 89, 211, .2); }
@media (max-width: 760px) {
  .settings-nav { flex-basis: 52px; padding: 16px 7px; }
  .settings-nav-title { display: none; }
  .settings-nav button { justify-content: center; padding: 0; font-size: 0; }
  .settings-content { padding: 28px 20px; }
}
</style>
