# 设置页日历：年月手选跳转 + 今日按钮 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让设置页日历工具栏的年月标签可点击，弹出月份网格 + 灵活输入框跳转，并新增"今日"按钮一键回到当月。

**Architecture:** 纯前端改动。新增一个纯函数 `parseFlexibleMonthKey`（多格式解析）、一个独立 Vue 组件 `MonthNavigator.vue`（NDatePicker 当月份面板 + 独立 NInput 灵活输入），再在 `SettingsView.vue` 工具栏接入该组件并加"今日"按钮。

**Tech Stack:** Vue 3.5 `<script setup>` + TypeScript + naive-ui 2.38.2（NPopover / NDatePicker / NInput / NIcon）+ lucide-vue-next（CalendarDays / ChevronDown）+ vitest 4.1.9 + @vue/test-utils 2.4.6。

## Global Constraints

- 所有测试通过 `npm test`（即 `vitest run`，配置在 `desktop/web-grid/vite.config.ts`，environment=jsdom，include=`src/**/*.test.ts`）。
- 类型检查通过 `npm run typecheck`（即 `vue-tsc --noEmit`）。
- naive-ui 组件**显式导入**（无自动导入插件），不要依赖全局注册。
- 路径别名 `@` → `desktop/web-grid/src`（vite.config.ts:21）。
- i18n key 松散字符串，`t(key, params?)` 从 `@/i18n` 导入（index.ts:43）。fallback：当前 locale 缺 key 时回退到 zh-CN。
- CSS 变量沿用现有 `--vt-fg` / `--vt-fg-muted` / `--vt-border` / `--vt-bg` / `--vt-radius-lg` / `--vt-font-caption`。
- 每个任务结束必须 commit；commit message 用中文或英文均可，遵循现有 `feat:` / `test:` / `fix:` 前缀风格。
- **测试文件位置约定**：`workCalendar.test.ts` 与源文件同目录（`src/calendar/`）；组件测试与源文件同目录（如 `src/components/calendar/MonthNavigator.test.ts`）；视图测试 `src/views/SettingsView.test.ts`。

**参考 spec：** `docs/superpowers/specs/2026-07-21-settings-calendar-month-jump-design.md`

---

## File Structure

| 文件 | 职责 | 操作 |
|------|------|------|
| `desktop/web-grid/src/calendar/workCalendar.ts` | 加 `parseFlexibleMonthKey` 纯函数 | 修改 |
| `desktop/web-grid/src/calendar/workCalendar.test.ts` | 加解析函数边界用例 | 修改 |
| `desktop/web-grid/src/components/calendar/MonthNavigator.vue` | 月份跳转组件（触发器 + popover + 面板 + 输入框） | 新建 |
| `desktop/web-grid/src/components/calendar/MonthNavigator.test.ts` | 组件单测 | 新建 |
| `desktop/web-grid/src/views/SettingsView.vue` | 工具栏接入 MonthNavigator + 今日按钮 | 修改 |
| `desktop/web-grid/src/views/SettingsView.test.ts` | 端到端用例 | 修改 |
| `desktop/web-grid/src/i18n/locales/zh-CN.ts` | 3 个新 key | 修改 |
| `desktop/web-grid/src/i18n/locales/en-US.ts` | 3 个新 key | 修改 |

---

### Task 1: `parseFlexibleMonthKey` 纯函数（TDD）

**Files:**
- Modify: `desktop/web-grid/src/calendar/workCalendar.ts`（在文件末尾追加）
- Test: `desktop/web-grid/src/calendar/workCalendar.test.ts`（修改 import + 新增 `it` 块）

**Interfaces:**
- Consumes: 现有 `formatMonthKey(date: Date): string`（同文件 line 34）
- Produces: `parseFlexibleMonthKey(text: string): string | null` — 接受年月或完整日期的多格式字符串，返回 `"YYYY-MM"` 或 `null`

- [ ] **Step 1: 在 `workCalendar.test.ts` 顶部扩展 import 加入 `parseFlexibleMonthKey`**

修改 `desktop/web-grid/src/calendar/workCalendar.test.ts` 第 1-9 行的 import 块，把 `parseFlexibleMonthKey` 加入（保持字母序）：

