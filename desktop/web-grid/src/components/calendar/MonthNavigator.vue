<script setup lang="ts">
import { computed, ref } from "vue";
import { NDatePicker, NIcon, NInput, NPopover } from "naive-ui";
import { CalendarDays, ChevronDown } from "lucide-vue-next";
import { t } from "@/i18n";
import {
  formatMonthKey,
  monthLabel,
  parseFlexibleMonthKey,
  parseMonthKey,
} from "@/calendar/workCalendar";

const props = defineProps<{
  monthKey: string;
  locale: "zh-CN" | "en-US";
}>();

const emit = defineEmits<{ "update:monthKey": [value: string] }>();

const open = ref(false);
const inputText = ref("");

const label = computed(() => monthLabel(props.monthKey, props.locale));

// NDatePicker 需要 timestamp（毫秒）。当前月 key 解析失败时回退到"现在"。
const monthTimestamp = computed(() => {
  const parsed = parseMonthKey(props.monthKey);
  return parsed ? parsed.getTime() : Date.now();
});

function onPick(ts: number): void {
  emit("update:monthKey", formatMonthKey(new Date(ts)));
  open.value = false;
}

function commitInput(): void {
  const parsed = parseFlexibleMonthKey(inputText.value);
  if (parsed) {
    emit("update:monthKey", parsed);
    inputText.value = "";
    open.value = false;
  }
  // 解析失败：静默，保留 inputText 原文
}
</script>

<template>
  <NPopover
    v-model:show="open"
    trigger="click"
    placement="bottom-start"
    :show-arrow="false"
    :width="260"
  >
    <template #trigger>
      <button
        type="button"
        class="month-navigator-trigger"
        :aria-label="t('settings.workCalendar.chooseMonth')"
      >
        <NIcon :size="14"><CalendarDays /></NIcon>
        <strong>{{ label }}</strong>
        <NIcon :size="14"><ChevronDown /></NIcon>
      </button>
    </template>

    <div class="month-navigator-panel">
      <NDatePicker
        type="month"
        :value="monthTimestamp"
        :input-readonly="true"
        :clearable="false"
        :actions="null"
        @update:value="onPick"
      />
      <NInput
        v-model:value="inputText"
        :placeholder="t('settings.workCalendar.jumpPlaceholder')"
        size="small"
        @keyup.enter="commitInput"
      />
    </div>
  </NPopover>
</template>

<style scoped>
.month-navigator-trigger {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1px solid transparent;
  border-radius: var(--vt-radius-lg, 6px);
  background: transparent;
  color: var(--vt-fg);
  cursor: pointer;
  font: inherit;
  transition: background-color 0.15s, border-color 0.15s;
}
.month-navigator-trigger:hover {
  background: var(--vt-bg-hover, rgba(0, 0, 0, 0.04));
  border-color: var(--vt-border);
}
.month-navigator-trigger strong {
  font-weight: 600;
}
.month-navigator-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px;
}
</style>
