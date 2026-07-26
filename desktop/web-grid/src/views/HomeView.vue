<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NButton, NIcon, NInput } from "naive-ui";
import {
  ArrowRight,
  Database,
  FilePlus2,
  RefreshCw,
  Search,
  Sparkles,
  Table2,
} from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { collectionLabel } from "@/components/layout/collectionLabel";
import { t } from "@/i18n";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import { formatMonthKey } from "@/calendar/workCalendar";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { loadDailyQuote, type DailyQuote } from "@/services/dailyQuoteService";

const workspace = useWorkspaceStore();
const ui = useUiStore();
const workCalendar = useWorkCalendarStore();
const query = ref("");

const emit = defineEmits<{
  openTable: [name: string];
  newTable: [];
  openAdmin: [];
}>();

const now = new Date();
const localeName = computed(() => (ui.locale === "zh-CN" ? "zh-CN" : "en-US"));
const dateText = computed(() =>
  new Intl.DateTimeFormat(localeName.value, {
    month: "long",
    day: "numeric",
    weekday: "long",
  }).format(now),
);

const greetingKey =
  now.getHours() < 11
    ? "home.greeting.morning"
    : now.getHours() < 18
      ? "home.greeting.afternoon"
      : "home.greeting.evening";

const availableByName = computed(
  () => new Map(workspace.collections.map((item) => [item.collection, item])),
);
const displayNames = computed(() => workspace.displayNames);
const recent = computed(() =>
  ui.recentTables
    .map((item) => ({ recent: item, collection: availableByName.value.get(item.name) }))
    .filter((item) => item.collection),
);
const fallbackRecent = computed(() =>
  workspace.collections.slice(0, 5).map((collection) => ({
    recent: { name: collection.collection, openedAt: 0 },
    collection,
  })),
);
const continueItems = computed(() =>
  recent.value.length > 0 ? recent.value : fallbackRecent.value,
);
const searchResults = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase();
  if (!needle) return [];
  return workspace.collections
    .filter((item) =>
      `${collectionLabel(item, displayNames.value)} ${item.collection}`.toLocaleLowerCase().includes(needle),
    )
    .slice(0, 6);
});

const defaultQuotes = [
  "home.quote.1",
  "home.quote.2",
  "home.quote.3",
  "home.quote.4",
  "home.quote.5",
] as const;
const movieQuotes = [
  "home.quote.movie.1",
  "home.quote.movie.2",
  "home.quote.movie.3",
  "home.quote.movie.4",
] as const;
const builtinQuoteFallbacks = [...defaultQuotes, ...movieQuotes] as const;
const daySeed = Math.floor(
  Date.UTC(now.getFullYear(), now.getMonth(), now.getDate()) / 86_400_000,
);
let fallbackCursor = daySeed;

function getFallbackQuoteKeys(): readonly string[] {
  return ui.dailyQuoteSource === "builtin" ? builtinQuoteFallbacks : defaultQuotes;
}

function nextFallbackQuote(): DailyQuote {
  const quotes = getFallbackQuoteKeys();
  const key = quotes[fallbackCursor % quotes.length];
  fallbackCursor += 1;
  return { text: t(key), attribution: "", url: "", origin: "builtin" };
}
const dailyQuote = ref<DailyQuote>(nextFallbackQuote());
const quoteLoading = ref(false);
const quoteSourceLabel = computed(() => t(`settings.quote.source.${ui.dailyQuoteSource}`));
let quoteRequestId = 0;
const calendarMonth = formatMonthKey(now);
const monthText = computed(() =>
  new Intl.DateTimeFormat(localeName.value, { year: "numeric", month: "long" }).format(now),
);
const homeAsideRef = ref<HTMLElement | null>(null);
const syncedContinueHeight = ref(0);

const continueCardStyle = computed(() =>
  syncedContinueHeight.value > 0 ? { minHeight: `${syncedContinueHeight.value}px` } : undefined,
);

function syncContinueHeight(): void {
  const wideLayout =
    typeof window.matchMedia === "function"
      ? window.matchMedia("(min-width: 901px)").matches
      : window.innerWidth >= 901;
  if (!wideLayout) {
    syncedContinueHeight.value = 0;
    return;
  }
  if (!homeAsideRef.value) {
    syncedContinueHeight.value = 0;
    return;
  }
  syncedContinueHeight.value = Math.ceil(homeAsideRef.value.getBoundingClientRect().height);
}

const scheduleContinueHeightSync = () => void nextTick(syncContinueHeight);
let asideObserver: ResizeObserver | null = null;

