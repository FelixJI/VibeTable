<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import {
  NButton,
  NAlert,
  NDynamicTags,
  NIcon,
  NInput,
  NModal,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSwitch,
  NTag,
  NTooltip,
} from "naive-ui";
import {
  Braces,
  ArchiveRestore,
  CalendarDays,
  Check,
  ChevronLeft,
  ChevronRight,
  HelpCircle,
  Database,
  Download,
  FolderOpen,
  Info,
  Keyboard,
  Copy,
  Palette,
  SlidersHorizontal,
  RefreshCw,
  Search,
  Tags,
  Trash2,
  X,
} from "lucide-vue-next";
import brandIconUrl from "@/assets/brand/vibetable.png";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import { QUOTE_STYLES_BY_SOURCE, useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import type {
  DailyQuoteSource,
  DailyQuoteStyle,
  DensityMode,
  StartupPage,
  ThemeMode,
} from "@/stores/uiStore";
import type { Locale } from "@/i18n";
import { t } from "@/i18n";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
import { formatDateKey, formatMonthKey, parseDateKey, shiftMonthKey } from "@/calendar/workCalendar";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import type { WorkCalendarOverrideKind } from "@/calendar/workCalendar";
import type { BackupEntry } from "@/contracts/backupContracts";
import type { RuntimeDiagnostics } from "@/contracts/runtimeDiagnosticsContracts";
import type { DataRootStatus } from "@/contracts";
import { useBackupService } from "@/services/backupService";
import { useDataRootService } from "@/services/dataRootService";
import { useRuntimeDiagnosticsService } from "@/services/runtimeDiagnosticsService";

type Section = "general" | "calendar" | "mapping" | "source" | "backup" | "interaction" | "about";

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
  reconcileMappings: [];
}>();
const mappingQuery = ref("");
const editingMappingId = ref<string | null>(null);
const aliasDraft = ref<string[]>([]);
const transferMessage = ref<string | null>(null);
const calendarMonth = ref(formatMonthKey(new Date()));
const selectedCalendarDate = ref(formatDateKey(new Date()));
const backupService = useBackupService();
const backups = ref<readonly BackupEntry[]>([]);
const backupPhase = ref<"idle" | "loading" | "creating" | "deleting" | "restoring">("idle");
const backupError = ref<string | null>(null);
const backupStatus = ref<string | null>(null);
let backupStatusTimer: number | null = null;
const restoreTarget = ref<BackupEntry | null>(null);
const restoreTrigger = ref<HTMLElement | null>(null);
const dataRootService = useDataRootService();
const dataRootStatus = ref<DataRootStatus | null>(null);
const dataRootPhase = ref<"idle" | "loading" | "choosing">("idle");
const dataRootError = ref<string | null>(null);
const dataRootMessage = ref<string | null>(null);
const diagnosticsService = useRuntimeDiagnosticsService();
const diagnostics = ref<RuntimeDiagnostics | null>(null);
const diagnosticsPhase = ref<"idle" | "loading">("idle");
const diagnosticsError = ref<string | null>(null);

function openRestoreConfirmation(backup: BackupEntry, event: MouseEvent): void {
  restoreTrigger.value = event.currentTarget instanceof HTMLElement
    ? event.currentTarget
    : null;
  restoreTarget.value = backup;
}

function closeRestoreConfirmation(): void {
  restoreTarget.value = null;
  const trigger = restoreTrigger.value;
  restoreTrigger.value = null;
  void nextTick(() => {
    window.setTimeout(() => trigger?.focus(), 0);
  });
}

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
  if (section === "backup") void loadBackups();
  if (section === "source") void loadDataRoot();
  if (section === "about") void loadDiagnostics();
});

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : t("settings.backup.failed");
}

async function loadDataRoot(): Promise<void> {
  dataRootPhase.value = "loading";
  dataRootError.value = null;
  try {
    dataRootStatus.value = await dataRootService.getStatus();
  } catch (error) {
    dataRootError.value = error instanceof Error
      ? error.message
      : t("settings.dataRoot.failed");
  } finally {
    dataRootPhase.value = "idle";
  }
}

