<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NCheckbox, NInput, NSpin } from "naive-ui";
import type { HeaderMenuOption } from "@/grid/headerFilterMenu";

/** 勾选草稿只在应用时提交；分页追加不覆盖用户已取消的项。 */
defineOptions({ name: "HeaderFilterMenu" });
const props = defineProps<{
  fieldTitle: string;
  options: readonly HeaderMenuOption[];
  selection: readonly string[] | null;
  status: "ready" | "loading" | "error" | "unavailable";
  hasMore?: boolean;
  loadingMore?: boolean;
  limitation?: string | null;
  validateSelection?: (keys: readonly string[]) => string | null;
}>();
const emit = defineEmits<{
  apply: [keys: readonly string[]];
  clear: [];
  close: [];
  loadMore: [];
}>();

const search = ref("");
/** 草稿是否仍处于初始全选态：用户一旦取消勾选即离开该态。 */
const pristine = ref(props.selection === null);
const draft = ref<ReadonlySet<string>>(
  new Set(props.selection ?? props.options.map(option => option.key)),
);
/**
 * 分页追加选项时不重置草稿：初始全选态下新增项默认勾选（维持“未筛选
 * =全选”语义）；用户已编辑后新增项默认不勾选，由用户显式勾选，
 * 绝不覆盖已有编辑。
 */
watch(() => props.options, (options, previous) => {
  const known = new Set((previous ?? []).map(option => option.key));
  const added = options.filter(option => !known.has(option.key));
  if (added.length === 0) return;
  if (pristine.value) {
    const next = new Set(draft.value);
    for (const option of added) next.add(option.key);
    draft.value = next;
  }
});
const visibleOptions = computed(() => {
  const tokens = search.value.trim().toLocaleLowerCase().split(/\s+/u).filter(Boolean);
  if (tokens.length === 0) return props.options;
  return props.options.filter(option => {
    const label = option.label.toLocaleLowerCase();
    const key = option.key.slice(2).toLocaleLowerCase();
    return tokens.some(token => label.includes(token) || key.includes(token));
  });
});
const checkedCount = computed(() =>
  props.options.filter(option => draft.value.has(option.key)).length);
const allChecked = computed(() =>
  props.options.length > 0 && checkedCount.value === props.options.length);
const noneChecked = computed(() => checkedCount.value === 0);
const selectedKeys = computed(() => props.options.filter(option => draft.value.has(option.key)).map(option => option.key));
const selectionProblem = computed(() => props.validateSelection?.(selectedKeys.value) ?? null);

function toggle(key: string, checked: boolean): void {
  const next = new Set(draft.value);
  if (checked) next.add(key);
  else next.delete(key);
  pristine.value = false;
  draft.value = next;
}
function checkVisible(checked: boolean): void {
  const next = new Set(draft.value);
  for (const option of visibleOptions.value) {
    if (checked) next.add(option.key);
    else next.delete(option.key);
  }
  pristine.value = false;
  draft.value = next;
}
function apply(): void {
  if (noneChecked.value || selectionProblem.value) return;
  emit("apply", selectedKeys.value);
}
</script>

