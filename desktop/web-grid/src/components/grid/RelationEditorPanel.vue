<script setup lang="ts">
import { computed } from "vue";
import { NButton, NEmpty, NInput, NModal, NSelect, NSpin } from "naive-ui";
import { Check, Link2, Search, X } from "lucide-vue-next";
import type { NormalizedRelationDescriptor, RelationTargetRef } from "@/contracts";
import { targetKey } from "@/stores/relationLookupStore";
import { t } from "@/i18n";

const props = defineProps<{
  show: boolean;
  descriptor: NormalizedRelationDescriptor | null;
  selected: readonly RelationTargetRef[];
  candidates: readonly RelationTargetRef[];
  loading?: boolean;
  applying?: boolean;
  error?: string | null;
  query?: string;
  m2aCollection?: string | null;
}>();

const emit = defineEmits<{
  close: [];
  search: [query: string, collection?: string | null];
  select: [target: RelationTargetRef];
  clear: [];
  patchJunction: [target: RelationTargetRef, field: string, value: string];
  apply: [];
  collectionChange: [collection: string];
}>();

const multi = computed(() => props.descriptor?.kind !== "m2o");
const selectedKeys = computed(() => new Set(props.selected.map(targetKey)));
const title = computed(() => {
  const descriptor = props.descriptor;
  if (!descriptor) return t("relationEditor.title");
  const labels = {
    m2o: t("relationEditor.kind.m2o"),
    o2m: t("relationEditor.kind.o2m"),
    m2m: t("relationEditor.kind.m2m"),
    m2a: t("relationEditor.kind.m2a"),
  };
  return `${labels[descriptor.kind]} · ${descriptor.fieldRef}`;
});
const collectionOptions = computed(() => (props.descriptor?.allowedCollections ?? []).map((value) => ({
  label: value,
  value,
})));