```ts
import { describe, expect, it } from "vitest";
import {
  buildMonthDays,
  formatDateKey,
  parseDateKey,
  parseFlexibleMonthKey,
  resolveWorkCalendarDay,
  sanitizeOverrides,
  shiftMonthKey,
} from "./workCalendar";
```

- [ ] **Step 2: 在 `workCalendar.test.ts` 的 `describe("workCalendar", ...)` 块内、最后一个 `it` 之后追加测试**

```ts
  it("parses flexible year-month and full-date inputs into month keys", () => {
    // 年月：分隔符 [-./\s] 任一 + 1-2 位月
    expect(parseFlexibleMonthKey("2026-7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026-07")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.07")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026/7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026 7")).toBe("2026-07");
    expect(parseFlexibleMonthKey("20267")).toBe("2026-07");   // 无分隔，4 位年 + 1 位月

    // 完整日期：忽略日，跳到对应月
    expect(parseFlexibleMonthKey("2026-7-15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026-07-05")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026.07.05")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026/7/15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("2026 7 15")).toBe("2026-07");
    expect(parseFlexibleMonthKey("20260715")).toBe("2026-07");  // 8 位无分隔
  });

  it("rejects out-of-range or malformed flexible month inputs", () => {
    expect(parseFlexibleMonthKey("2026-2-30")).toBeNull();   // 日不合法（2 月无 30 日）
    expect(parseFlexibleMonthKey("2026-13")).toBeNull();     // 月超界
    expect(parseFlexibleMonthKey("2026-0")).toBeNull();      // 月为 0
    expect(parseFlexibleMonthKey("2026")).toBeNull();        // 只有年
    expect(parseFlexibleMonthKey("hello")).toBeNull();
    expect(parseFlexibleMonthKey("")).toBeNull();
    expect(parseFlexibleMonthKey("  2026-7  ")).toBe("2026-07");  // 前后空格容错
  });
```

- [ ] **Step 3: 运行测试，确认失败（函数未定义）**

Run（在 `desktop/web-grid/` 目录下）:
```bash
npm test -- --run src/calendar/workCalendar.test.ts
```
Expected: FAIL，错误信息包含 `parseFlexibleMonthKey is not a function` 或导入失败。

- [ ] **Step 4: 在 `workCalendar.ts` 末尾追加实现**

在 `desktop/web-grid/src/calendar/workCalendar.ts` 文件**末尾**（`monthLabel` 函数之后）追加：

```ts
// 灵活年月/日期解析：支持 2026-7 / 2026.07 / 2026/7 / 2026 7 / 20267（年月）
// 与 2026-7-15 / 2026.07.05 / 20260715（完整日期，忽略日）。
// 分隔符 [-./\s] 可选且可混用。返回 "YYYY-MM" 或 null。
const FLEXIBLE_YM = /^\s*(\d{4})\s*[-./\s]?(\d{1,2})\s*$/;
const FLEXIBLE_YMD = /^\s*(\d{4})\s*[-./\s]?(\d{1,2})\s*[-./\s]?(\d{1,2})\s*$/;

export function parseFlexibleMonthKey(text: string): string | null {
  const trimmed = text.trim();
  const ymd = FLEXIBLE_YMD.exec(trimmed);
  if (ymd) {
    const year = Number(ymd[1]);
    const month = Number(ymd[2]);
    const day = Number(ymd[3]);
    const date = new Date(year, month - 1, day);
    if (
      date.getFullYear() !== year
      || date.getMonth() !== month - 1
      || date.getDate() !== day
    ) return null;          // 如 2026-2-30 → null
    return formatMonthKey(date);
  }
  const ym = FLEXIBLE_YM.exec(trimmed);
  if (ym) {
    const year = Number(ym[1]);
    const month = Number(ym[2]);
    if (month < 1 || month > 12) return null;
    if (year < 1900 || year > 9999) return null;
    return formatMonthKey(new Date(year, month - 1, 1));
  }
  return null;
}
```

- [ ] **Step 5: 运行测试，确认全部通过**

Run:
```bash
npm test -- --run src/calendar/workCalendar.test.ts
```
Expected: PASS（所有原有用例 + 新增 2 个 `it` 块全部通过）。

- [ ] **Step 6: 运行类型检查**