watch(
  () => [ui.showMiniCalendar, ui.showDailyQuote, ui.locale, ui.dailyQuoteSource, ui.dailyQuoteStyle, dailyQuote.value.text],
  scheduleContinueHeightSync,
  { immediate: true },
);

onMounted(() => {
  if (typeof ResizeObserver !== "undefined" && homeAsideRef.value) {
    asideObserver = new ResizeObserver(scheduleContinueHeightSync);
    asideObserver.observe(homeAsideRef.value);
  }
  window.addEventListener("resize", scheduleContinueHeightSync);
  scheduleContinueHeightSync();
});

onBeforeUnmount(() => {
  asideObserver?.disconnect();
  window.removeEventListener("resize", scheduleContinueHeightSync);
});

async function refreshDailyQuote(): Promise<void> {
  const requestId = ++quoteRequestId;
  const fallback = nextFallbackQuote();
  dailyQuote.value = fallback;
  if (!ui.showDailyQuote || ui.activeView !== "home") return;
  quoteLoading.value = true;
  const quote = await loadDailyQuote({
    fallback,
    locale: ui.locale,
    source: ui.dailyQuoteSource,
    style: ui.dailyQuoteStyle,
  });
  if (requestId !== quoteRequestId) return;
  dailyQuote.value = quote;
  quoteLoading.value = false;
}

watch(
  () => [ui.activeView, ui.showDailyQuote, ui.locale, ui.dailyQuoteSource, ui.dailyQuoteStyle] as const,
  ([view, show]) => {
    if (view === "home" && show) void refreshDailyQuote();
    else {
      quoteRequestId += 1;
      quoteLoading.value = false;
    }
  },
  { immediate: true },
);

