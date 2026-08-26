<script setup lang="ts">
import { NButton, NIcon, NInput } from "naive-ui";
import { LayoutDashboard, Plus, Search } from "@lucide/vue";
import { computed, ref } from "vue";
import type { DashboardListEntry } from "@/stores/dashboardStore";
import { t } from "@/i18n";

const props = defineProps<{ dashboards: readonly DashboardListEntry[]; selectedId?: string | null }>();
const emit = defineEmits<{ select: [dashboardId: string]; create: [] }>();
const search = ref("");
const filtered = computed(() => {
  const needle = search.value.trim().toLocaleLowerCase();
  return needle ? props.dashboards.filter((item) => `${item.name} ${item.note}`.toLocaleLowerCase().includes(needle)) : props.dashboards;
});
</script>

<template>
  <aside class="dashboard-sidebar">
    <div class="sidebar-heading"><strong>{{ t("dashboard.title") }}</strong><NButton quaternary size="tiny" data-testid="dashboard-create" :aria-label="t('dashboard.action.create')" @click="emit('create')"><NIcon><Plus /></NIcon></NButton></div>
    <NInput v-model:value="search" size="small" clearable :placeholder="t('dashboard.search')" class="sidebar-search"><template #prefix><NIcon><Search /></NIcon></template></NInput>
    <div class="sidebar-list" role="listbox" :aria-label="t('dashboard.list')">
      <button
        v-for="item in filtered"
        :key="item.id"
        class="dashboard-row"
        :class="{ 'dashboard-row--active': item.id === selectedId }"
        :data-testid="`dashboard-select-${item.id}`"
        role="option"
        :aria-selected="item.id === selectedId"
        @click="emit('select', item.id)"
      >
        <LayoutDashboard :size="15" />
        <span><strong>{{ item.name }}</strong><small>{{ t("dashboard.panelCount", { count: item.panelCount }) }}</small></span>
      </button>
      <div v-if="filtered.length === 0" class="sidebar-empty">{{ t("dashboard.empty.list") }}</div>
    </div>
  </aside>
</template>

<style scoped>
.dashboard-sidebar { flex:0 0 232px; display:flex; flex-direction:column; min-width:0; border-right:1px solid var(--vt-border); background:var(--vt-bg); }
.sidebar-heading { display:flex; align-items:center; justify-content:space-between; height:46px; padding:0 10px 0 14px; border-bottom:1px solid var(--vt-border-subtle); font-size:13px; }
.sidebar-search { width:calc(100% - 20px); margin:10px; }
.sidebar-list { overflow:auto; padding:0 7px 10px; }
.dashboard-row { display:flex; align-items:center; gap:9px; width:100%; min-height:42px; padding:5px 9px; border:0; border-radius:var(--vt-radius-md); color:var(--vt-fg-muted); background:transparent; text-align:left; cursor:pointer; }
.dashboard-row:hover { background:var(--vt-bg-sunken); color:var(--vt-fg); }
.dashboard-row--active { color:var(--vt-fg-accent); background:var(--vt-color-primary-50); }
.dashboard-row span { display:flex; min-width:0; flex:1; flex-direction:column; }
.dashboard-row strong { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:12px; font-weight:600; }
.dashboard-row small { margin-top:2px; font-size:10px; color:var(--vt-fg-subtle); }
.sidebar-empty { padding:24px 10px; text-align:center; color:var(--vt-fg-subtle); font-size:12px; }
</style>