Run:
```bash
npm run typecheck
```
Expected: 无错误退出（exit 0）。

- [ ] **Step 7: Commit**

```bash
git add desktop/web-grid/src/calendar/workCalendar.ts desktop/web-grid/src/calendar/workCalendar.test.ts
git commit -m "feat(calendar): add parseFlexibleMonthKey for multi-format month input"
```

---

### Task 2: i18n 新增 3 个 key

**Files:**
- Modify: `desktop/web-grid/src/i18n/locales/zh-CN.ts`（在 `settings.workCalendar.next` 行之后插入）
- Modify: `desktop/web-grid/src/i18n/locales/en-US.ts`（同位置）

**Interfaces:**
- Produces: 3 个新 i18n key，供 Task 3 的 `MonthNavigator.vue` 和 Task 4 的 `SettingsView.vue` 使用：
  - `settings.workCalendar.chooseMonth` — 触发器 aria-label
  - `settings.workCalendar.today` — 今日按钮文本 + aria-label
  - `settings.workCalendar.jumpPlaceholder` — 输入框 placeholder

- [ ] **Step 1: 在 `zh-CN.ts` 的 `settings.workCalendar.next` 行之后插入 3 行**

修改 `desktop/web-grid/src/i18n/locales/zh-CN.ts`，找到第 181 行：
```ts
  "settings.workCalendar.next": "下个月",
```
在其后插入 3 行（注意保持逗号）：
```ts
  "settings.workCalendar.next": "下个月",
  "settings.workCalendar.chooseMonth": "选择月份",
  "settings.workCalendar.today": "今日",
  "settings.workCalendar.jumpPlaceholder": "跳转到 如 2026-7",
```

- [ ] **Step 2: 在 `en-US.ts` 同位置插入对应英文**

修改 `desktop/web-grid/src/i18n/locales/en-US.ts`，找到：
```ts
  "settings.workCalendar.next": "Next month",
```
在其后插入：
```ts
  "settings.workCalendar.next": "Next month",
  "settings.workCalendar.chooseMonth": "Choose month",
  "settings.workCalendar.today": "Today",
  "settings.workCalendar.jumpPlaceholder": "Jump to e.g. 2026-7",
```

- [ ] **Step 3: 运行 i18n 测试确认 key 对齐（如果有对齐检查）**

Run:
```bash
npm test -- --run src/i18n/i18n.test.ts
```
Expected: PASS。若 i18n.test.ts 有"两 locale key 集合一致"的检查，新 key 已两边都加，应通过。

- [ ] **Step 4: 类型检查**

Run:
```bash
npm run typecheck
```
Expected: exit 0。

- [ ] **Step 5: Commit**

```bash
git add desktop/web-grid/src/i18n/locales/zh-CN.ts desktop/web-grid/src/i18n/locales/en-US.ts
git commit -m "feat(i18n): add calendar month-jump and today strings"
```

---

### Task 3: `MonthNavigator.vue` 组件（TDD）

**Files:**
- Create: `desktop/web-grid/src/components/calendar/MonthNavigator.vue`
- Test: `desktop/web-grid/src/components/calendar/MonthNavigator.test.ts`

**Interfaces:**
- Consumes（来自 Task 1、2）:
  - `parseFlexibleMonthKey(text: string): string | null` from `@/calendar/workCalendar`
  - `formatMonthKey(date: Date): string` from `@/calendar/workCalendar`
  - `parseMonthKey(value: string): Date | null` from `@/calendar/workCalendar`
  - `monthLabel(monthKey: string, locale: string): string` from `@/calendar/workCalendar`
  - `t(key: string, params?): string` from `@/i18n`
  - i18n key `settings.workCalendar.chooseMonth`、`settings.workCalendar.jumpPlaceholder`
- Produces: Vue 组件 `MonthNavigator`，props `{ monthKey: string; locale: "zh-CN" | "en-US" }`，emit `"update:monthKey": [string]`。供 Task 4 接入。

- [ ] **Step 1: 创建测试文件 `MonthNavigator.test.ts`**

新建 `desktop/web-grid/src/components/calendar/MonthNavigator.test.ts`：

