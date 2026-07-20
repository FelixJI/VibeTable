<script setup lang="ts">
import { computed } from "vue";
import { buildMonthDays, type WorkCalendarOverride } from "@/calendar/workCalendar";

const props = withDefaults(defineProps<{
  monthKey: string;
  overrides: readonly WorkCalendarOverride[];
  locale?: "zh-CN" | "en-US";
  selectedDate?: string | null;
  compact?: boolean;
  interactive?: boolean;
}>(), {
  locale: "zh-CN",
  selectedDate: null,
  compact: false,
  interactive: false,
});

const emit = defineEmits<{ select: [date: string] }>();
const days = computed(() => buildMonthDays(props.monthKey, props.overrides));
const weekLabels = computed(() => (
  props.locale === "zh-CN"
    ? ["日", "一", "二", "三", "四", "五", "六"]
    : ["S", "M", "T", "W", "T", "F", "S"]
));

function dayTitle(day: (typeof days.value)[number]): string {
  if (day.name) return `${day.date} · ${day.name}`;
  if (day.kind === "weekend") return `${day.date} · ${props.locale === "zh-CN" ? "周末休息" : "Weekend"}`;
  return day.date;
}
</script>

<template>
  <div class="work-calendar" :class="{ 'work-calendar--compact': compact }">
    <div class="work-calendar__week" aria-hidden="true">
      <span v-for="(label, index) in weekLabels" :key="`${label}-${index}`">{{ label }}</span>
    </div>
    <div class="work-calendar__days" role="grid">
      <button
        v-for="day in days"
        :key="day.date"
        type="button"
        role="gridcell"
        class="work-calendar__day"
        :class="[
          `work-calendar__day--${day.kind}`,
          {
            'work-calendar__day--other': !day.inCurrentMonth,
            'work-calendar__day--today': day.isToday,
            'work-calendar__day--selected': day.date === selectedDate,
          },
        ]"
        :disabled="!interactive || !day.inCurrentMonth"
        :title="dayTitle(day)"
        :aria-label="dayTitle(day)"
        :aria-selected="day.date === selectedDate"
        :data-date="day.date"
        @click="emit('select', day.date)"
      >
        <span class="work-calendar__number">{{ day.day }}</span>
        <span v-if="day.marker && day.inCurrentMonth" class="work-calendar__marker">{{ day.marker }}</span>
        <span v-if="day.name && !compact && day.inCurrentMonth" class="work-calendar__name">{{ day.name }}</span>
      </button>
    </div>
  </div>
</template>
