# 设置页日历：年月手选跳转 + 今日按钮

**日期：** 2026-07-21
**状态：** 待评审
**关联需求：** 设置里日历支持点击年月手动选择并跳转，跳转后有"转到今日"按钮；灵活键盘输入 `2026-7` / `2026.7` / `2026.07` 等多种格式也要能跳转。

---

## 背景与现状

- 设置页 `SettingsView.vue` 的日历工具栏（`desktop/web-grid/src/views/SettingsView.vue:322-332`）目前结构：

  ```
  [‹]  2026年7月  · 3 special dates configured  [›]
  ```

  "2026年7月"是静态 `<strong>` 文本，只能用左右箭头逐月翻动，无法直接跳转到任意年月。

- 月份网格由 `WorkCalendarMonth.vue` 渲染（`desktop/web-grid/src/components/calendar/WorkCalendarMonth.vue`），只响应 `:month-key` prop 变化；它本身不渲染年月头，也不含跳转交互。

- 日历 helpers 在 `desktop/web-grid/src/calendar/workCalendar.ts`，已有 `formatMonthKey` / `parseMonthKey`（`/^(\d{4})-(\d{2})$/`）/ `shiftMonthKey` / `monthLabel`。无灵活解析函数。

- 项目无第三方日期选择库（package.json 依赖只有 `naive-ui@2.38.2`、`lucide-vue-next` 等）。日历为纯自研。

### 关键可行性结论（NDatePicker 原生做不到灵活输入）

读本地 `node_modules/naive-ui/es/date-picker/src/DatePicker.mjs:566` 与 `utils.mjs:223`：

```js
// handleSingleUpdateValue（每次键盘输入触发）
const result = strictParse(v, mergedFormatRef.value, new Date(), dateFnsOptionsRef.value);

// strictParse
const result = parse(text, pattern, ...);        // date-fns parse
if (format(result, pattern, option) === string) return result;  // 往返一致
else return new Date(NaN);                         // 否则判无效
```

- NDatePicker **没有 `parser` prop**，只有一个 `format` 字符串同时用于显示和解析。
- `strictParse` 要求 `format(parse(text, pattern), pattern) === text` 往返一致。
- 因此一个 `format` 模式只能接受**一种分隔符 + 一种月份位数**（如 `yyyy-MM` 只认 `2026-07`，`yyyy-M` 只认 `2026-7`，`yyyy.MM` 只认 `2026.07`），三者无法同时满足。

结论：`2026-7` / `2026.7` / `2026.07` / `2026/7` 这种多分隔符 + 1/2 位月份的混合输入，NDatePicker 原生不支持。必须自写解析。

## 决策记录

| 决策 | 原因 |
|---|---|
| 用 NDatePicker `type="month"` 当**纯月份网格面板**（`input-readonly`），另加独立 NInput 做灵活输入 | NDatePicker 面板交互成熟、复用 naive-ui；其 strictParse 限制通过禁用原生输入框绕开 |
| 弹窗内同时放网格 + 输入框（而非工具栏常驻输入框） | 工具栏保持简洁；两种跳转方式集中在同一弹窗，无"两处入口同步"问题 |
| 把弹窗逻辑独立成 `MonthNavigator.vue` 组件 | 当前仅 SettingsView 用，但 HomeView 迷你日历未来可复用同一交互；边界清晰、可独立测试 |
| 灵活解析支持年月 + 完整日期两种格式，忽略日跳到对应月 | 用户明确要求 `2026-7-15` 类输入也跳到月份；日做合法性校验（`2026-2-30` → null） |
| "今日"按钮始终显示 | 简单一致，发现性好；用户确认 |
| 无效输入静默回滚 + 保留原文本 | 设置页轻量场景，不上浮错误提示；用户确认 |
| 选月/回车后自动关闭弹窗 | 符合"选完就走"直觉；用户确认 |
| `20267`（5 位无分隔）接受为 `2026-07` | 少打字符更省事；与 `2026-7` 直觉一致；用户确认 |

## 变更范围

### 1. 新增 `parseFlexibleMonthKey` 纯函数

**文件：** `desktop/web-grid/src/calendar/workCalendar.ts`

接受两种格式，统一返回 `"YYYY-MM"` 或 `null`：

| 格式类 | 示例 | 处理 |
|--------|------|------|
| 年月（4 位年 + 1-2 位月） | `2026-7` / `2026.07` / `2026/7` / `2026 7` / `20267` | 校验 month ∈ [1,12]、year ∈ [1900,9999] |
| 完整日期（年 + 月 + 日） | `2026-7-15` / `2026.07.05` / `2026/7/15` / `2026 7 15` / `20260715` | 校验 month ∈ [1,12] **且** 日合法（`new Date(y, m-1, d)` 往返校验：`getFullYear/getMonth/getDate` 全部一致） |

