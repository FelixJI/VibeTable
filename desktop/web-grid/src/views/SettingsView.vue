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
  CalendarDays,
  Check,
  ChevronLeft,
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
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import { formatDateKey, formatMonthKey, monthLabel, parseDateKey, shiftMonthKey } from "@/calendar/workCalendar";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import type { WorkCalendarOverrideKind } from "@/calendar/workCalendar";

type Section = "general" | "calendar" | "mapping" | "source" | "interaction" | "about";

const ui = useUiStore();
const workspace = useWorkspaceStore();
const mappings = useIdentifierMappingStore();
const workCalendar = useWorkCalendarStore();
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
const calendarMonth = ref(formatMonthKey(new Date()));
const selectedCalendarDate = ref(formatDateKey(new Date()));

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
  { key: "calendar" as const, icon: CalendarDays, label: "settings.workCalendar" },
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

const selectedCalendarOverride = computed(() => workCalendar.getOverride(selectedCalendarDate.value));
const selectedCalendarRule = computed(() => selectedCalendarOverride.value?.kind ?? "default");
const selectedCalendarName = computed(() => selectedCalendarOverride.value?.name ?? "");
const selectedCalendarText = computed(() => {
  const date = parseDateKey(selectedCalendarDate.value) ?? new Date();
  return new Intl.DateTimeFormat(ui.locale, {
    year: "numeric", month: "long", day: "numeric", weekday: "long",
  }).format(date);
});
const calendarMonthText = computed(() => monthLabel(calendarMonth.value, ui.locale));

function selectCalendarDate(date: string): void {
  selectedCalendarDate.value = date;
}

function setCalendarRule(rule: "default" | WorkCalendarOverrideKind): void {
  if (rule === "default") workCalendar.clearOverride(selectedCalendarDate.value);
  else workCalendar.setOverride(selectedCalendarDate.value, rule, selectedCalendarName.value);
}

function setCalendarName(name: string): void {
  const rule = selectedCalendarOverride.value?.kind;
  if (rule) workCalendar.setOverride(selectedCalendarDate.value, rule, name);
}
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

        <template v-else-if="current === 'calendar'">
          <header><h1>{{ t("settings.workCalendar") }}</h1><p>{{ t("settings.workCalendar.description") }}</p></header>
          <section class="calendar-workbench">
            <div class="calendar-toolbar">
              <NButton quaternary circle :aria-label="t('settings.workCalendar.previous')" @click="calendarMonth = shiftMonthKey(calendarMonth, -1)">
                <template #icon><NIcon><ChevronLeft /></NIcon></template>
              </NButton>
              <strong>{{ calendarMonthText }}</strong>
              <span>{{ t("settings.workCalendar.overrides", { count: workCalendar.overrideCount }) }}</span>
              <NButton quaternary circle :aria-label="t('settings.workCalendar.next')" @click="calendarMonth = shiftMonthKey(calendarMonth, 1)">
                <template #icon><NIcon><ChevronRight /></NIcon></template>
              </NButton>
            </div>
            <div class="calendar-layout">
              <WorkCalendarMonth
                :month-key="calendarMonth"
                :overrides="workCalendar.overrides"
                :locale="ui.locale"
                :selected-date="selectedCalendarDate"
                interactive
                @select="selectCalendarDate"
              />
              <aside class="calendar-rule-panel">
                <span>{{ t("settings.workCalendar.selected") }}</span>
                <strong>{{ selectedCalendarText }}</strong>
                <label>{{ t("settings.workCalendar.rule") }}</label>
                <NRadioGroup
                  :value="selectedCalendarRule"
                  size="small"
                  @update:value="setCalendarRule($event as 'default' | WorkCalendarOverrideKind)"
                >
                  <div class="calendar-rule-options">
                    <NRadioButton value="default">{{ t("settings.workCalendar.rule.default") }}</NRadioButton>
                    <NRadioButton value="holiday">{{ t("settings.workCalendar.rule.holiday") }}</NRadioButton>
                    <NRadioButton value="workday">{{ t("settings.workCalendar.rule.workday") }}</NRadioButton>
                  </div>
                </NRadioGroup>
                <label>{{ t("settings.workCalendar.name") }}</label>
                <NInput
                  :value="selectedCalendarName"
                  :disabled="selectedCalendarRule === 'default'"
                  :placeholder="t('settings.workCalendar.name.placeholder')"
                  maxlength="40"
                  @update:value="setCalendarName"
                />
                <p>{{ selectedCalendarRule === "default" ? t("settings.workCalendar.defaultHint") : t("settings.workCalendar.saved") }}</p>
              </aside>
            </div>
            <footer class="calendar-footer">
              <span><i class="calendar-seal calendar-seal--rest">休</i>{{ t("calendar.legend.rest") }}</span>
              <span><i class="calendar-seal calendar-seal--work">班</i>{{ t("calendar.legend.work") }}</span>
              <small>{{ t("settings.workCalendar.saved") }}</small>
            </footer>
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
.calendar-workbench { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.calendar-toolbar { display: grid; grid-template-columns: 32px auto 1fr 32px; align-items: center; gap: 9px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
.calendar-toolbar strong { font-weight: 600; }
.calendar-toolbar > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: right; }
.calendar-layout { display: grid; grid-template-columns: minmax(360px, 1fr) 230px; gap: 18px; padding: 16px; }
.calendar-rule-panel { display: flex; flex-direction: column; gap: 8px; padding: 14px; border-left: 1px solid var(--vt-border); }
.calendar-rule-panel > span, .calendar-rule-panel > label { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.calendar-rule-panel > strong { margin-bottom: 7px; font-size: 15px; font-weight: 600; line-height: 1.45; }
.calendar-rule-panel > label { margin-top: 7px; }
.calendar-rule-panel > p { margin: 8px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.55; }
.calendar-rule-options { display: flex; flex-direction: column; gap: 5px; }
.calendar-rule-options :deep(.n-radio-button) { width: 100%; }
.calendar-footer { display: flex; align-items: center; gap: 14px; padding: 10px 14px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); border-top: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.calendar-footer > span { display: inline-flex; align-items: center; gap: 5px; }
.calendar-footer small { margin-left: auto; }
.calendar-seal { display: grid; place-items: center; width: 18px; height: 18px; font-size: 9px; font-style: normal; font-weight: 700; }
.calendar-seal--rest { color: #b94a48; border-radius: 50%; background: rgba(185, 74, 72, .09); }
.calendar-seal--work { color: #2f67a8; border-radius: 5px; background: rgba(47, 103, 168, .1); }
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
  .calendar-layout { grid-template-columns: 1fr; }
  .calendar-rule-panel { border-top: 1px solid var(--vt-border); border-left: 0; }
}
</style>