<template>
  <div
    class="vt-header-menu"
    role="dialog"
    :aria-label="`${fieldTitle} 值筛选`"
    data-testid="vt-header-menu"
    @click.stop
    @keydown.escape.stop="emit('close')"
  >
    <header>
      <strong>{{ fieldTitle }}</strong>
      <span v-if="status === 'ready'">{{ checkedCount }}/{{ options.length }}</span>
    </header>
    <template v-if="status === 'loading'">
      <div class="vt-header-menu__state" data-testid="vt-header-menu-loading">
        <NSpin :size="14" /> 正在加载选项…
      </div>
    </template>
    <template v-else-if="status === 'error'">
      <p class="vt-header-menu__state" data-testid="vt-header-menu-error">选项加载失败，请重试</p>
    </template>
    <template v-else-if="status === 'unavailable'">
      <p class="vt-header-menu__state" data-testid="vt-header-menu-unavailable">
        该字段暂不支持按值勾选{{ limitation ? `：${limitation}` : "" }}
      </p>
    </template>
    <template v-else>
      <NInput
        v-model:value="search"
        size="small"
        clearable
        placeholder="搜索已加载选项（空格分词）"
        data-testid="vt-header-menu-search"
        :aria-label="`${fieldTitle} 搜索选项`"
      />
      <div class="vt-header-menu__list" role="group" :aria-label="`${fieldTitle} 选项列表`">
        <NCheckbox
          v-for="option in visibleOptions"
          :key="option.key"
          :data-testid="`vt-header-menu-option-${option.key}`"
          class="vt-header-menu__option"
          :checked="draft.has(option.key)"
          @update:checked="checked => toggle(option.key, checked)"
        >
          <span class="vt-header-menu__option-label">{{ option.label }}</span>
          <small v-if="typeof option.count === 'number'">{{ option.count }}</small>
        </NCheckbox>
        <p v-if="visibleOptions.length === 0" class="vt-header-menu__empty">无匹配选项</p>
        <NButton
          v-if="hasMore"
          size="tiny"
          secondary
          block
          :loading="loadingMore === true"
          data-testid="vt-header-menu-more"
          @click="emit('loadMore')"
        >
          加载更多
        </NButton>
      </div>
      <p v-if="hasMore" class="vt-header-menu__hint">
        选项较多，仅显示已加载部分；搜索也仅覆盖已加载选项
      </p>
      <footer>
        <NButton size="tiny" quaternary data-testid="vt-header-menu-all" @click="checkVisible(true)">全选</NButton>
        <NButton size="tiny" quaternary data-testid="vt-header-menu-none" @click="checkVisible(false)">全不选</NButton>
        <span class="vt-header-menu__spacer" />
        <NButton
          size="tiny"
          quaternary
          data-testid="vt-header-menu-clear"
          :disabled="selection === null"
          @click="emit('clear')"
        >清除筛选</NButton>
        <NButton size="tiny" data-testid="vt-header-menu-cancel" @click="emit('close')">取消</NButton>
        <NButton
          size="small"
          type="primary"
          :disabled="noneChecked || !!selectionProblem"
          data-testid="vt-header-menu-apply"
          @click="apply"
        >应用</NButton>
      </footer>
      <p v-if="selectionProblem" role="alert" class="vt-header-menu__hint" data-testid="vt-header-menu-selection-error">{{ selectionProblem }}</p>
      <p v-if="noneChecked" class="vt-header-menu__hint" data-testid="vt-header-menu-empty-hint">
        至少勾选一个值；清除筛选请用“清除筛选”
      </p>
      <p v-else class="vt-header-menu__state">
        {{ allChecked ? (hasMore ? "已勾选全部已加载值，仍有更多选项" : "当前：未筛选（全部值）") : "当前：仅显示勾选值" }}
      </p>
    </template>
  </div>
</template>

<style scoped>
.vt-header-menu {
  display: grid;
  width: 252px;
  gap: 7px;
  padding: 10px;
  color: var(--vt-fg);
  border: 1px solid var(--vt-border);
  border-radius: var(--vt-radius-md);
  background: var(--vt-bg-elevated);
  box-shadow: var(--vt-shadow-2);
}
.vt-header-menu header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
}
.vt-header-menu header span {
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.vt-header-menu__state {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 8px 2px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.vt-header-menu__list {
  display: grid;
  max-height: 248px;
  overflow: auto;
  gap: 1px;
}
.vt-header-menu__option {
  display: flex;
  min-height: 28px;
  align-items: center;
  gap: 8px;
  padding: 0 4px;
  border-radius: var(--vt-radius-sm);
}
.vt-header-menu__option:hover { background: var(--vt-bg-subtle); }
.vt-header-menu__option :deep(.n-checkbox__label) {
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  gap: 8px;
}
.vt-header-menu__option-label {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vt-header-menu__option small {
  flex: 0 0 auto;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  font-variant-numeric: tabular-nums;
}
.vt-header-menu__empty {
  padding: 10px 4px;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
  text-align: center;
}
.vt-header-menu footer {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
  padding-top: 7px;
  border-top: 1px solid var(--vt-border);
}
.vt-header-menu__spacer { flex: 1 1 auto; }
.vt-header-menu__hint {
  margin: 0;
  color: var(--vt-fg-muted);
  font-size: var(--vt-font-caption);
}
.vt-header-menu__state { color: var(--vt-fg-accent-strong); }
</style>