分隔符 `[-./\s]` 可选，同一输入里可混用（`2026-7.15` 也接受）。

```ts
// 年月：2026-7 / 2026.07 / 2026/7 / 2026 7 / 20267
const FLEXIBLE_YM = /^\s*(\d{4})[-./\s]?(\d{1,2})\s*$/;
// 完整日期带分隔符：2026-7-15 / 2026.07.05 / 2026/7/15 / 2026 7 15（分隔符可混用）
const FLEXIBLE_YMD_SEP = /^\s*(\d{4})[-./\s](\d{1,2})[-./\s](\d{1,2})\s*$/;
// 完整日期无分隔：20260715（恰好 8 位）
const FLEXIBLE_YMD_COMPACT = /^\s*(\d{4})(\d{2})(\d{2})\s*$/;

export function parseFlexibleMonthKey(text: string): string | null {
  const trimmed = text.trim();
  const ymdSep = FLEXIBLE_YMD_SEP.exec(trimmed);
  const ymdCompact = FLEXIBLE_YMD_COMPACT.exec(trimmed);
  const ymd = ymdSep ?? ymdCompact;
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

**正则设计要点（避免歧义）：**
- 年月（`FLEXIBLE_YM`）：分隔符可选，这样 `20267`（4 位年 + 1 位月）也能解析。
- 完整日期带分隔符（`FLEXIBLE_YMD_SEP`）：段间**必须**至少一个 `[-./\s]` 分隔符，否则 `2026-13` 会被错误拆成 `2026-1-3`。分隔符可混用（`2026-7.15` 接受）。
- 完整日期无分隔（`FLEXIBLE_YMD_COMPACT`）：恰好 8 位数字（`YYYYMMDD`），月/日各 2 位，避免与年月歧义。

**边界用例（测试覆盖）：**
- 年月：`2026-7` / `2026.07` / `2026/7` / `2026 7` / `20267` → `2026-07`
- 完整日期：`2026-7-15` / `2026.07.05` / `2026/7/15` / `2026 7 15` / `20260715` → `2026-07`
- 非法：`2026-2-30` → null（日不合法）、`2026-13` → null（月超界）、`hello` → null、`2026` → null（只有年）、`2026-0` → null、`99999`（5 位）→ null

### 2. 新增 `MonthNavigator.vue` 组件

**文件：** `desktop/web-grid/src/components/calendar/MonthNavigator.vue`（新）

封装"可点击的月份触发器 + NPopover 弹窗（内含 NDatePicker 月份面板 + NInput 灵活输入）"。

**Props / Emits：**
```ts
props: {
  monthKey: string;      // "YYYY-MM"，受控
  locale: "zh-CN" | "en-US";
}
emits: {
  "update:monthKey": [value: string];
}
```

**状态：**
- `open: ref(false)` — popover 显隐
- `inputText: ref("")` — NInput 输入文本

**Computed：**
- `label` → `monthLabel(props.monthKey, props.locale)`（复用现有 helper）
- `monthTimestamp` → `parseMonthKey(props.monthKey)?.getTime() ?? Date.now()`（NDatePicker 需 timestamp）

**方法：**
```ts
function onPick(ts: number) {
  emit("update:monthKey", formatMonthKey(new Date(ts)));
  open.value = false;        // 选月后关闭
}

function commitInput() {
  const parsed = parseFlexibleMonthKey(inputText.value);
  if (parsed) {
    emit("update:monthKey", parsed);
    inputText.value = "";     // 清空，下次输入干净
    open.value = false;       // 回车/失焦后关闭
  }
  // 解析失败：静默，保留 inputText 原文
}
```

**双重触发与焦点说明：**
- `@keyup.enter` 后输入框可能失焦再次触发 `@blur` → 此时 `inputText` 已清空，`parseFlexibleMonthKey("")` 返回 `null`，无副作用。
- 用户点击 NDatePicker 月份网格时，NInput 失焦触发 `commitInput`：若输入框为空或无效，`open` 保持 true，不误关弹窗；若有效则正常跳转（与点网格行为一致，不冲突）。
- 用户点击网格本身的跳转由 NDatePicker 的 `@update:value` → `onPick` 处理，与输入框解析路径独立。

**模板骨架：**
```vue
<NPopover trigger="click" placement="bottom-start" :show-arrow="false"
          v-model:show="open" :width="260">
  <template #trigger>
    <button type="button" class="month-navigator-trigger"
            :aria-label="t('settings.workCalendar.chooseMonth')">
      <NIcon><CalendarDays /></NIcon>
      <strong>{{ label }}</strong>
      <NIcon><ChevronDown /></NIcon>
    </button>
  </template>

  <NDatePicker type="month" :value="monthTimestamp" :input-readonly="true"
               :clearable="false" :actions="null"
               @update:value="onPick" />

  <NInput v-model:value="inputText"
          :placeholder="t('settings.workCalendar.jumpPlaceholder')"
          size="small"
          @keyup.enter="commitInput"
          @blur="commitInput" />
