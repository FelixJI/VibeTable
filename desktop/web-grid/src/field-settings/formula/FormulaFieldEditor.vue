<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NInput, NSelect, NTag } from "naive-ui";
import { Braces, ChevronLeft, PencilLine } from "lucide-vue-next";
import type { FieldDraftV2, LogicalTypeV2 } from "@/contracts";

type FormulaDefinition = NonNullable<FieldDraftV2["formula"]>;

const props = defineProps<{ value: FormulaDefinition }>();
const emit = defineEmits<{ commit: [value: FormulaDefinition] }>();

const editing = ref(false);
const working = ref<FormulaDefinition>({ ...props.value });
const resultTypeOptions: {
  label: string;
  value: LogicalTypeV2;
}[] = [
  { label: "单行文本", value: "text" },
  { label: "数字", value: "number" },
  { label: "勾选", value: "bool" },
  { label: "日期", value: "date" },
  { label: "日期时间", value: "dateTime" },
  { label: "时间", value: "time" },
  { label: "邮箱", value: "email" },
  { label: "网址", value: "url" },
  { label: "JSON", value: "json" },
];
const canCommit = computed(() => working.value.source.trim().length > 0);

watch(
  () => props.value,
  (value) => {
    if (!editing.value) working.value = { ...value };
  },
  { deep: true },
);

function beginEditing(): void {
  working.value = { ...props.value };
  editing.value = true;
}

function cancel(): void {
  working.value = { ...props.value };
  editing.value = false;
}

function commit(): void {
  if (!canCommit.value) return;
  emit("commit", {
    language: "cel-v1",
    source: working.value.source.trim(),
    resultType: working.value.resultType,
  });
  editing.value = false;
}
</script>

<template>
  <article class="specialized-editor" data-testid="formula-field-editor">
    <template v-if="!editing">
      <div class="editor-summary">
        <span class="editor-mark"><Braces :size="18" /></span>
        <div>
          <span class="eyebrow">FORMULA MODULE</span>
          <strong>公式定义</strong>
          <small>{{ value.source || "尚未配置 CEL 表达式" }}</small>
        </div>
        <NTag size="small" :bordered="false">{{ value.resultType }}</NTag>
      </div>
      <NButton secondary data-testid="formula-editor-entry" @click="beginEditing">
        <PencilLine :size="15" />
        进入公式编辑器
      </NButton>
    </template>

    <template v-else>
      <div class="editor-heading">
        <NButton quaternary size="small" data-testid="formula-editor-cancel" @click="cancel">
          <ChevronLeft :size="15" />
          返回字段设置
        </NButton>
        <NTag size="small" :bordered="false">CEL v1</NTag>
      </div>
      <label>
        <span>CEL 公式</span>
        <NInput
          :value="working.source"
          type="textarea"
          :autosize="{ minRows: 5, maxRows: 12 }"
          placeholder="例如：record.price * record.quantity"
          data-testid="formula-source"
          @update:value="working = { ...working, source: $event }"
        />
      </label>
      <label>
        <span>结果类型</span>
        <NSelect
          :value="working.resultType"
          :options="resultTypeOptions"
          data-testid="formula-result-type"
          @update:value="working = {
            ...working,
            resultType: $event as LogicalTypeV2,
          }"
        />
      </label>
      <div class="editor-actions">
        <small>确认后写回同一份 Schema v2 字段草稿。</small>
        <NButton
          type="primary"
          :disabled="!canCommit"
          data-testid="formula-editor-commit"
          @click="commit"
        >
          确认公式
        </NButton>
      </div>
    </template>
  </article>
</template>

<style scoped>
.specialized-editor {
  display: grid;
  gap: 14px;
  padding: 14px;
  border: 1px solid color-mix(in srgb, var(--vt-fg-accent) 35%, var(--vt-border));
  border-radius: 12px;
  background:
    linear-gradient(135deg, color-mix(in srgb, var(--vt-fg-accent) 7%, transparent), transparent 55%),
    var(--vt-bg-elevated);
}
.editor-summary,.editor-heading,.editor-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
.editor-summary>div {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}
.editor-summary small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.editor-mark {
  display: grid;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 10px;
  color: var(--vt-fg-accent);
  background: color-mix(in srgb, var(--vt-fg-accent) 12%, var(--vt-bg-subtle));
}
.editor-heading,.editor-actions { justify-content: space-between; }
label {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
  font-weight: 650;
}
.eyebrow {
  color: var(--vt-fg-accent);
  font-size: 9px;
  font-weight: 800;
  letter-spacing: .14em;
}
small { color: var(--vt-fg-muted); }
</style>