async function chooseDataRootMigration(): Promise<void> {
  dataRootPhase.value = "choosing";
  dataRootError.value = null;
  dataRootMessage.value = null;
  try {
    const selection = await dataRootService.chooseMigration();
    if (!selection.selected) return;
    dataRootStatus.value = dataRootStatus.value
      ? {
          ...dataRootStatus.value,
          migrationPending: true,
          pendingDataRoot: selection.targetDataRoot,
        }
      : null;
    dataRootMessage.value = t("settings.dataRoot.pending", {
      path: selection.targetDataRoot ?? "",
    });
  } catch (error) {
    dataRootError.value = error instanceof Error
      ? error.message
      : t("settings.dataRoot.failed");
  } finally {
    dataRootPhase.value = "idle";
  }
}

async function loadBackups(): Promise<void> {
  backupPhase.value = "loading";
  backupError.value = null;
  clearBackupStatus();
  try {
    backups.value = (await backupService.listBackups()).backups;
  } catch (error) {
    backupError.value = errorMessage(error);
  } finally {
    backupPhase.value = "idle";
  }
}

function clearBackupStatus(): void {
  if (backupStatusTimer !== null) {
    window.clearTimeout(backupStatusTimer);
    backupStatusTimer = null;
  }
  backupStatus.value = null;
}

function setBackupStatus(message: string, autoClearMs = 0): void {
  clearBackupStatus();
  backupStatus.value = message;
  if (autoClearMs > 0) {
    backupStatusTimer = window.setTimeout(clearBackupStatus, autoClearMs);
  }
}

async function createBackup(): Promise<void> {
  backupPhase.value = "creating";
  backupError.value = null;
  setBackupStatus(t("settings.backup.creating"));
  try {
    const result = await backupService.createBackup();
    backups.value = [
      result.backup,
      ...backups.value.filter((item) => item.name !== result.backup.name),
    ];
    setBackupStatus(t("settings.backup.created", { name: result.backup.name }), 5000);
  } catch (error) {
    backupStatus.value = null;
    backupError.value = errorMessage(error);
  } finally {
    backupPhase.value = "idle";
  }
}

async function deleteBackup(backup: BackupEntry): Promise<void> {
  backupPhase.value = "deleting";
  backupError.value = null;
  setBackupStatus(t("settings.backup.deleting", { name: backup.name }));
  try {
    await backupService.deleteBackup(backup.name);
    backups.value = backups.value.filter((item) => item.name !== backup.name);
    setBackupStatus(t("settings.backup.deleted", { name: backup.name }), 5000);
  } catch (error) {
    clearBackupStatus();
    backupError.value = errorMessage(error);
  } finally {
    backupPhase.value = "idle";
  }
}

async function openBackupFolder(): Promise<void> {
  backupError.value = null;
  try {
    await backupService.openBackupFolder();
    setBackupStatus(t("settings.backup.folderOpened"), 3500);
  } catch (error) {
    backupError.value = errorMessage(error);
  }
}

async function confirmRestore(): Promise<void> {
  const target = restoreTarget.value;
  if (!target) return;
  restoreTarget.value = null;
  backupPhase.value = "restoring";
  backupError.value = null;
  setBackupStatus(t("settings.backup.restoring"));
  try {
    await backupService.restoreBackup(target.name, true);
    setBackupStatus(t("settings.backup.restarting"));
  } catch (error) {
    backupStatus.value = null;
    backupError.value = errorMessage(error);
    backupPhase.value = "idle";
  }
}

async function loadDiagnostics(): Promise<void> {
  diagnosticsPhase.value = "loading";
  diagnosticsError.value = null;
  try {
    diagnostics.value = await diagnosticsService.getDiagnostics();
  } catch (error) {
    diagnosticsError.value = error instanceof Error
      ? error.message
      : t("settings.about.failed");
  } finally {
    diagnosticsPhase.value = "idle";
  }
}