</NPopover>
```

**NDatePicker 关键 prop：**
- `:input-readonly="true"` — 禁掉原生键盘输入（绕开 strictParse 多格式限制）
- `:actions="null"` — 去掉 naive-ui 默认的"现在/确认"底部按钮

**CSS（组件内 scoped）：** `.month-navigator-trigger` 用 `display: inline-flex; gap: 4px; align-items: center` + hover 高亮，颜色用现有 CSS 变量（`--vt-fg` / `--vt-border` / `--vt-bg-hover`），匹配 naive-ui 按钮观感。

### 3. SettingsView 集成

**文件：** `desktop/web-grid/src/views/SettingsView.vue`

工具栏（替换第 323-332 行）：

```vue
<div class="calendar-toolbar">
  <NButton quaternary circle :aria-label="t('settings.workCalendar.previous')"
           @click="calendarMonth = shiftMonthKey(calendarMonth, -1)">
    <template #icon><NIcon><ChevronLeft /></NIcon></template>
  </NButton>

  <MonthNavigator :month-key="calendarMonth" :locale="ui.locale"
                  @update:month-key="calendarMonth = $event" />

  <span>{{ t("settings.workCalendar.overrides", { count: workCalendar.overrideCount }) }}</span>

  <NButton quaternary size="small" :aria-label="t('settings.workCalendar.today')"
           @click="calendarMonth = formatMonthKey(new Date())">
    {{ t("settings.workCalendar.today") }}
  </NButton>

  <NButton quaternary circle :aria-label="t('settings.workCalendar.next')"
           @click="calendarMonth = shiftMonthKey(calendarMonth, 1)">
    <template #icon><NIcon><ChevronRight /></NIcon></template>
  </NButton>
</div>
```

**新增 import：** `MonthNavigator`（SettingsView 不需要 `ChevronDown`/`CalendarDays`，它们在 MonthNavigator 内部）。

**CSS（第 532 行）：**
```css
/* 从 4 列 → 5 列，新增"今日"按钮列 */
.calendar-toolbar {
  grid-template-columns: 32px auto 1fr auto 32px;
}
```
其余 `.calendar-toolbar` 规则（padding、border-bottom）不变。

### 4. i18n 新增

两个 locale 文件，加在 `settings.workCalendar.*` 块：

| key | en-US | zh-CN |
|-----|-------|-------|
| `settings.workCalendar.chooseMonth` | `Choose month` | `选择月份` |
| `settings.workCalendar.today` | `Today` | `今日` |
| `settings.workCalendar.jumpPlaceholder` | `Jump to e.g. 2026-7` | `跳转到 如 2026-7` |

## 测试策略

遵循现有 `workCalendar.test.ts` 和 `SettingsView.test.ts` 模式（TDD）。

### 单元测试：`workCalendar.test.ts`

新增 ~15 条用例覆盖 `parseFlexibleMonthKey`：
- 年月正向：`2026-7` / `2026.07` / `2026/7` / `2026 7` / `20267` → `2026-07`
- 完整日期正向：`2026-7-15` / `2026.07.05` / `2026/7/15` / `2026 7 15` / `20260715` → `2026-07`
- 非法：`2026-2-30` → null、`2026-13` → null、`hello` → null、`2026` → null、`2026-0` → null、`99999` → null

### 端到端测试：`SettingsView.test.ts`

新增用例：
- 点击月份触发器 → 弹出面板 → 点选某月 → 工具栏月份更新 + popover 关闭
- 在输入框敲 `2025-12` 回车 → 跳到 2025-12 + popover 关闭
- 点击"今日"按钮 → 月份回到当月
- 输入 `hello` 回车 → 不跳转，输入框文本保留

## 不改动范围

- **HomeView 迷你日历**：只读（HomeView.vue:98 `calendarMonth` 是 const），本次不动。`MonthNavigator` 独立组件为未来 HomeView 复用留接口。
- **WorkCalendarMonth.vue**：纯展示组件，已响应 `:month-key` 变化，无需改动。
- **后端 / RPC**：纯前端 UI 改动，无后端契约变化。
- **workCalendarStore**：月份视图状态是 SettingsView 本地 ref，不入 store。

## 开放问题

无。所有决策点已与用户确认（见"决策记录"）。
