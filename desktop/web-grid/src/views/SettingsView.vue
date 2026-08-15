<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import {
  NButton,
  NAlert,
  NIcon,
  NInput,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSwitch,
  NTag,
} from "naive-ui";
import {
  ArchiveRestore,
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  HelpCircle,
  Database,
  Download,
  Info,
  Keyboard,
  Palette,
  SlidersHorizontal,
} from "lucide-vue-next";
import brandIconUrl from "@/assets/brand/vibetable.png";
import changelog from "@/generated/changelog.json";
import { QUOTE_STYLES_BY_SOURCE, useUiStore } from "@/stores/uiStore";
import type {
  DailyQuoteSource,
  DailyQuoteStyle,
  DensityMode,
  StartupPage,
  ThemeMode,
  WorkspaceStartupPolicy,
} from "@/stores/uiStore";
import type { Locale } from "@/i18n";
import { t } from "@/i18n";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
import { formatDateKey, formatMonthKey, parseDateKey, shiftMonthKey } from "@/calendar/workCalendar";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import type { WorkCalendarOverrideKind } from "@/calendar/workCalendar";
import type { RuntimeDiagnostics } from "@/contracts/runtimeDiagnosticsContracts";
import { useRuntimeDiagnosticsService } from "@/services/runtimeDiagnosticsService";
import type {
  AppPreferences,
  AppPreferencesUpdate,
  UpdateProxyId,
} from "@/contracts/appPreferencesContracts";
import { useAppPreferencesService } from "@/services/appPreferencesService";
import type { ReleaseUpdateCheckResult } from "@/contracts/releaseUpdateContracts";
import { useReleaseUpdateService } from "@/services/releaseUpdateService";
import WorkspaceProtectionSettings, {
  type WorkspaceProtectionAction,
} from "@/components/settings/WorkspaceProtectionSettings.vue";

type Section = "general" | "calendar" | "storage" | "versions" | "interaction" | "about";
type ChangelogEntry = Readonly<{
  subject: string;
  commit: string | null;
}>;

const ui = useUiStore();
const workCalendar = useWorkCalendarStore();
const current = ref<Section>("general");
const emit = defineEmits<{
  reconnect: [];
  openHelp: [];
  workspaceV2Action: [action: WorkspaceProtectionAction];
}>();
const calendarMonth = ref(formatMonthKey(new Date()));
const selectedCalendarDate = ref(formatDateKey(new Date()));
const diagnosticsService = useRuntimeDiagnosticsService();
const diagnostics = ref<RuntimeDiagnostics | null>(null);
const diagnosticsPhase = ref<"idle" | "loading">("idle");
const diagnosticsError = ref<string | null>(null);
const appPreferencesService = useAppPreferencesService();
const appPreferences = ref<AppPreferences>({
  minimizeToTrayOnClose: false,
  startWithWindows: false,
  updateProxy: "direct",
  customUpdateProxyUrl: "",
});
const appPreferencesPhase = ref<"loading" | "idle" | "saving">("loading");
const appPreferencesError = ref<string | null>(null);
const releaseUpdateService = useReleaseUpdateService();
const releaseUpdate = ref<ReleaseUpdateCheckResult | null>(null);
const releaseUpdatePhase = ref<"idle" | "checking" | "installing">("idle");
const releaseUpdateError = ref<string | null>(null);
const changelogEntries = changelog.entries as readonly ChangelogEntry[];
const updateProxyOptions = computed(() => [
  { label: t("settings.update.proxy.direct"), value: "direct" },
  { label: "ghproxy.net", value: "ghproxyNet" },
  { label: "gh-proxy.com", value: "ghProxyCom" },
  { label: t("settings.update.proxy.custom"), value: "custom" },
]);

onMounted(() => void loadAppPreferences());

watch(current, (section) => {
  if (section === "about") void loadDiagnostics();
});

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

async function loadAppPreferences(): Promise<void> {
  appPreferencesPhase.value = "loading";
  appPreferencesError.value = null;
  try {
    appPreferences.value = await appPreferencesService.get();
  } catch (error) {
    appPreferencesError.value = error instanceof Error
      ? error.message
      : t("settings.appPreferences.failed");
  } finally {
    appPreferencesPhase.value = "idle";
  }
}

