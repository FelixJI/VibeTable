<script setup lang="ts">
import { ref, watch } from "vue";

const props = withDefaults(defineProps<{
  modelValue: unknown;
  readonly?: boolean;
  serverError?: string | null;
  errorPath?: string | null;
}>(), {
  readonly: false,
  serverError: null,
  errorPath: null,
});

const emit = defineEmits<{
  "update:modelValue": [value: unknown];
  validityChanged: [valid: boolean];
}>();

const text = ref(format(props.modelValue));
const parseError = ref<string | null>(null);

watch(() => props.modelValue, (value) => {
  if (!parseError.value) text.value = format(value);
});

function update(event: Event): void {
  text.value = (event.target as HTMLTextAreaElement).value;
  try {
    const parsed = JSON.parse(text.value) as unknown;
    parseError.value = null;
    emit("validityChanged", true);
    emit("update:modelValue", parsed);
  } catch {
    parseError.value = "请输入有效的 JSON。";
    emit("validityChanged", false);
  }
}

function format(value: unknown): string {
  return JSON.stringify(value, null, 2) ?? "null";
}
</script>

<template>
  <div class="json-editor" :class="{ invalid: parseError || serverError }">
    <div class="json-editor__bar">
      <span>JSON</span>
      <small>结构化值 · 保存时保持类型</small>
    </div>
    <textarea
      :value="text"
      :readonly="readonly"
      spellcheck="false"
      aria-label="JSON editor"
      data-testid="json-editor-input"
      @input="update"
    />
    <p v-if="parseError" data-testid="json-editor-error" role="alert">
      {{ parseError }}
    </p>
    <p v-if="serverError" data-testid="json-editor-server-error" role="alert">
      <code v-if="errorPath">{{ errorPath }}</code>
      {{ serverError }}
    </p>
  </div>
</template>

<style scoped>
.json-editor { overflow: hidden; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md); background: var(--vt-bg-sunken); }
.json-editor.invalid { border-color: var(--vt-color-danger); }
.json-editor__bar { display: flex; justify-content: space-between; padding: 6px 9px; border-bottom: 1px solid var(--vt-border); color: var(--vt-fg-muted); font-size: 10px; letter-spacing: .05em; text-transform: uppercase; }
textarea { box-sizing: border-box; width: 100%; min-height: 132px; padding: 10px; border: 0; outline: 0; color: var(--vt-fg); background: transparent; font: 12px/1.55 Consolas, "SFMono-Regular", monospace; resize: vertical; }
p { margin: 0; padding: 6px 9px; border-top: 1px solid color-mix(in srgb, var(--vt-color-danger) 40%, var(--vt-border)); color: var(--vt-color-danger); font-size: 11px; }
code { margin-right: 6px; }
</style>