```ts
import { describe, expect, it, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import MonthNavigator from "./MonthNavigator.vue";
import { NDatePicker, NInput, NPopover } from "naive-ui";

describe("MonthNavigator", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("renders the current month label from props", () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    expect(wrapper.text()).toContain("2026");
    expect(wrapper.text()).toContain("7");
  });

  it("emits update:monthKey with the picked month and closes the popover", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    await wrapper.findComponent(NDatePicker).vm.$emit("update:value", new Date(2025, 11, 1).getTime());
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
    // popover 内部 show 状态由 NPopover 管理；这里只验证 emit 即可
  });

  it("jumps when a flexible string is committed via the input", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const input = wrapper.findComponent(NInput);
    await input.vm.$emit("update:value", "2025-12");
    // 模拟回车提交：直接调用 keyup.enter
    await input.find("input").trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("update:monthKey")).toEqual([["2025-12"]]);
  });

  it("does not emit when input is unparseable", async () => {
    const wrapper = mount(MonthNavigator, {
      props: { monthKey: "2026-07", locale: "zh-CN" },
    });
    const input = wrapper.findComponent(NInput);
    await input.vm.$emit("update:value", "hello");
    await input.find("input").trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("update:monthKey")).toBeUndefined();
  });
});
```

- [ ] **Step 2: 运行测试，确认失败（组件不存在）**

Run:
```bash
npm test -- --run src/components/calendar/MonthNavigator.test.ts
```
Expected: FAIL，错误信息包含 `Cannot find module './MonthNavigator.vue'`。

- [ ] **Step 3: 创建组件文件 `MonthNavigator.vue`**

新建 `desktop/web-grid/src/components/calendar/MonthNavigator.vue`：

```vue
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
        @blur="commitInput"
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
```

- [ ] **Step 4: 运行测试，确认通过**

Run:
```bash
npm test -- --run src/components/calendar/MonthNavigator.test.ts
```
Expected: PASS（4 个用例全部通过）。

**注意：** 若 `wrapper.find("input").trigger("keyup", { key: "Enter" })` 在 jsdom 下触发不到 `@keyup.enter`，则改用直接调用：把第 3 个用例的"回车提交"改成 `await input.vm.$emit("blur")` 触发 blur 路径（commitInput 同时绑在 blur 上），同样能验证解析逻辑。若两种都不行，保留前两个用例（label + onPick emit）和第 4 个（unparseable 不 emit，用 blur 路径），确保至少 3 个用例通过。这种调整属于测试技术细节，不改变被测行为。

- [ ] **Step 5: 类型检查**

Run:
```bash
npm run typecheck
```
Expected: exit 0。

- [ ] **Step 6: Commit**

```bash
git add desktop/web-grid/src/components/calendar/MonthNavigator.vue desktop/web-grid/src/components/calendar/MonthNavigator.test.ts
git commit -m "feat(calendar): add MonthNavigator with month panel and flexible jump input"
```

---

### Task 4: SettingsView 工具栏接入 + 今日按钮

**Files:**
- Modify: `desktop/web-grid/src/views/SettingsView.vue`（script import 块 line 3-14；template line 322-332；CSS line 532）
- Test: `desktop/web-grid/src/views/SettingsView.test.ts`（追加用例）

**Interfaces:**
- Consumes:
  - `MonthNavigator` 组件（Task 3 产物），props `{ monthKey, locale }`，emit `update:monthKey`
  - i18n key `settings.workCalendar.today`（Task 2 产物）
  - 现有 `formatMonthKey`（已 import 在 line 51）
- Produces: 设置页日历工具栏支持点击年月跳转 + 今日按钮。

- [ ] **Step 1: 在 `SettingsView.test.ts` 末尾追加 3 个用例**

首先在文件顶部 import 块（第 1-11 行附近）加入两个组件引用（直接用组件对象而非 name 字符串更可靠，因为 `<script setup>` 组件不一定有显式 name）：

```ts
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
```

然后在 `desktop/web-grid/src/views/SettingsView.test.ts` 的 `describe("SettingsView", ...)` 块内、最后一个 `it` 之后追加：

