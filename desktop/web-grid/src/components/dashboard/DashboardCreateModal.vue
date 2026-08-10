<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NInput, NModal } from "naive-ui";
import { DASHBOARD_TEMPLATES, type DashboardTemplateId } from "@/dashboard";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const props = defineProps<{ show: boolean }>();
const emit = defineEmits<{ close: []; create: [templateId: DashboardTemplateId, name: string] }>();
const ui = useUiStore();
const selected = ref<DashboardTemplateId>("blank");
const name = ref("");
const templates = computed(() => DASHBOARD_TEMPLATES.map((item) => ({
  ...item,
  label: item.name[ui.locale],
})));

watch(() => props.show, (show) => {
  if (show) {
    selected.value = "blank";
    name.value = t("dashboard.create.defaultName");
  }
});

function submit(): void {
  const value = name.value.trim();
  if (!value) return;
  emit("create", selected.value, value);
}
</script>

<template>
  <NModal :show="show" preset="card" :title="t('dashboard.create.title')" class="create-dashboard-modal" data-testid="dashboard-create-modal" @close="emit('close')" @mask-click="emit('close')">
    <label class="field-label">{{ t("dashboard.field.name") }}<NInput v-model:value="name" maxlength="128" show-count autofocus data-testid="dashboard-create-name" /></label>
    <div class="template-grid" role="radiogroup" :aria-label="t('dashboard.create.template')">
      <button v-for="template in templates" :key="template.id" type="button" class="template-card" :class="{ selected: selected === template.id }" :data-testid="`dashboard-create-template-${template.id}`" role="radio" :aria-checked="selected === template.id" @click="selected = template.id">
        <span class="template-preview"><i v-for="panel in template.panels.slice(0, 6)" :key="panel.key" :style="{ gridColumn: `span ${Math.max(1, Math.round(panel.position.width / 3))}` }"></i></span>
        <strong>{{ template.label }}</strong><small>{{ t("dashboard.panelCount", { count: template.panels.length }) }}</small>
      </button>
    </div>
    <template #footer><div class="modal-actions"><NButton @click="emit('close')">{{ t("common.cancel") }}</NButton><NButton type="primary" data-testid="dashboard-create-submit" :disabled="!name.trim()" @click="submit">{{ t("dashboard.action.create") }}</NButton></div></template>
  </NModal>
</template>

<style scoped>
.create-dashboard-modal { width:min(720px,calc(100vw - 32px)); }
.field-label { display:grid; gap:6px; font-size:12px; color:var(--vt-fg-muted); }
.template-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; margin-top:18px; }
.template-card { display:grid; grid-template-columns:100px 1fr; grid-template-rows:1fr 1fr; gap:2px 12px; padding:12px; text-align:left; color:var(--vt-fg); border:1px solid var(--vt-border); border-radius:var(--vt-radius-lg); background:var(--vt-bg); cursor:pointer; }
.template-card:hover,.template-card.selected { border-color:var(--vt-color-primary-500); background:var(--vt-color-primary-50); }
.template-preview { grid-row:1/3; display:grid; grid-template-columns:repeat(4,1fr); gap:3px; height:54px; padding:5px; border-radius:6px; background:var(--vt-bg-sunken); }
.template-preview i { min-height:10px; border-radius:2px; background:color-mix(in srgb,var(--vt-color-primary-500) 45%,var(--vt-bg)); }
.template-card small { color:var(--vt-fg-subtle); }
.modal-actions { display:flex; justify-content:flex-end; gap:8px; }
@media (max-width:620px) { .template-grid { grid-template-columns:1fr; } }
</style>