function formatMemory(size: number): string {
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

onBeforeUnmount(clearBackupStatus);

function formatBackupSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function formatBackupDate(value: string): string {
  return new Intl.DateTimeFormat(ui.locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

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

const sections = [
  { key: "general" as const, icon: Palette, label: "settings.general" },
  { key: "calendar" as const, icon: CalendarDays, label: "settings.workCalendar" },
  { key: "mapping" as const, icon: Braces, label: "settings.mapping" },
  { key: "source" as const, icon: Database, label: "settings.source" },
  { key: "backup" as const, icon: ArchiveRestore, label: "settings.backup" },
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
const quoteSourceOptions = computed(() => (["hitokoto", "jinrishici", "quotable", "builtin"] as const)
  .map((value) => ({ label: t(`settings.quote.source.${value}`), value })));
const quoteStyleOptions = computed(() => QUOTE_STYLES_BY_SOURCE[ui.dailyQuoteSource]
  .map((value) => ({ label: t(`settings.quote.style.${value}`), value })));

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
        :aria-current="current === section.key ? 'page' : undefined"
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
                data-testid="language-select"
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
                data-testid="theme-select"
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
            <div class="setting-row setting-row--nested">
              <div><strong>{{ t("settings.quote.source") }}</strong><small>{{ t("settings.quote.source.hint") }}</small></div>
              <NSelect
                :value="ui.dailyQuoteSource"
                :disabled="!ui.showDailyQuote"
                :aria-label="t('settings.quote.source')"
                :options="quoteSourceOptions"
                class="setting-control"
                data-testid="quote-source-select"
                @update:value="ui.setDailyQuoteSource($event as DailyQuoteSource)"
              />
            </div>
            <p
              v-if="ui.showDailyQuote && ui.dailyQuoteSource !== 'builtin'"
              class="setting-network-disclosure"
              data-testid="quote-network-disclosure"
            >
              {{ t("settings.quote.networkDisclosure") }}
            </p>
            <div class="setting-row setting-row--nested">
              <div><strong>{{ t("settings.quote.style") }}</strong><small>{{ t("settings.quote.style.hint") }}</small></div>
              <NSelect
                :value="ui.dailyQuoteStyle"
                :disabled="!ui.showDailyQuote || quoteStyleOptions.length === 1"
                :aria-label="t('settings.quote.style')"
                :options="quoteStyleOptions"
                class="setting-control"
                data-testid="quote-style-select"
                @update:value="ui.setDailyQuoteStyle($event as DailyQuoteStyle)"
              />
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
                class="setting-control setting-control--radio"
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
              <MonthNavigator :month-key="calendarMonth" :locale="ui.locale" @update:month-key="calendarMonth = $event" />
              <NButton size="small" class="calendar-today-btn" data-testid="calendar-today" :aria-label="t('settings.workCalendar.today')" @click="calendarMonth = formatMonthKey(new Date())">
                {{ t("settings.workCalendar.today") }}
              </NButton>
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
                  class="calendar-name-input"
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
              <NTooltip><template #trigger><NButton quaternary circle :aria-label="t('settings.mapping.export')" :disabled="!mappings.mappings.length" @click="exportMappings"><template #icon><NIcon><Download /></NIcon></template></NButton></template>{{ t("settings.mapping.export") }}</NTooltip>
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
            <footer class="mapping-footer"><span>{{ t("settings.mapping.physicalLocked") }}</span><NButton size="small" @click="emit('openAdmin')">{{ t("nav.admin") }}</NButton></footer>
          </section>
        </template>

        <template v-else-if="current === 'source'">
          <header><h1>{{ t("settings.source") }}</h1><p>{{ t("settings.source.description") }}</p></header>
          <section class="setting-card">
            <div class="setting-row setting-row--tall">
              <div><strong>{{ t("settings.source.localService") }}</strong><small>{{ connectionDetail }}</small></div>
              <div class="setting-control--pill"><ConnectionPill @reconnect="emit('reconnect')" /></div>
            </div>
            <div class="setting-row setting-row--tall">
              <div class="data-root-copy">
                <strong>{{ t("settings.dataRoot") }}</strong>
                <small>{{ t("settings.dataRoot.hint") }}</small>
                <code data-testid="data-root-path">
                  {{ dataRootStatus?.dataRoot ?? t("settings.dataRoot.loading") }}
                </code>
              </div>
              <NButton
                size="small"
                :loading="dataRootPhase === 'choosing'"
                :disabled="dataRootPhase !== 'idle'"
                data-testid="data-root-migrate"
                @click="chooseDataRootMigration"
              >
                {{ t("settings.dataRoot.migrate") }}
              </NButton>
            </div>
            <NAlert
              v-if="dataRootError"
              type="error"
              :title="t('settings.dataRoot.failed')"
              data-testid="data-root-error"
            >
              {{ dataRootError }}
            </NAlert>
            <NAlert
              v-if="dataRootMessage || dataRootStatus?.migrationPending"
              type="warning"
              :title="t('settings.dataRoot.restartTitle')"
              data-testid="data-root-pending"
            >
              {{ dataRootMessage ?? t("settings.dataRoot.pending", {
                path: dataRootStatus?.pendingDataRoot ?? "",
              }) }}
            </NAlert>
            <div class="source-note">
              <NIcon :size="17"><Database /></NIcon>
              <p>{{ t("settings.source.automatic") }}</p>
            </div>
          </section>
        </template>

        <template v-else-if="current === 'backup'">
          <header>
            <h1>{{ t("settings.backup") }}</h1>
            <p>{{ t("settings.backup.description") }}</p>
          </header>
          <section class="backup-workbench">
            <div class="backup-toolbar">
              <div>
                <strong>{{ t("settings.backup.local") }}</strong>
                <small>{{ t("settings.backup.localHint") }}</small>
              </div>
              <div class="backup-actions">
                <NButton
                  size="small"
                  :disabled="backupPhase !== 'idle'"
                  data-testid="backup-open-folder"
                  @click="openBackupFolder"
                >
                  <template #icon><NIcon><FolderOpen /></NIcon></template>
                  {{ t("settings.backup.openFolder") }}
                </NButton>
                <NButton
                  size="small"
                  quaternary
                  :loading="backupPhase === 'loading'"
                  :disabled="backupPhase !== 'idle'"
                  data-testid="backup-refresh"
                  @click="loadBackups"
                >
                  {{ t("settings.backup.refresh") }}
                </NButton>
                <NButton
                  size="small"
                  type="primary"
                  :loading="backupPhase === 'creating'"
                  :disabled="backupPhase !== 'idle'"
                  data-testid="backup-create"
                  @click="createBackup"
                >
                  {{ t("settings.backup.create") }}
                </NButton>
              </div>
            </div>

            <NAlert
              v-if="backupError"
              type="error"
              :title="t('settings.backup.failed')"
              data-testid="backup-error"
            >
              {{ backupError }}
            </NAlert>
            <NAlert
              v-if="backupStatus"
              :type="backupPhase === 'restoring' || backupPhase === 'deleting' ? 'warning' : 'success'"
              :title="t('settings.backup.status')"
              closable
              data-testid="backup-status"
              @close="clearBackupStatus"
            >
              {{ backupStatus }}
            </NAlert>

            <div v-if="backupPhase === 'loading'" class="backup-empty">
              {{ t("settings.backup.loading") }}
            </div>
            <div v-else-if="backups.length === 0" class="backup-empty">
              <strong>{{ t("settings.backup.empty") }}</strong>
              <small>{{ t("settings.backup.emptyHint") }}</small>
            </div>
            <div v-else class="backup-list">
              <article v-for="backup in backups" :key="backup.name" class="backup-entry">
                <div class="backup-entry-mark"><ArchiveRestore :size="17" /></div>
                <div class="backup-entry-copy">
                  <strong>{{ backup.name }}</strong>
                  <span>{{ formatBackupDate(backup.modified) }} · {{ formatBackupSize(backup.size) }}</span>
                  <code :title="backup.sha256">SHA-256 {{ backup.sha256.slice(0, 12) }}…</code>
                </div>
                <div class="backup-entry-actions">
                  <NButton
                    size="small"
                    secondary
                    type="warning"
                    :disabled="backupPhase !== 'idle'"
                    :data-testid="`backup-restore-${backup.name}`"
                    @click="openRestoreConfirmation(backup, $event)"
                  >
                    {{ t("settings.backup.restore") }}
                  </NButton>
                  <NPopconfirm
                    :positive-text="t('settings.backup.deleteConfirm')"
                    :negative-text="t('settings.backup.cancel')"
                    @positive-click="deleteBackup(backup)"
                  >
                    <template #trigger>
                      <NButton
                        size="small"
                        quaternary
                        type="error"
                        :disabled="backupPhase !== 'idle'"
                        :data-testid="`backup-delete-${backup.name}`"
                      >
                        <template #icon><NIcon><Trash2 /></NIcon></template>
                        {{ t("settings.backup.delete") }}
                      </NButton>
                    </template>
                    {{ t("settings.backup.deleteMessage", { name: backup.name }) }}
                  </NPopconfirm>
                </div>
              </article>
            </div>

            <NModal
              :show="!!restoreTarget"
              preset="card"
              :title="t('settings.backup.confirmTitle')"
              class="backup-confirmation-modal"
              :auto-focus="true"
              :trap-focus="true"
              :close-on-esc="true"
              :mask-closable="false"
              aria-modal="true"
              :aria-label="t('settings.backup.confirmTitle')"
              data-testid="backup-restore-confirmation"
              @update:show="show => { if (!show) closeRestoreConfirmation() }"
            >
              <div v-if="restoreTarget" class="backup-confirmation">
                <p>{{ t("settings.backup.confirmMessage", { name: restoreTarget.name }) }}</p>
                <small>{{ t("settings.backup.confirmSafety") }}</small>
                <div class="backup-confirmation-actions">
                  <NButton
                    size="small"
                    data-testid="backup-restore-cancel"
                    @click="closeRestoreConfirmation"
                  >
                    {{ t("settings.backup.cancel") }}
                  </NButton>
                  <NButton
                    size="small"
                    type="error"
                    data-testid="backup-restore-confirm"
                    @click="confirmRestore"
                  >
                    {{ t("settings.backup.confirm") }}
                  </NButton>
                </div>
              </div>
            </NModal>

            <footer class="backup-safety-note">
              {{ t("settings.backup.safety") }}
            </footer>
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
          <section class="diagnostics-card">
            <div class="diagnostics-heading">
              <div>
                <strong>{{ t("settings.about.runtime") }}</strong>
                <small>{{ t("settings.about.runtimeHint") }}</small>
              </div>
              <NButton size="small" :loading="diagnosticsPhase === 'loading'" @click="loadDiagnostics">
                {{ t("settings.about.refresh") }}
              </NButton>
            </div>
            <NAlert v-if="diagnosticsError" type="error" :title="t('settings.about.failed')">
              {{ diagnosticsError }}
            </NAlert>
            <dl v-else-if="diagnostics" class="diagnostics-grid">
              <div><dt>{{ t("settings.about.currentDirectory") }}</dt><dd><code>{{ diagnostics.currentDirectory }}</code></dd></div>
              <div><dt>{{ t("settings.about.programDirectory") }}</dt><dd><code>{{ diagnostics.programDirectory }}</code></dd></div>
              <div><dt>{{ t("settings.about.dataDirectory") }}</dt><dd><code>{{ diagnostics.dataDirectory }}</code></dd></div>
              <div><dt>{{ t("settings.about.systemVersion") }}</dt><dd>{{ diagnostics.operatingSystem }}</dd></div>
              <div><dt>{{ t("settings.about.programVersion") }}</dt><dd>{{ diagnostics.programVersion }}</dd></div>
              <div><dt>{{ t("settings.about.runtimeVersion") }}</dt><dd>{{ diagnostics.dotnetVersion }}</dd></div>
              <div><dt>{{ t("settings.about.pocketBaseVersion") }}</dt><dd>{{ diagnostics.pocketBaseVersion }}</dd></div>
              <div><dt>{{ t("settings.about.memory") }}</dt><dd>{{ formatMemory(diagnostics.memoryBytes) }}</dd></div>
              <div><dt>{{ t("settings.about.dataServiceState") }}</dt><dd>{{ diagnostics.dataServiceState }}</dd></div>
            </dl>
            <p v-else class="diagnostics-loading">{{ t("settings.about.loading") }}</p>
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
.setting-row--nested { background: color-mix(in srgb, var(--vt-bg-subtle) 55%, transparent); }
.setting-row > div { display: flex; flex-direction: column; }
.setting-row strong, .setting-action strong { font-weight: 500; }
.setting-row small, .setting-action small { color: var(--vt-fg-muted); }
.setting-control { width: 170px; }
/* Naive UI renders `.n-radio-group` as `display: inline-block`, so applying
   `flex` to the buttons does nothing. Turn the group into a flex container so
   the density radio buttons share the fixed 170px column side-by-side. */
.setting-control.setting-control--radio { display: flex; flex-direction: row; width: 170px; }
.setting-control--radio :deep(.n-radio-button) { flex: 1 1 0; }
/* The connection pill is intrinsically wider than a single line of text; in the
   tall source row `justify-content: space-between` shrinks it until its content
   wraps. Pin it to its content width so it stays on one line like the other
   right-aligned controls. */
.setting-control--pill { flex: none; }
.data-root-copy { min-width: 0; }
.data-root-copy code {
  overflow: hidden;
  max-width: min(460px, 55vw);
  margin-top: 5px;
  color: var(--vt-fg-secondary);
  font-size: var(--vt-font-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.setting-card > :deep(.n-alert) { margin: 12px 14px; }
.calendar-workbench { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.calendar-toolbar { display: grid; grid-template-columns: 32px auto auto 1fr 32px; align-items: center; gap: 9px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
.calendar-toolbar > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: right; }
/* "今日/Today" 文字很短，但 Naive UI 默认按钮水平内边距（~14px×2）让它在
   工具栏里偏宽、挤占有限空间。收紧到与圆形图标按钮视觉一致。 */
.calendar-today-btn :deep(.n-button__content) { padding: 0 6px; }
.calendar-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(180px, 220px); gap: 18px; padding: 16px; }
/* 面板宽度由内容驱动（不再固定 230px）：NInput 的固定宽度定下列宽，
   单选按钮按文字自然宽度排列，标签/备注在列宽内换行。避免固定窄列把
   控件右边裁切。 */
.calendar-rule-panel { display: flex; min-width: 0; flex-direction: column; gap: 8px; padding: 14px; border-left: 1px solid var(--vt-border); }
.calendar-rule-panel > span, .calendar-rule-panel > label { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.calendar-rule-panel > strong { margin-bottom: 7px; font-size: 15px; font-weight: 600; line-height: 1.45; }
.calendar-rule-panel > label { margin-top: 7px; }
.calendar-rule-panel > p { margin: 8px 0 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); line-height: 1.55; }
/* 名称输入框给固定宽度，作为面板列宽的基准（Naive UI 默认 width:100% 在
   auto 列里会塌缩，所以显式给值）。 */
.calendar-name-input { width: min(200px, 100%); }
/* 单选按钮按文字宽度排成一行（不再竖排拉满），按需换行。 */
.calendar-rule-options { display: flex; flex-direction: row; flex-wrap: wrap; gap: 5px; }
.calendar-rule-panel :deep(.n-radio-group) { min-width: 0; max-width: 100%; }
.calendar-rule-options :deep(.n-radio-button) { flex: 0 1 auto; width: auto; max-width: 100%; }
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
.mapping-toolbar { display: grid; grid-template-columns: minmax(180px, 1fr) repeat(2, 32px); gap: 6px; padding: 12px; border-bottom: 1px solid var(--vt-border); }
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
.backup-workbench {
  overflow: hidden;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
}
.backup-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--vt-border);
}
.backup-toolbar > div:first-child,
.backup-empty,
.backup-entry-copy {
  display: flex;
  flex-direction: column;
}
.backup-toolbar small,
.backup-entry-copy span,
.backup-entry-copy code,
.backup-empty small,
.backup-confirmation small {
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.backup-actions,
.backup-entry-actions,
.backup-confirmation-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.backup-workbench :deep(.n-alert) { margin: 12px 14px 0; }
.backup-list { border-top: 0; }
.backup-entry {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) auto;
  align-items: center;
  gap: 11px;
  min-height: 72px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--vt-border);
}
.backup-entry-mark {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  color: var(--vt-color-primary-500);
  border-radius: var(--vt-radius-md);
  background: var(--vt-color-primary-50);
}
.backup-entry-copy { min-width: 0; gap: 2px; }
.backup-entry-copy strong,
.backup-entry-copy code {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.backup-entry-copy strong { font-weight: 550; }
.backup-entry-actions { justify-content: flex-end; }
.backup-empty {
  align-items: center;
  gap: 4px;
  padding: 42px 18px;
  text-align: center;
}
.backup-confirmation {
  display: grid;
  gap: 14px;
}
.backup-confirmation p { margin: 4px 0; }
.backup-confirmation-actions { flex: none; justify-content: flex-end; }
.backup-confirmation-modal {
  width: min(500px, calc(100vw - 32px));
  border-top: 3px solid var(--vt-color-danger-500);
}
.backup-safety-note {
  padding: 10px 14px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  border-top: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
}
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
.diagnostics-card { margin-top: 16px; overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); }
.diagnostics-heading { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 14px 16px; border-bottom: 1px solid var(--vt-border); }
.diagnostics-heading > div { display: flex; min-width: 0; flex-direction: column; }
.diagnostics-heading small, .diagnostics-loading { color: var(--vt-fg-muted); }
.diagnostics-card :deep(.n-alert) { margin: 12px 14px; }
.diagnostics-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 0; }
.diagnostics-grid > div { min-width: 0; padding: 12px 16px; border-bottom: 1px solid var(--vt-border); }
.diagnostics-grid > div:nth-child(odd) { border-right: 1px solid var(--vt-border); }
.diagnostics-grid dt { margin-bottom: 4px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.diagnostics-grid dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
.diagnostics-grid code { font-size: var(--vt-font-caption); }
.diagnostics-loading { margin: 0; padding: 24px 16px; text-align: center; }
@media (max-width: 760px) {
  .settings-nav { flex-basis: 52px; padding: 16px 7px; }
  .settings-nav-title { display: none; }
  .settings-nav button { justify-content: center; padding: 0; font-size: 0; }
  .settings-content { padding: 28px 20px; }
  .calendar-layout { grid-template-columns: 1fr; }
  .calendar-rule-panel { border-top: 1px solid var(--vt-border); border-left: 0; }
  /* 单列堆叠时输入框恢复满宽。 */
  .calendar-name-input { width: 100%; }
  .diagnostics-grid { grid-template-columns: 1fr; }
  .diagnostics-grid > div:nth-child(odd) { border-right: 0; }
}
@media (max-width: 560px) {
  .settings-content { padding: 24px 14px; }
  .setting-row { align-items: stretch; flex-direction: column; gap: 10px; }
  .setting-row > .setting-control { width: 100%; }
  .calendar-layout { padding: 12px; }
  .mapping-footer { align-items: flex-start; flex-direction: column; }
  .backup-toolbar,
  .backup-confirmation { align-items: stretch; flex-direction: column; }
  .backup-actions,
  .backup-entry-actions,
  .backup-confirmation-actions { justify-content: flex-end; }
  .backup-entry { grid-template-columns: 34px minmax(0, 1fr); }
  .backup-entry-actions { grid-column: 1 / -1; }
}
</style>