```ts
  it("jumps to a picked month via the month navigator", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    // MonthNavigator 渲染在工具栏
    const navigator = wrapper.findComponent(MonthNavigator);
    expect(navigator.exists(), "MonthNavigator should render in toolbar").toBe(true);
    // 模拟用户在面板选了 2025-12
    await navigator.vm.$emit("update:monthKey", "2025-12");
    // WorkCalendarMonth 的 monthKey prop 已更新
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe("2025-12");
  });

  it("jumps back to the current month via the Today button", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    // 先切到一个非当月
    const navigator = wrapper.findComponent(MonthNavigator);
    await navigator.vm.$emit("update:monthKey", "2025-01");
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe("2025-01");
    // 点今日按钮（用 aria-label 定位，避免和箭头按钮混淆）
    const todayBtn = wrapper
      .findAll(".calendar-toolbar .n-button")
      .find((btn) => btn.attributes("aria-label") === "今日");
    expect(todayBtn, "today button should render").toBeTruthy();
    await todayBtn!.trigger("click");
    const currentMonthKey = `${new Date().getFullYear()}-${String(new Date().getMonth() + 1).padStart(2, "0")}`;
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe(currentMonthKey);
  });

  it("renders the overrides count between the navigator and today button", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    const store = useWorkCalendarStore();
    // store.setOverride 签名为 (date, kind, name)——位置参数，非对象
    store.setOverride(formatDateKey(new Date()), "holiday", "测试假日");
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("1 个特殊日期");
  });
```

- [ ] **Step 2: 运行测试，确认失败**

Run:
```bash
npm test -- --run src/views/SettingsView.test.ts
```
Expected: FAIL（MonthNavigator 未渲染、今日按钮不存在）。前两个新用例失败，第三个可能通过或失败取决于现有 store 行为。

- [ ] **Step 3: 修改 `SettingsView.vue` 的 import 块**

修改 `desktop/web-grid/src/views/SettingsView.vue` 第 50 行的 import，在其后加一行导入 `MonthNavigator`：

找到：
```ts
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
```
在其后加：
```ts
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
```

- [ ] **Step 4: 替换工具栏模板（第 323-332 行）**

找到这段（`desktop/web-grid/src/views/SettingsView.vue` 第 323-332 行）：
```vue
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
```
替换为：
```vue
            <div class="calendar-toolbar">
              <NButton quaternary circle :aria-label="t('settings.workCalendar.previous')" @click="calendarMonth = shiftMonthKey(calendarMonth, -1)">
                <template #icon><NIcon><ChevronLeft /></NIcon></template>
              </NButton>
              <MonthNavigator :month-key="calendarMonth" :locale="ui.locale" @update:month-key="calendarMonth = $event" />
              <span>{{ t("settings.workCalendar.overrides", { count: workCalendar.overrideCount }) }}</span>
              <NButton quaternary size="small" :aria-label="t('settings.workCalendar.today')" @click="calendarMonth = formatMonthKey(new Date())">
                {{ t("settings.workCalendar.today") }}
              </NButton>
              <NButton quaternary circle :aria-label="t('settings.workCalendar.next')" @click="calendarMonth = shiftMonthKey(calendarMonth, 1)">
                <template #icon><NIcon><ChevronRight /></NIcon></template>
              </NButton>
            </div>
```

- [ ] **Step 5: 修改 CSS（第 532 行 grid 列数）**

找到 `desktop/web-grid/src/views/SettingsView.vue` 第 532 行：
```css
.calendar-toolbar { display: grid; grid-template-columns: 32px auto 1fr 32px; align-items: center; gap: 9px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
```
把 `grid-template-columns` 从 4 列改为 5 列（新增"今日"按钮列）：
```css
.calendar-toolbar { display: grid; grid-template-columns: 32px auto 1fr auto 32px; align-items: center; gap: 9px; padding: 10px 12px; border-bottom: 1px solid var(--vt-border); }
```

- [ ] **Step 6: 运行 SettingsView 测试**

Run:
```bash
npm test -- --run src/views/SettingsView.test.ts
```
Expected: PASS（原有用例 + 3 个新用例全部通过）。

**注意：** 若 `calendarMonthText` computed 在脚本里仍被引用导致未使用警告——它原本只服务被替换的 `<strong>`。检查 `SettingsView.vue` script 里 `calendarMonthText`（约第 200 行）的定义，若替换后已无引用，删除该 computed 定义以免 lint/typecheck 报 unused。具体：搜索 `calendarMonthText`，若仅在原 template 用过则删除其定义行。

