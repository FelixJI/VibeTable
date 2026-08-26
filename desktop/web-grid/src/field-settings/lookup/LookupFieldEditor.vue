<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NSelect, NTag } from "naive-ui";
import type { SelectOption } from "naive-ui";
import { ArrowRight, ChevronLeft, GitBranch, Minus, PencilLine, Plus } from "@lucide/vue";
import type { FieldDraftV2 } from "@/contracts";

type LookupDefinition = NonNullable<FieldDraftV2["lookup"]>;

interface LookupOption extends SelectOption {
  readonly label: string;
  readonly value: string;
  readonly many?: boolean;
}

const props = defineProps<{
  value: LookupDefinition;
  relationOptions: LookupOption[][];
  targetFieldOptions: LookupOption[];
  maxDepth: number;
  loading?: boolean;
  error?: string | null;
}>();
const emit = defineEmits<{
  commit: [value: LookupDefinition];
  pathChange: [path: LookupDefinition["path"]];
}>();

const editing = ref(false);
const working = ref<LookupDefinition>(cloneLookup(props.value));
const canCommit = computed(() =>
  working.value.path.length > 0
  && working.value.path.length <= props.maxDepth
  && working.value.path.every(step => step.relationFieldId.length > 0)
  && working.value.targetFieldId.trim().length > 0);
const producesList = computed(() => working.value.path.some((step, index) =>
  props.relationOptions[index]?.find(option => option.value === step.relationFieldId)?.many));
const targetFieldLabel = computed(() => props.targetFieldOptions.find(
  option => option.value === props.value.targetFieldId,
)?.label ?? "目标字段");

watch(
  () => props.value,
  (value) => {
    if (!editing.value) working.value = cloneLookup(value);
  },
  { deep: true },
);

function beginEditing(): void {
  working.value = cloneLookup(props.value);
  editing.value = true;
}

function cancel(): void {
  working.value = cloneLookup(props.value);
  editing.value = false;
}

function commit(): void {
  if (!canCommit.value) return;
  emit("commit", {
    path: working.value.path.map(step => ({ relationFieldId: step.relationFieldId })),
    targetFieldId: working.value.targetFieldId.trim(),
  });
  editing.value = false;
}

function selectStep(index: number, relationFieldId: string): void {
  const path = working.value.path.slice(0, index + 1).map(step => ({ ...step }));
  path[index] = { relationFieldId };
  working.value = { path, targetFieldId: "" };
  emit("pathChange", path);
}

function addStep(): void {
  if (working.value.path.length >= props.maxDepth) return;
  const path = [...working.value.path, { relationFieldId: "" }];
  working.value = { path, targetFieldId: "" };
}

function removeStep(): void {
  if (working.value.path.length <= 1) return;
  const path = working.value.path.slice(0, -1);
  working.value = { path, targetFieldId: "" };
  emit("pathChange", path);
}

function cloneLookup(value: LookupDefinition): LookupDefinition {
  return {
    path: value.path.map(step => ({ ...step })),
    targetFieldId: value.targetFieldId,
  };
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
            <template v-for="(step, index) in value.path" :key="index">
              <code>{{ relationOptions[index]?.find(option => option.value === step.relationFieldId)?.label || "关系字段" }}</code>
              <ArrowRight :size="12" />
            </template>
            <code>{{ targetFieldLabel }}</code>
          </small>
        </div>
        <NTag size="small" :bordered="false">自动类型</NTag>
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
      <NAlert v-if="error" type="error" :show-icon="false">{{ error }}</NAlert>
      <div class="lookup-path">
        <label v-for="(step, index) in working.path" :key="index">
          <span>第 {{ index + 1 }} 跳关系</span>
          <NSelect
            :value="step.relationFieldId || null"
            :options="relationOptions[index] ?? []"
            :loading="loading"
            :placeholder="`选择第 ${index + 1} 跳关系`"
            :data-testid="`lookup-relation-step-${index}`"
            @update:value="selectStep(index, String($event))"
          />
        </label>
        <label>
          <span>引用目标字段</span>
          <NSelect
            :value="working.targetFieldId || null"
            :options="targetFieldOptions"
            :loading="loading"
            placeholder="选择目标字段"
            data-testid="lookup-target-field"
            @update:value="working = { ...working, targetFieldId: String($event) }"
          />
        </label>
      </div>
      <div class="path-actions">
        <NButton size="small" secondary :disabled="working.path.length >= maxDepth" @click="addStep">
          <Plus :size="14" />继续关联
        </NButton>
        <NButton size="small" quaternary :disabled="working.path.length <= 1" @click="removeStep">
          <Minus :size="14" />移除末跳
        </NButton>
        <NTag size="small" :bordered="false">
          {{ producesList ? "多值 · 类型化列表" : "单值 · 自动类型" }}
        </NTag>
      </div>
      <div class="editor-actions">
        <small>最多 {{ maxDepth }} 跳；类型和单值/列表形状由路径自动推导，不在 Lookup 内聚合。</small>
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
.editor-summary,.editor-heading,.editor-actions,.path-summary,.path-actions {
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
.lookup-path { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.path-actions { flex-wrap: wrap; }
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
  .lookup-path { grid-template-columns: 1fr; }
}
</style>
