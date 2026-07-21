<script setup lang="ts">
import { computed, ref, watch } from "vue";
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

// 弹层（月份网格）开关。
const open = ref(false);
// 输入框文本。默认显示当前月份的本地化标签；用户聚焦时可键入覆盖，
// 失焦/Enter 时尝试解析为 monthKey，失败则静默回滚到标签。
const text = ref(monthLabel(props.monthKey, props.locale));

const label = computed(() => monthLabel(props.monthKey, props.locale));

// NDatePicker 需要 timestamp（毫秒）。当前月 key 解析失败时回退到"现在"。
const monthTimestamp = computed(() => {
  const parsed = parseMonthKey(props.monthKey);
  return parsed ? parsed.getTime() : Date.now();
});

// 外部（今日按钮 / 上下月箭头）改动 monthKey 时，把输入框同步为新标签。
// 用 open 防御：网格打开期间由 onPick 接管，避免与用户正在编辑的文本打架。
watch(label, (next) => {
  if (!open.value) text.value = next;
});

function onPick(ts: number): void {
  emit("update:monthKey", formatMonthKey(new Date(ts)));
  text.value = monthLabel(formatMonthKey(new Date(ts)), props.locale);
  open.value = false;
}

function commit(): void {
  const parsed = parseFlexibleMonthKey(text.value);
  if (parsed) {
    emit("update:monthKey", parsed);
    text.value = monthLabel(parsed, props.locale);
  } else {
    // 解析失败：静默恢复为当前月份标签。
    text.value = label.value;
  }
}

// 聚焦即全选：用户点进来后直接键入是覆盖整段月份文本，而非插入到标签中间。
// NInput 的 focus 事件透传原生 FocusEvent，target 即输入框元素。
function selectAll(event: FocusEvent): void {
  const el = event.target;
  if (el instanceof HTMLInputElement) el.select();
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
      <NInput
        v-model:value="text"
        class="month-navigator-input"
        :aria-label="t('settings.workCalendar.chooseMonth')"
        :placeholder="t('settings.workCalendar.jumpPlaceholder')"
        size="small"
        @focus="selectAll"
        @blur="commit"
        @keyup.enter="commit"
      >
        <template #prefix>
          <NIcon :size="14" class="month-navigator-icon"><CalendarDays /></NIcon>
        </template>
        <template #suffix>
          <NIcon :size="14" class="month-navigator-icon"><ChevronDown /></NIcon>
        </template>
      </NInput>
    </template>

    <div class="month-navigator-panel">
      <NDatePicker
        type="month"
        panel
        :value="monthTimestamp"
        :actions="null"
        @update:value="onPick"
      />
    </div>
  </NPopover>
</template>

<style scoped>
.month-navigator-input {
  width: 150px;
}
.month-navigator-icon {
  color: var(--vt-fg-muted);
}
.month-navigator-panel {
  display: flex;
  flex-direction: column;
  padding: 4px;
}
</style>