function onQuery(value: string): void {
  emit("search", value, props.m2aCollection);
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="title"
    class="relation-editor"
    :mask-closable="false"
    :auto-focus="true"
    :trap-focus="true"
    :close-on-esc="true"
    @update:show="value => { if (!value) emit('close') }"
  >
    <div v-if="descriptor" class="relation-editor__body">
      <div class="relation-editor__meta">
        <span class="relation-editor__kind">{{ descriptor.preset }}</span>
        <span>{{ multi ? t("relationEditor.applyTogether") : t("relationEditor.saveImmediately") }}</span>
        <span v-if="descriptor.selfRelation">{{ t("relationEditor.selfRelation") }}</span>
      </div>

      <NSelect
        v-if="descriptor.kind === 'm2a'"
        :value="m2aCollection"
        :options="collectionOptions"
        :placeholder="t('relationEditor.collectionPlaceholder')"
        :aria-label="t('relationEditor.collectionLabel')"
        @update:value="(value) => emit('collectionChange', value)"
      />

      <NInput
        :value="query"
        clearable
        :placeholder="t('relationEditor.searchPlaceholder')"
        :input-props="{ 'aria-label': t('relationEditor.searchLabel') }"
        @update:value="onQuery"
      >
        <template #prefix><Search :size="14" /></template>
      </NInput>

      <div v-if="selected.length" class="relation-editor__selected">
        <div v-for="target in selected" :key="targetKey(target)" class="relation-editor__selected-row">
          <span class="relation-editor__token">
            <b v-if="descriptor.kind === 'm2a'">{{ target.collection }}</b>
            {{ target.label }}
          </span>
          <div v-if="descriptor.junction?.contextFields.length" class="relation-editor__junction">
            <NInput
              v-for="field in descriptor.junction.contextFields"
              :key="field"
              size="small"
              :value="String(target.junctionValues[field] ?? '')"
              :placeholder="field"
              @update:value="(value) => emit('patchJunction', target, field, value)"
            />
          </div>
          <NButton
            quaternary
            size="tiny"
            :aria-label="t('relationEditor.remove')"
            @click="multi ? emit('select', target) : emit('clear')"
          >
            <X :size="13" />
          </NButton>
        </div>
      </div>

      <NSpin :show="loading">
        <div class="relation-editor__results">
          <button
            v-for="target in candidates"
            :key="targetKey(target)"
            type="button"
            class="relation-editor__candidate"
            :class="{ 'relation-editor__candidate--selected': selectedKeys.has(targetKey(target)) }"
            :aria-pressed="selectedKeys.has(targetKey(target))"
            @click="emit('select', target)"
          >
            <Link2 :size="14" />
            <span>
              <b v-if="descriptor.kind === 'm2a'">{{ target.collection }}</b>
              {{ target.label }}
            </span>
            <Check v-if="selectedKeys.has(targetKey(target))" :size="14" />
          </button>
          <NEmpty
            v-if="!loading && candidates.length === 0"
            :description="t('relationEditor.empty')"
            size="small"
          />
        </div>
      </NSpin>

      <p v-if="error" class="relation-editor__error" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <div class="relation-editor__footer">
        <NButton v-if="descriptor?.nullable && !multi" quaternary @click="emit('clear')">
          {{ t("relationEditor.clear") }}
        </NButton>
        <span class="relation-editor__spacer"></span>
        <NButton @click="emit('close')">{{ t("relationEditor.cancel") }}</NButton>
        <NButton v-if="multi" type="primary" :loading="applying" @click="emit('apply')">
          {{ t("relationEditor.apply", { count: selected.length }) }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style>
.relation-editor { width: min(620px, calc(100vw - 32px)); }
.relation-editor__body { display: grid; gap: var(--vt-space-3); }
.relation-editor__meta {
  display: flex; gap: var(--vt-space-2); align-items: center;
  color: var(--vt-fg-muted); font-size: var(--vt-font-caption);
}
.relation-editor__kind {
  padding: 1px 7px; border-radius: 999px;
  color: var(--vt-fg-accent-strong); background: var(--vt-color-primary-50);
  font-weight: 700; letter-spacing: .04em; text-transform: uppercase;
}
.relation-editor__selected { display: grid; gap: 6px; }
.relation-editor__selected-row {
  display: grid; grid-template-columns: minmax(120px, 1fr) minmax(0, 1.6fr) auto;
  gap: var(--vt-space-2); align-items: center;
  padding: 6px 8px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-md);
  background: var(--vt-bg-subtle);
}
.relation-editor__token { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.relation-editor__token b, .relation-editor__candidate b {
  margin-right: 6px; color: var(--vt-fg-accent-strong); font-size: 10px; text-transform: uppercase;
}
.relation-editor__junction { display: flex; gap: 5px; min-width: 0; }
.relation-editor__results {
  display: grid; gap: 3px; min-height: 100px; max-height: 280px; padding: 2px; overflow: auto;
}
.relation-editor__candidate {
  display: grid; grid-template-columns: 18px 1fr 18px; align-items: center; gap: 7px;
  width: 100%; min-height: 34px; padding: 5px 9px;
  border: 1px solid transparent; border-radius: var(--vt-radius-sm);
  color: var(--vt-fg); background: transparent; text-align: left; cursor: pointer;
  transition: background var(--vt-duration-fast) var(--vt-ease), border-color var(--vt-duration-fast) var(--vt-ease);
}
.relation-editor__candidate:hover { background: var(--vt-bg-subtle); }
.relation-editor__candidate--selected {
  border-color: color-mix(in srgb, var(--vt-color-primary-500) 35%, var(--vt-border));
  background: color-mix(in srgb, var(--vt-color-primary-500) 7%, var(--vt-bg));
}
.relation-editor__error { margin: 0; color: var(--vt-color-danger); font-size: var(--vt-font-caption); }
.relation-editor__footer { display: flex; align-items: center; gap: var(--vt-space-2); }
.relation-editor__spacer { flex: 1; }
</style>