async function updateAppPreferences(patch: AppPreferencesUpdate): Promise<void> {
  if (appPreferencesPhase.value !== "idle") return;
  appPreferencesPhase.value = "saving";
  appPreferencesError.value = null;
  try {
    appPreferences.value = await appPreferencesService.update(patch);
  } catch (error) {
    appPreferencesError.value = error instanceof Error
      ? error.message
      : t("settings.appPreferences.failed");
  } finally {
    appPreferencesPhase.value = "idle";
  }
}

async function selectUpdateProxy(value: UpdateProxyId): Promise<void> {
  await updateAppPreferences({ updateProxy: value });
  releaseUpdate.value = null;
  releaseUpdateError.value = null;
}

async function saveCustomUpdateProxy(value: string): Promise<void> {
  await updateAppPreferences({ customUpdateProxyUrl: value });
  releaseUpdate.value = null;
  releaseUpdateError.value = null;
}

async function checkForReleaseUpdate(): Promise<void> {
  if (releaseUpdatePhase.value !== "idle") return;
  releaseUpdatePhase.value = "checking";
  releaseUpdateError.value = null;
  try {
    releaseUpdate.value = await releaseUpdateService.check();
  } catch (error) {
    releaseUpdateError.value = error instanceof Error
      ? error.message
      : t("settings.update.checkFailed");
  } finally {
    releaseUpdatePhase.value = "idle";
  }
}

async function installReleaseUpdate(): Promise<void> {
  if (releaseUpdatePhase.value !== "idle") return;
  releaseUpdatePhase.value = "installing";
  releaseUpdateError.value = null;
  try {
    await releaseUpdateService.install();
  } catch (error) {
    releaseUpdateError.value = error instanceof Error
      ? error.message
      : t("settings.update.installFailed");
    releaseUpdatePhase.value = "idle";
  }
}

