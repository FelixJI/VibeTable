<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NButton, NInput, NSelect, NTag } from "naive-ui";
import { ArrowRight, ChevronLeft, GitBranch, PencilLine } from "lucide-vue-next";
import type { FieldDraftV2, LogicalTypeV2 } from "@/contracts";

type LookupDefinition = NonNullable<FieldDraftV2["lookup"]>;

const props = defineProps<{ value: LookupDefinition }>();
const emit = defineEmits<{ commit: [value: LookupDefinition] }>();

const editing = ref(false);
const working = ref<LookupDefinition>({ ...props.value });
const aggregateOptions = [
  "none", "first", "distinct", "count", "sum", "avg", "min", "max",
].map(value => ({ label: value, value }));
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
  { label: "JSON", value: "json" },
];
const canCommit = computed(() =>
  working.value.relationFieldId.trim().length > 0
  && working.value.targetFieldId.trim().length > 0);

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
    relationFieldId: working.value.relationFieldId.trim(),
    targetFieldId: working.value.targetFieldId.trim(),
    aggregate: working.value.aggregate,
    resultType: working.value.resultType,
  });
  editing.value = false;
}
</script>

<template>
  <article class="specialized-editor" data-testid="lookup-field-editor">
    <template v-if="!editing">
      <div class="editor-summary">
        <span class="editor-mark"><GitBranch :size="18" /></span>
        <div>
          <span class="eyebrow">LOOKUP MODULE</span>
          <strong>引用路径</strong>
          <small class="path-summary">
            <code>{{ value.relationFieldId || "关系字段" }}</code>
            <ArrowRight :size="12" />
            <code>{{ value.targetFieldId || "目标字段" }}</code>
          </small>
        </div>
        <NTag size="small" :bordered="false">{{ value.aggregate }} · {{ value.resultType }}</NTag>
      </div>
      <NButton secondary data-testid="lookup-editor-entry" @click="beginEditing">
        <PencilLine :size="15" />
        进入查找引用编辑器
      </NButton>
    </template>

    <template v-else>
      <div class="editor-heading">
        <NButton quaternary size="small" data-testid="lookup-editor-cancel" @click="cancel">
          <ChevronLeft :size="15" />
          返回字段设置
        </NButton>
        <NTag size="small" :bordered="false">结构化路径</NTag>
      </div>
      <div class="path-grid">
        <label>
          <span>关系字段 ID</span>
          <NInput
            :value="working.relationFieldId"
            placeholder="fld_customer"
            data-testid="lookup-relation-field"
            @update:value="working = { ...working, relationFieldId: $event }"
          />
        </label>
        <span class="path-arrow"><ArrowRight :size="16" /></span>
        <label>
          <span>目标字段 ID</span>
          <NInput
            :value="working.targetFieldId"
            placeholder="fld_name"
            data-testid="lookup-target-field"
            @update:value="working = { ...working, targetFieldId: $event }"
          />
        </label>
      </div>
      <div class="two-column">
        <label>
          <span>聚合</span>
          <NSelect
            :value="working.aggregate"
            :options="aggregateOptions"
            data-testid="lookup-aggregate"
            @update:value="working = { ...working, aggregate: $event }"
          />
        </label>
        <label>
          <span>结果类型</span>
          <NSelect
            :value="working.resultType"
            :options="resultTypeOptions"
            data-testid="lookup-result-type"
            @update:value="working = {
              ...working,
              resultType: $event as LogicalTypeV2,
            }"
          />
        </label>
      </div>
      <div class="editor-actions">
        <small>确认后写回同一份 Schema v2 字段草稿。</small>
        <NButton
          type="primary"
          :disabled="!canCommit"
          data-testid="lookup-editor-commit"
          @click="commit"
        >
          确认引用路径
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
  border: 1px solid color-mix(in srgb, #0ea5e9 35%, var(--vt-border));
  border-radius: 12px;
  background:
    linear-gradient(135deg, color-mix(in srgb, #0ea5e9 7%, transparent), transparent 55%),
    var(--vt-bg-elevated);
}
.editor-summary,.editor-heading,.editor-actions,.path-summary {
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
.editor-mark {
  display: grid;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 10px;
  color: #0284c7;
  background: color-mix(in srgb, #0ea5e9 12%, var(--vt-bg-subtle));
}
.editor-heading,.editor-actions { justify-content: space-between; }
.path-grid {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: end;
  gap: 10px;
}
.path-arrow { padding-bottom: 10px; color: var(--vt-fg-muted); }
.two-column { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
label {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
  font-weight: 650;
}
.path-summary { gap: 5px; overflow: hidden; }
.path-summary code {
  overflow: hidden;
  max-width: 150px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.eyebrow {
  color: #0284c7;
  font-size: 9px;
  font-weight: 800;
  letter-spacing: .14em;
}
small { color: var(--vt-fg-muted); }
@media(max-width:720px) {
  .path-grid,.two-column { grid-template-columns: 1fr; }
  .path-arrow { display: none; }
}
</style>