function relativeTime(timestamp: number): string {
  if (!timestamp) return t("home.recent.available");
  const minutes = Math.max(0, Math.floor((Date.now() - timestamp) / 60_000));
  if (minutes < 1) return t("home.recent.justNow");
  if (minutes < 60) return t("home.recent.minutes", { count: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("home.recent.hours", { count: hours });
  return t("home.recent.days", { count: Math.floor(hours / 24) });
}

function openTable(name: string) {
  query.value = "";
  emit("openTable", name);
}
</script>

<template>
  <section class="home-view" data-testid="home-view">
    <header class="home-header">
      <div>
        <p class="eyebrow">{{ dateText }}</p>
        <h1>{{ t(greetingKey) }}</h1>
        <p class="home-subtitle">{{ t("home.subtitle") }}</p>
      </div>
      <div class="search-wrap">
        <NInput
          v-model:value="query"
          clearable
          :placeholder="t('home.search')"
          :aria-label="t('home.search')"
          data-testid="home-search"
        >
          <template #prefix><NIcon :size="16"><Search /></NIcon></template>
        </NInput>
        <div v-if="query" class="search-results">
          <button
            v-for="item in searchResults"
            :key="item.collection"
            type="button"
            @click="openTable(item.collection)"
          >
            <NIcon :size="16"><Table2 /></NIcon>
            <span>{{ collectionLabel(item, displayNames) }}</span>
            <small v-if="collectionLabel(item, displayNames) !== item.collection">{{ item.collection }}</small>
          </button>
          <p v-if="searchResults.length === 0">{{ t("home.search.empty") }}</p>
        </div>
      </div>
    </header>

    <div class="home-grid">
      <main class="home-main">
        <section class="content-card continue-card" :style="continueCardStyle">
          <div class="section-heading">
            <div>
              <p class="section-kicker">{{ t("home.continue.kicker") }}</p>
              <h2>{{ t("home.continue.title") }}</h2>
            </div>
            <NButton text type="primary" @click="ui.navigate('tables')">
              {{ t("home.allTables") }}
              <template #icon><NIcon :size="15"><ArrowRight /></NIcon></template>
            </NButton>
          </div>

          <div v-if="continueItems.length" class="recent-list">
            <button
              v-for="item in continueItems"
              :key="item.recent.name"
              type="button"
              class="recent-row"
              data-testid="home-recent-table"
              @click="openTable(item.recent.name)"
            >
              <span class="table-symbol"><NIcon :size="17"><Table2 /></NIcon></span>
              <span class="recent-name">
                <strong>{{ collectionLabel(item.collection!, displayNames) }}</strong>
                <small>{{ relativeTime(item.recent.openedAt) }}</small>
              </span>
              <NIcon :size="16" class="row-arrow"><ArrowRight /></NIcon>
            </button>
          </div>

          <div v-else class="empty-guide">
            <div class="empty-copy">
              <span class="empty-icon"><NIcon :size="20"><Sparkles /></NIcon></span>
              <div>
                <h3>{{ t("home.empty.title") }}</h3>
                <p>{{ t("home.empty.description") }}</p>
              </div>
            </div>
            <div class="guide-steps">
              <button type="button" @click="emit('newTable')">
                <span>01</span><NIcon :size="17"><FilePlus2 /></NIcon>
                <strong>{{ t("home.empty.create") }}</strong>
                <small>{{ t("home.empty.createHint") }}</small>
              </button>
              <div>
                <span>02</span><NIcon :size="17"><Table2 /></NIcon>
                <strong>{{ t("home.empty.paste") }}</strong>
                <small>{{ t("home.empty.pasteHint") }}</small>
              </div>
              <button type="button" @click="emit('openAdmin')">
                <span>03</span><NIcon :size="17"><Database /></NIcon>
                <strong>{{ t("home.empty.admin") }}</strong>
                <small>{{ t("home.empty.adminHint") }}</small>
              </button>
            </div>
          </div>
        </section>
      </main>

      <aside class="home-aside" ref="homeAsideRef">
        <section v-if="ui.showMiniCalendar" class="content-card calendar-card">
          <div class="mini-heading">
            <span>{{ t("home.today") }}</span><strong>{{ monthText }}</strong>
          </div>
          <WorkCalendarMonth
            :month-key="calendarMonth"
            :overrides="workCalendar.overrides"
            :locale="ui.locale"
            compact
          />
          <div class="calendar-legend">
            <span><i class="legend-rest">休</i>{{ t("calendar.legend.rest") }}</span>
            <span><i class="legend-work">班</i>{{ t("calendar.legend.work") }}</span>
          </div>
        </section>

        <section v-if="ui.showDailyQuote" class="content-card quote-card">
          <div class="quote-heading">
            <p>{{ t("home.quote.label") }}<span>{{ quoteSourceLabel }}</span></p>
            <NButton
              text
              circle
              size="tiny"
              :loading="quoteLoading"
              :aria-label="t('home.quote.refresh')"
              data-testid="quote-refresh"
              @click="refreshDailyQuote"
            >
              <template #icon><NIcon :size="14"><RefreshCw /></NIcon></template>
            </NButton>
          </div>
          <blockquote>{{ dailyQuote.text }}</blockquote>
          <footer>
            <a v-if="dailyQuote.origin !== 'builtin'" :href="dailyQuote.url" target="_blank" rel="noreferrer">
              {{ dailyQuote.attribution || quoteSourceLabel }}
            </a>
            <span v-else>{{ t("home.quote.builtin") }}</span>
          </footer>
        </section>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.home-view {
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: auto;
  padding: 28px clamp(24px, 4vw, 52px) 40px;
  background: var(--vt-bg-subtle);
}
.home-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 32px;
  max-width: 1180px;
  margin: 0 auto 24px;
}
.eyebrow, .section-kicker {
  margin: 0 0 4px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
h1, h2, h3, p { margin-top: 0; }
h1 {
  margin-bottom: 4px;
  font-size: 24px;
  font-weight: 650;
  letter-spacing: -0.02em;
}
.home-subtitle { margin-bottom: 0; color: var(--vt-fg-muted); }
.search-wrap { position: relative; width: min(380px, 38vw); }
.search-results {
  position: absolute;
  z-index: 40;
  top: calc(100% + 6px);
  right: 0;
  left: 0;
  padding: 5px;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg-elevated);
  box-shadow: var(--vt-shadow-2);
}
.search-results button {
  display: grid;
  grid-template-columns: 20px 1fr auto;
  align-items: center;
  width: 100%;
  padding: 8px;
  color: var(--vt-fg);
  text-align: left;
  border: 0;
  border-radius: var(--vt-radius-md);
  background: transparent;
  cursor: pointer;
}
.search-results button:hover { background: var(--vt-bg-sunken); }
.search-results small { color: var(--vt-fg-muted); }
.search-results p { margin: 10px; color: var(--vt-fg-muted); }
.home-grid {
  display: grid;
  grid-template-columns: minmax(0, 7fr) minmax(230px, 3fr);
  gap: 16px;
  align-items: start;
  max-width: 1180px;
  margin: 0 auto;
}
.content-card {
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg);
}
.continue-card { min-height: 0; padding: 22px; transition: min-height 160ms var(--vt-ease, ease); }
.section-heading { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.section-heading h2 { margin: 0; font-size: var(--vt-font-heading); font-weight: 600; }
.recent-list { border-top: 1px solid var(--vt-border); }
.recent-row {
  display: flex;
  align-items: center;
  width: 100%;
  gap: 12px;
  padding: 13px 8px;
  color: var(--vt-fg);
  text-align: left;
  border: 0;
  border-bottom: 1px solid var(--vt-border);
  background: transparent;
  cursor: pointer;
}
.recent-row:hover { background: var(--vt-bg-subtle); }
.table-symbol, .empty-icon {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  color: var(--vt-color-primary-500);
  border-radius: var(--vt-radius-md);
  background: var(--vt-color-primary-50);
}
:root.dark .table-symbol, :root.dark .empty-icon { background: rgba(91, 139, 255, 0.14); }
.recent-name { display: flex; flex: 1; flex-direction: column; min-width: 0; }
.recent-name strong { overflow: hidden; font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
.recent-name small { color: var(--vt-fg-muted); }
.row-arrow { color: var(--vt-fg-muted); opacity: 0; transform: translateX(-4px); transition: 150ms var(--vt-ease); }
.recent-row:hover .row-arrow { opacity: 1; transform: none; }
.empty-guide { padding: 26px 4px 4px; }
.empty-copy { display: flex; gap: 12px; margin-bottom: 24px; }
.empty-copy h3 { margin-bottom: 4px; font-size: var(--vt-font-title); }
.empty-copy p { margin-bottom: 0; color: var(--vt-fg-muted); }
.guide-steps { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.guide-steps > * {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 5px 8px;
  align-items: center;
  min-height: 116px;
  padding: 14px;
  color: var(--vt-fg);
  text-align: left;
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-lg);
  background: var(--vt-bg-subtle);
}
.guide-steps button { cursor: pointer; }
.guide-steps button:hover { border-color: var(--vt-color-primary-200); background: var(--vt-color-primary-50); }
.guide-steps span, .guide-steps small { grid-column: 1 / 3; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.guide-steps strong { font-weight: 500; }
.home-aside { display: flex; flex-direction: column; gap: 12px; }
.calendar-card, .quote-card { padding: 16px; }
.mini-heading { display: flex; justify-content: space-between; margin-bottom: 12px; }
.mini-heading span { color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.mini-heading strong { font-weight: 500; }
.calendar-legend { display: flex; gap: 10px; margin-top: 9px; color: var(--vt-fg-muted); font-size: 10px; }
.calendar-legend span { display: inline-flex; align-items: center; gap: 4px; }
.calendar-legend i { display: grid; place-items: center; width: 14px; height: 14px; font-size: 8px; font-style: normal; font-weight: 700; }
.legend-rest { color: var(--vt-color-danger); border-radius: 50%; background: color-mix(in srgb, var(--vt-color-danger) 14%, transparent); }
.legend-work { color: var(--vt-color-success); border-radius: 4px; background: color-mix(in srgb, var(--vt-color-success) 15%, transparent); }
.quote-card { background: #fbfaf7; }
:root.dark .quote-card { background: #24231f; }
.quote-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 10px; }
.quote-card p { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin: 0; color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
.quote-card p span { padding: 1px 6px; border-radius: 999px; background: color-mix(in srgb, var(--vt-fg-muted) 9%, transparent); font-size: 10px; }
.quote-card blockquote { margin: 0; color: var(--vt-fg-secondary); line-height: 1.75; }
.quote-card footer { margin-top: 10px; font-size: 10px; }
.quote-card footer a, .quote-card footer span { color: var(--vt-fg-muted); text-decoration: none; }
.quote-card footer a:hover { color: var(--vt-color-primary-500); }
@media (max-width: 900px) {
  .home-view { padding: 24px; }
  .home-grid { grid-template-columns: 1fr; }
  .home-aside { display: grid; grid-template-columns: 1fr 1fr; }
}
@media (max-width: 680px) {
  .home-view { padding: 20px clamp(16px, 4vw, 24px) 28px; }
  .home-header { align-items: stretch; flex-direction: column; gap: 16px; }
  .search-wrap { width: 100%; }
  .continue-card { min-height: 0; padding: 18px; }
  .section-heading { align-items: flex-start; gap: 10px; }
  .guide-steps { grid-template-columns: 1fr; }
  .home-aside { grid-template-columns: minmax(0, 1fr); }
}
@media (max-height: 620px) {
  .home-view { padding-top: 20px; padding-bottom: 24px; }
  .continue-card { min-height: 0; }
}
</style>