function formatDownloadSize(size: number): string {
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function formatReleaseDate(value: string | null): string {
  return value ? value.slice(0, 10) : "";
}

function formatMemory(size: number): string {
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

const sections = computed(() => [
  { key: "general" as const, icon: Palette, label: "settings.general" },
  { key: "calendar" as const, icon: CalendarDays, label: "settings.workCalendar" },
  {
    key: "storage" as const,
    icon: Database,
    label: "workspaceV2.settings.storage",
  },
  {
    key: "versions" as const,
    icon: ArchiveRestore,
    label: "workspaceV2.settings.versions",
  },
  { key: "interaction" as const, icon: SlidersHorizontal, label: "settings.interaction" },
  { key: "about" as const, icon: Info, label: "settings.about" },
]);

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
const workspaceStartupOptions = computed(() => [
  {
    label: t("settings.workspaceStartup.lastWorkspace"),
    value: "lastWorkspace",
  },
  {
    label: t("settings.workspaceStartup.workspaceCenter"),
    value: "workspaceCenter",
  },
]);
const quoteSourceOptions = computed(() => (["hitokoto", "jinrishici", "quotable", "builtin"] as const)
  .map((value) => ({ label: t(`settings.quote.source.${value}`), value })));
const quoteStyleOptions = computed(() => QUOTE_STYLES_BY_SOURCE[ui.dailyQuoteSource]
  .map((value) => ({ label: t(`settings.quote.style.${value}`), value })));

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
      <div
        class="settings-inner"
        :class="{ 'settings-inner--workspace-v2':
          current === 'storage' || current === 'versions' }"
      >
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
              <div>
                <strong>{{ t("settings.workspaceStartup") }}</strong>
                <small>{{ t("settings.workspaceStartup.hint") }}</small>
              </div>
              <NSelect
                :value="ui.workspaceStartupPolicy"
                :aria-label="t('settings.workspaceStartup')"
                :options="workspaceStartupOptions"
                class="setting-control"
                data-testid="workspace-startup-policy-select"
                @update:value="ui.setWorkspaceStartupPolicy($event as WorkspaceStartupPolicy)"
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
              <div>
                <strong>{{ t("settings.minimizeToTrayOnClose") }}</strong>
                <small>{{ t("settings.minimizeToTrayOnClose.hint") }}</small>
              </div>
              <NSwitch
                :value="appPreferences.minimizeToTrayOnClose"
                :disabled="appPreferencesPhase !== 'idle'"
                :loading="appPreferencesPhase === 'saving'"
                :aria-label="t('settings.minimizeToTrayOnClose')"
                data-testid="minimize-to-tray-switch"
                @update:value="updateAppPreferences({ minimizeToTrayOnClose: $event })"
              />
            </div>
            <div class="setting-row">
              <div>
                <strong>{{ t("settings.startWithWindows") }}</strong>
                <small>{{ t("settings.startWithWindows.hint") }}</small>
              </div>
              <NSwitch
                :value="appPreferences.startWithWindows"
                :disabled="appPreferencesPhase !== 'idle'"
                :loading="appPreferencesPhase === 'saving'"
                :aria-label="t('settings.startWithWindows')"
                data-testid="start-with-windows-switch"
                @update:value="updateAppPreferences({ startWithWindows: $event })"
              />
            </div>
            <NAlert
              v-if="appPreferencesError"
              type="error"
              :title="t('settings.appPreferences.failed')"
              data-testid="app-preferences-error"
            >
              {{ appPreferencesError }}
            </NAlert>
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

        <template v-else-if="current === 'storage'">
          <WorkspaceProtectionSettings
            mode="storage"
            @action="emit('workspaceV2Action', $event)"
          />
        </template>

        <template v-else-if="current === 'versions'">
          <WorkspaceProtectionSettings
            mode="versions"
            @action="emit('workspaceV2Action', $event)"
          />
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
            <div class="about-badges">
              <span class="desktop-badge">{{ t("settings.about.desktop") }}</span>
              <span v-if="diagnostics" class="version-badge">v{{ diagnostics.programVersion }}</span>
            </div>
          </section>
          <section class="update-card" data-testid="release-update-card">
            <div class="update-heading">
              <div>
                <strong>{{ t("settings.update.title") }}</strong>
                <small>{{ t("settings.update.hint") }}</small>
              </div>
              <NButton
                size="small"
                data-testid="check-update-button"
                :loading="releaseUpdatePhase === 'checking'"
                :disabled="releaseUpdatePhase === 'installing'"
                @click="checkForReleaseUpdate"
              >
                {{ t("settings.update.check") }}
              </NButton>
            </div>
            <div class="update-proxy-row">
              <div>
                <strong>{{ t("settings.update.proxy") }}</strong>
                <small>{{ t("settings.update.proxyHint") }}</small>
              </div>
              <NSelect
                data-testid="update-proxy-select"
                class="update-proxy-select"
                :value="appPreferences.updateProxy"
                :options="updateProxyOptions"
                :disabled="appPreferencesPhase !== 'idle' || releaseUpdatePhase !== 'idle'"
                @update:value="selectUpdateProxy"
              />
            </div>
            <NInput
              v-if="appPreferences.updateProxy === 'custom'"
              data-testid="custom-update-proxy-input"
              :value="appPreferences.customUpdateProxyUrl"
              :placeholder="t('settings.update.proxyPlaceholder')"
              :disabled="appPreferencesPhase !== 'idle' || releaseUpdatePhase !== 'idle'"
              @change="saveCustomUpdateProxy"
            />
            <NAlert type="info" :show-icon="true" class="proxy-disclosure">
              {{ t("settings.update.proxyDisclosure") }}
            </NAlert>
            <NAlert
              v-if="releaseUpdateError"
              type="error"
              :title="t('settings.update.failed')"
            >
              {{ releaseUpdateError }}
            </NAlert>
            <div v-if="releaseUpdate" class="update-result" data-testid="release-update-result">
              <div class="update-version-line">
                <div>
                  <span>{{ t("settings.update.current") }} v{{ releaseUpdate.currentVersion }}</span>
                  <NIcon :size="16"><ChevronRight /></NIcon>
                  <strong>v{{ releaseUpdate.latestVersion }}</strong>
                </div>
                <NTag :type="releaseUpdate.updateAvailable ? 'success' : 'default'" size="small">
                  {{ releaseUpdate.updateAvailable
                    ? t("settings.update.available")
                    : t("settings.update.latest") }}
                </NTag>
              </div>
              <template v-if="releaseUpdate.updateAvailable">
                <NAlert
                  v-if="!releaseUpdate.canInstall"
                  type="warning"
                  :title="t('settings.update.installUnavailable')"
                >
                  {{ releaseUpdate.installUnavailableReason }}
                </NAlert>
                <NAlert v-if="releaseUpdate.notesTruncated" type="info">
                  {{ t("settings.update.notesTruncated") }}
                </NAlert>
                <div class="release-notes-heading">
                  <strong>{{ t("settings.update.betweenVersions") }}</strong>
                  <small>{{ t("settings.update.downloadSize") }} {{ formatDownloadSize(releaseUpdate.downloadBytes) }}</small>
                </div>
                <ol class="release-notes-list" data-testid="between-version-release-notes">
                  <li v-for="release in releaseUpdate.releases" :key="release.version">
                    <div>
                      <strong>v{{ release.version }} · {{ release.title }}</strong>
                      <time v-if="release.publishedAt">{{ formatReleaseDate(release.publishedAt) }}</time>
                    </div>
                    <pre>{{ release.body || t("settings.update.notesEmpty") }}</pre>
                  </li>
                </ol>
                <NButton
                  type="primary"
                  data-testid="install-update-button"
                  :loading="releaseUpdatePhase === 'installing'"
                  :disabled="!releaseUpdate.canInstall || releaseUpdatePhase !== 'idle'"
                  @click="installReleaseUpdate"
                >
                  <template #icon><NIcon><Download /></NIcon></template>
                  {{ t("settings.update.install") }}
                </NButton>
              </template>
            </div>
          </section>
          <section class="changelog-card" data-testid="about-changelog">
            <div class="changelog-heading">
              <strong>{{ t("settings.about.changelog") }}</strong>
              <small>{{ t("settings.about.changelogHint") }}</small>
            </div>
            <ol v-if="changelogEntries.length" class="changelog-list">
              <li v-for="entry in changelogEntries" :key="entry.commit ?? entry.subject">
                <span class="changelog-marker" aria-hidden="true"></span>
                <span class="changelog-subject">{{ entry.subject }}</span>
                <code v-if="entry.commit" :aria-label="t('settings.about.commit')">
                  {{ entry.commit }}
                </code>
              </li>
            </ol>
            <p v-else class="changelog-empty">{{ t("settings.about.changelogEmpty") }}</p>
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
              <div><dt>{{ t("settings.about.systemVersion") }}</dt><dd>{{ diagnostics.operatingSystem }}</dd></div>
              <div><dt>{{ t("settings.about.programVersion") }}</dt><dd>{{ diagnostics.programVersion }}</dd></div>
              <div><dt>{{ t("settings.about.runtimeVersion") }}</dt><dd>{{ diagnostics.dotnetVersion }}</dd></div>
              <div><dt>{{ t("settings.about.pocketBaseVersion") }}</dt><dd>{{ diagnostics.pocketBaseVersion }}</dd></div>
              <div><dt>{{ t("settings.about.memory") }}</dt><dd>{{ formatMemory(diagnostics.memoryBytes) }}</dd></div>
              <div><dt>索引状态</dt><dd>{{ diagnostics.index.state }} · G{{ diagnostics.index.generation }}</dd></div>
              <div><dt>后台任务</dt><dd>{{ diagnostics.jobs.running }} 运行 / {{ diagnostics.jobs.failed }} 失败</dd></div>
              <div><dt>待恢复 mutation</dt><dd>{{ diagnostics.pendingMutationRevision || "—" }}</dd></div>
              <div><dt>脱敏日志</dt><dd>{{ diagnostics.logs.length }} 条事件</dd></div>
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
.settings-inner--workspace-v2 { max-width: 1080px; }
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
.setting-card > :deep(.n-alert) { margin: 12px 14px; }
.calendar-workbench { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.calendar-toolbar { display: grid; grid-template-columns: 32px auto auto 1fr 32px; align-items: center; gap: 9px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
.calendar-toolbar > span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); text-align: right; }
/* "今日/Today" 文字很短，但 Naive UI 默认按钮水平内边距（~14px×2）让它在
   工具栏里偏宽、挤占有限空间。收紧到与圆形图标按钮视觉一致。 */
.calendar-today-btn {
  min-width: 48px;
}
.calendar-today-btn :deep(.n-button__content) {
  width: 100%;
  justify-content: center;
  padding: 0 6px;
}
.calendar-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(210px, 240px); gap: 18px; padding: 16px; }
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
.calendar-rule-panel :deep(.n-radio-group) { min-width: 0; max-width: 100%; }
.calendar-rule-panel { container-type: inline-size; }
.calendar-rule-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 5px; }
.calendar-rule-options :deep(.n-radio-button) {
  width: 100%;
  min-width: 0;
  justify-content: center;
}
.calendar-rule-options :deep(.n-radio__label) {
  width: 100%;
  text-align: center;
  justify-content: center;
  white-space: nowrap;
}
.calendar-footer { display: flex; align-items: center; gap: 14px; padding: 10px 14px; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); border-top: 1px solid var(--vt-border); background: var(--vt-bg-subtle); }
.calendar-footer > span { display: inline-flex; align-items: center; gap: 5px; }
.calendar-footer small { margin-left: auto; }
.calendar-seal { display: grid; place-items: center; width: 18px; height: 18px; font-size: 9px; font-style: normal; font-weight: 700; }
.calendar-seal--rest { color: var(--vt-color-danger); border-radius: 50%; background: color-mix(in srgb, var(--vt-color-danger) 14%, transparent); }
.calendar-seal--work { color: var(--vt-color-success); border-radius: 5px; background: color-mix(in srgb, var(--vt-color-success) 15%, transparent); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); }
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
.about-badges { display: flex; align-items: center; gap: 8px; }
.version-badge { color: var(--vt-color-primary-700); font-family: Consolas, monospace; font-size: var(--vt-font-caption); }
.changelog-card { margin-top: 16px; padding: 18px 20px 8px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: linear-gradient(145deg, var(--vt-bg) 0%, var(--vt-bg-subtle) 100%); }
.update-card { display: flex; flex-direction: column; gap: 14px; margin-top: 16px; padding: 18px 20px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); background: var(--vt-bg); }
.update-heading, .update-proxy-row, .update-version-line, .release-notes-list li > div { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.update-heading > div, .update-proxy-row > div, .release-notes-heading { display: flex; flex-direction: column; gap: 3px; }
.update-heading small, .update-proxy-row small, .release-notes-heading small, .release-notes-list time { color: var(--vt-fg-muted); }
.update-proxy-select { width: min(260px, 46%); }
.proxy-disclosure { font-size: var(--vt-font-caption); }
.update-result { display: flex; flex-direction: column; gap: 14px; padding-top: 2px; }
.update-version-line > div { display: flex; align-items: center; gap: 6px; }
.release-notes-list { display: flex; flex-direction: column; gap: 10px; margin: 0; padding: 0; list-style: none; }
.release-notes-list li { padding: 13px 14px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-subtle); }
.release-notes-list pre { margin: 8px 0 0; color: var(--vt-fg-muted); font: inherit; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.changelog-heading { display: flex; flex-direction: column; gap: 3px; margin-bottom: 14px; }
.changelog-heading small, .changelog-empty { color: var(--vt-fg-muted); }
.changelog-list { margin: 0; padding: 0; list-style: none; }
.changelog-list li { position: relative; display: grid; grid-template-columns: 12px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 42px; padding: 8px 0; border-top: 1px solid var(--vt-border); }
.changelog-marker { width: 7px; height: 7px; border: 2px solid var(--vt-color-primary-500); border-radius: 50%; background: var(--vt-bg); box-shadow: 0 0 0 3px color-mix(in srgb, var(--vt-color-primary-500) 12%, transparent); }
.changelog-subject { min-width: 0; overflow-wrap: anywhere; }
.changelog-list code { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.changelog-empty { margin: 0; padding: 6px 0 16px; }
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
  .calendar-rule-options { grid-template-columns: 1fr; }
  .diagnostics-grid { grid-template-columns: 1fr; }
  .diagnostics-grid > div:nth-child(odd) { border-right: 0; }
  .about-card { align-items: flex-start; }
  .about-badges { align-items: flex-end; flex-direction: column; }
  .changelog-list li { grid-template-columns: 12px minmax(0, 1fr); }
  .changelog-list code { grid-column: 2; }
  .update-heading, .update-proxy-row { align-items: stretch; flex-direction: column; }
  .update-proxy-select { width: 100%; }
}
@media (max-width: 560px) {
  .settings-content { padding: 24px 14px; }
  .setting-row { align-items: stretch; flex-direction: column; gap: 10px; }
  .setting-row > .setting-control { width: 100%; }
  .calendar-layout { padding: 12px; }
}
</style>