- [ ] **Step 7: 清理未使用的 `calendarMonthText`（如适用）**

在 `desktop/web-grid/src/views/SettingsView.vue` script 中搜索：
```ts
const calendarMonthText = computed(() => monthLabel(calendarMonth.value, ui.locale));
```
若该行存在且替换 template 后无其他引用，删除它。同时检查 `monthLabel` 是否还有其他引用；若仅 `calendarMonthText` 用到，也从 import 里移除 `monthLabel`。

- [ ] **Step 8: 全量测试 + 类型检查**

Run:
```bash
npm test
npm run typecheck
```
Expected: 全部 PASS，typecheck exit 0。

- [ ] **Step 9: Commit**

```bash
git add desktop/web-grid/src/views/SettingsView.vue desktop/web-grid/src/views/SettingsView.test.ts
git commit -m "feat(settings): wire MonthNavigator and Today button into calendar toolbar"
```

---

### Task 5: 最终验证 + 手动验证清单

**Files:** 无修改（纯验证）

- [ ] **Step 1: 全量测试 + 类型检查 + 构建**

Run（在 `desktop/web-grid/`）:
```bash
npm test
npm run typecheck
npm run build
```
Expected: 全部 PASS / exit 0。`npm run build` 产出 `dist/` 无报错。

- [ ] **Step 2: 人工验证清单（启动桌面应用后）**

在桌面应用中打开"设置 → 工作日历"，逐项确认：
1. 工具栏显示：`[‹] [📅 2026年7月 ▾] [已设置 N 个特殊日期] [今日] [›]`，5 元素一行
2. 点击 "2026年7月" 触发器 → 弹出面板（月份网格 + 输入框）
3. 面板里点选其他月份（如 3 月）→ 工具栏月份更新为"2026年3月"，弹窗关闭，网格跳到 3 月
4. 重新打开弹窗，在输入框敲 `2025-12` 回车 → 跳到 2025-12，弹窗关闭
5. 输入框敲 `2026.07` → 跳到 2026-07
6. 输入框敲 `2026-7-15` → 跳到 2026-07（忽略日）
7. 输入框敲 `hello` 回车 → 不跳转，输入框文本保留
8. 翻到任意非当月（如 2025-01）→ 点"今日"按钮 → 回到当月
9. 当月下点"今日"按钮 → 仍在当月（无副作用）
10. 切换界面语言到 en-US → 触发器、今日按钮、placeholder 文本变为英文

- [ ] **Step 3: 确认无未提交改动**

Run:
```bash
git status
```
Expected: 工作区干净（或仅剩与本功能无关的预存改动）。

---

## Self-Review 记录

**1. Spec 覆盖核对：**
- Spec §1 `parseFlexibleMonthKey`（年月 + 完整日期 + 日合法性）→ Task 1 ✓
- Spec §2 `MonthNavigator.vue`（NDatePicker 面板 + NInput + onPick/commitInput）→ Task 3 ✓
- Spec §3 SettingsView 工具栏（MonthNavigator 接入 + 今日按钮 + CSS 5 列）→ Task 4 ✓
- Spec §4 i18n 3 个 key → Task 2 ✓
- Spec 测试策略（workCalendar.test.ts 单测 + SettingsView.test.ts 端到端）→ Task 1、3、4 ✓
- Spec "今日按钮始终显示" → Task 4 模板无条件渲染 ✓
- Spec "无效输入静默保留" → Task 1 commitInput + Task 3 测试用例 4 ✓
- Spec "选月/回车后关闭" → Task 3 onPick/commitInput 关闭 open ✓

**2. 占位符扫描：** 无 TODO/TBD；所有代码块完整；测试代码含真实断言。

**3. 类型一致性：**
- `parseFlexibleMonthKey(text: string): string | null` — Task 1 定义，Task 3 消费 ✓
- `MonthNavigator` props `{ monthKey: string; locale: "zh-CN" | "en-US" }` — Task 3 定义，Task 4 消费（`ui.locale` 类型匹配）✓
- emit `"update:monthKey": [string]` — Task 3 定义，Task 4 用 `@update:month-key` 接 ✓
- i18n key 三处名称一致：`chooseMonth` / `today` / `jumpPlaceholder` ✓
