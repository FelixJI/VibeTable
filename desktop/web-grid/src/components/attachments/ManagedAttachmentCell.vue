<script setup lang="ts">
import type { AttachmentPolicy } from "@/contracts";
import { NPopconfirm } from "naive-ui";
import { t } from "@/i18n";

export interface ManagedAttachment {
  readonly storedName: string;
  readonly originalName: string;
  readonly mimeType: string;
  readonly size: number;
  readonly sha256: string;
  readonly thumbnailUrl?: string | null;
}

defineProps<{
  files: readonly ManagedAttachment[];
  policy: AttachmentPolicy;
  error?: string | null;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  download: [storedName: string];
  preview: [storedName: string];
  replace: [storedName: string];
  remove: [storedName: string];
  upload: [];
}>();

function size(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

</script>

<template>
  <div class="attachments">
    <div class="attachments__policy" data-testid="attachment-policy">
      <strong>{{ t('attachment.managed') }}</strong>
      <span>{{ t('attachment.policy', {
        current: files.length,
        max: policy.maxFiles,
        size: size(policy.maxBytesPerFile),
      }) }}</span>
      <span v-if="policy.protected" class="protected">{{ t('attachment.protected') }}</span>
    </div>
    <ul v-if="files.length">
      <li v-for="(file, index) in files" :key="file.storedName">
        <img v-if="file.thumbnailUrl" :src="file.thumbnailUrl" alt="" />
        <span class="file-copy">
          <strong>{{ file.originalName }}</strong>
          <small>{{ size(file.size) }} · {{ file.mimeType }}</small>
        </span>
        <button type="button" :data-testid="`attachment-preview-${index}`" @click="emit('preview', file.storedName)">{{ t('attachment.preview') }}</button>
        <button type="button" :data-testid="`attachment-download-${index}`" @click="emit('download', file.storedName)">{{ t('attachment.download') }}</button>
        <button
          v-if="!disabled"
          type="button"
          class="replace"
          :data-testid="`attachment-replace-${index}`"
          @click="emit('replace', file.storedName)"
        >
          {{ t('attachment.replace') }}
        </button>
        <NPopconfirm
          :disabled="disabled"
          :positive-text="t('attachment.remove.confirm')"
          :negative-text="t('attachment.remove.cancel')"
          :positive-button-props="{ type: 'error' }"
          @positive-click="emit('remove', file.storedName)"
        >
          <template #trigger>
            <button
              type="button"
              class="remove"
              :disabled="disabled"
              :data-testid="`attachment-remove-${index}`"
            >
              {{ t('attachment.remove') }}
            </button>
          </template>
          {{ t('attachment.remove.message', { name: file.originalName }) }}
        </NPopconfirm>
      </li>
    </ul>
    <button
      v-if="files.length < policy.maxFiles && !disabled"
      type="button"
      class="upload"
      data-testid="attachment-upload"
      @click="emit('upload')"
    >
      {{ t('attachment.upload') }}
    </button>
    <p v-if="error" data-testid="attachment-error" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.attachments { display: grid; gap: 7px; min-width: 260px; }
.attachments__policy { display: flex; align-items: center; gap: 8px; color: var(--vt-fg-muted); font-size: 11px; }
.attachments__policy strong { color: var(--vt-fg); }
.protected { padding: 2px 5px; border: 1px solid var(--vt-border); border-radius: 999px; }
ul { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
li { display: grid; grid-template-columns: 34px minmax(0, 1fr) repeat(4, auto); align-items: center; gap: 6px; padding: 6px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); background: var(--vt-bg-subtle); }
img { width: 34px; height: 34px; object-fit: cover; }
.file-copy { display: flex; min-width: 0; flex-direction: column; }
.file-copy strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-copy small { color: var(--vt-fg-muted); }
button, .upload, .replace { border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); color: var(--vt-fg); background: var(--vt-bg); font: inherit; cursor: pointer; }
button { padding: 4px 6px; }
.upload, .replace { justify-self: start; padding: 5px 8px; }
.remove { color: var(--vt-color-danger); border-color: color-mix(in srgb, var(--vt-color-danger) 34%, var(--vt-border)); }
.remove:hover:not(:disabled) { background: color-mix(in srgb, var(--vt-color-danger) 8%, var(--vt-bg)); }
button:disabled { cursor: not-allowed; opacity: .48; }
p { margin: 0; color: var(--vt-color-danger); font-size: 11px; }
</style>
