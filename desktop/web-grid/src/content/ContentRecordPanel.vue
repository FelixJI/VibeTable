<script setup lang="ts">
import { computed, ref, shallowRef, watch } from "vue";
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NInput,
  NSelect,
  NSpin,
  NTag,
} from "naive-ui";
import type { ColumnSchema, FieldDefinitionV2, SchemaSnapshotV2 } from "@/contracts";
import type {
  ContentProfile,
  ContentProfileSnapshot,
  RecordDocumentLinkSnapshot,
} from "@/contracts/generated/workbench";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useHostBridge } from "@/services/bridgeContext";
import { useContentModelService } from "./contentModelService";

const props = defineProps<{
  show: boolean;
  tableId: string;
  row: Readonly<Record<string, unknown>> | null;
  columns: readonly ColumnSchema[];
  documents: readonly DocumentEntry[];
  documentLabels: Readonly<Record<string, string>>;
}>();
const emit = defineEmits<{
  close: [];
  saved: [];
}>();
const bridge = useHostBridge();
const service = useContentModelService();
const loading = ref(false);
const saving = ref(false);
const fieldSelectOpen = ref(false);
const error = ref<string | null>(null);
const schema = shallowRef<SchemaSnapshotV2 | null>(null);
const snapshot = ref<ContentProfileSnapshot | null>(null);
const links = ref<readonly RecordDocumentLinkSnapshot[]>([]);
const editing = ref(false);
const titleDraft = ref("");
const summaryDraft = ref("");
const bodyDraft = ref("");
const committedRow = ref<Readonly<Record<string, unknown>> | null>(null);
const committedValues = ref<Readonly<Record<string, unknown>> | null>(null);
const titleField = ref<string | null>(null);
const bodyField = ref<string | null>(null);
const summaryField = ref<string | null>(null);
const searchableFields = ref<string[]>([]);
const selectedDocument = ref<string | null>(null);
const linkRole = ref<"source" | "reference" | "supporting" | "output">("reference");

const fields = computed<readonly FieldDefinitionV2[]>(() => schema.value?.fields ?? []);
const fieldById = computed(() => {
  const result = new Map<string, FieldDefinitionV2>();
  for (const field of fields.value) result.set(field.identity.fieldId, field);
  return result;
});
const textFields = computed<FieldDefinitionV2[]>(() => {
  const result: FieldDefinitionV2[] = [];
  for (const field of fields.value) {
    if (["text", "editor", "email", "url", "select"].includes(field.logicalType)) {
      result.push(field);
    }
  }
  return result;
});
const bodyFields = computed<FieldDefinitionV2[]>(() => {
  const result: FieldDefinitionV2[] = [];
  for (const field of fields.value) if (field.logicalType === "editor") result.push(field);
  return result;
});
const textOptions = computed(() => textFields.value.map((field) => ({
  label: field.displayName,
  value: field.identity.fieldId,
})));
const bodyOptions = computed(() => bodyFields.value.map((field) => ({
  label: field.displayName,
  value: field.identity.fieldId,
})));
const documentOptions = computed(() => props.documents
  .filter((document) => document.status === "active")
  .map((document) => ({ label: `${document.displayName} · ${document.relativePath}`, value: document.documentId })));
const profile = computed(() => snapshot.value?.profile ?? null);
const visibleRow = computed(() => committedRow.value ?? props.row);
const recordId = computed(() => {
  const value = visibleRow.value?.rowKey ?? visibleRow.value?.id;
  return typeof value === "string" || typeof value === "number" ? String(value) : null;
});
const activeTitle = computed(() => fieldValue(profile.value?.titleFieldId));
const activeSummary = computed(() => fieldValue(profile.value?.summaryFieldId ?? null));
const activeBody = computed(() => fieldValue(profile.value?.bodyFieldId));
const linkedDocuments = computed(() => links.value.map((item) => {
  const document = props.documents.find(
    (candidate) => candidate.documentId === item.link.documentId,
  ) ?? null;
  return {
    ...item,
    displayName: document?.displayName
      ?? props.documentLabels[item.link.documentId]
      ?? item.link.documentId,
    active: document?.status === "active",
  };
}));

function fieldValue(fieldId: string | null | undefined): string {
  if (!fieldId || !visibleRow.value) return "";
  const field = fieldById.value.get(fieldId);
  const value = field ? visibleRow.value[field.identity.physicalName] : undefined;
  return value === null || value === undefined ? "" : String(value);
}

function resetDrafts(): void {
  titleDraft.value = activeTitle.value;
  summaryDraft.value = activeSummary.value;
  bodyDraft.value = activeBody.value;
}

async function load(): Promise<void> {
  if (!props.show || !props.tableId) return;
  loading.value = true;
  error.value = null;
  try {
    const [definition, loaded] = await Promise.all([
      bridge.request("schema.getTable", { tableId: props.tableId }),
      service.loadProfile(props.tableId),
    ]);
    schema.value = definition;
    snapshot.value = loaded;
    if (loaded) {
      titleField.value = loaded.profile.titleFieldId;
      bodyField.value = loaded.profile.bodyFieldId;
      summaryField.value = loaded.profile.summaryFieldId;
      searchableFields.value = [...loaded.profile.searchableFieldIds];
    } else {
      titleField.value = null;
      bodyField.value = null;
      summaryField.value = null;
      searchableFields.value = [];
    }
    await loadLinks();
    resetDrafts();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
  }
}

async function loadLinks(): Promise<void> {
  if (!recordId.value) {
    links.value = [];
    return;
  }
  links.value = (await service.listLinks(props.tableId, recordId.value)).items;
}

async function saveProfile(): Promise<void> {
  if (!titleField.value || !bodyField.value || searchableFields.value.length === 0) return;
  saving.value = true;
  error.value = null;
  const next: ContentProfile = {
    contractVersion: "1.0",
    tableId: props.tableId,
    titleFieldId: titleField.value,
    bodyFieldId: bodyField.value,
    summaryFieldId: summaryField.value,
    searchableFieldIds: searchableFields.value,
  };
  try {
    snapshot.value = await service.commitProfile(next, snapshot.value?.revision ?? null);
    resetDrafts();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    saving.value = false;
  }
}

async function saveRecord(): Promise<void> {
  const current = profile.value;
  const definition = schema.value;
  if (!current || !definition || !props.row || !recordId.value) return;
  const values: Record<string, unknown> = {};
  for (const [fieldId, next] of [
    [current.titleFieldId, titleDraft.value],
    [current.bodyFieldId, bodyDraft.value],
    [current.summaryFieldId, summaryDraft.value],
  ] as const) {
    if (!fieldId) continue;
    const field = fieldById.value.get(fieldId);
    if (!field) continue;
    const previous = props.row[field.identity.physicalName];
    if (String(previous ?? "") !== next) values[field.identity.physicalName] = next;
  }
  if (Object.keys(values).length === 0) {
    editing.value = false;
    return;
  }
  const digest = props.row.__vibetableDigest;
  saving.value = true;
  error.value = null;
  try {
    const receipt = await service.commitRecord({
      tableId: props.tableId,
      schemaRevision: definition.schemaRevision,
      recordId: recordId.value,
      values,
      expectedDigest: typeof digest === "string" ? digest : null,
    });
    const affected = receipt.affectedRows.find((row) => row.recordId === recordId.value);
    committedValues.value = values;
    committedRow.value = {
      ...props.row,
      ...values,
      ...(affected ? { __vibetableDigest: affected.digest } : {}),
    };
    editing.value = false;
    emit("saved");
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    saving.value = false;
  }
}

async function createLink(): Promise<void> {
  if (!recordId.value || !selectedDocument.value) return;
  saving.value = true;
  try {
    await service.commitLink({
      contractVersion: "1.0",
      linkId: crypto.randomUUID(),
      tableId: props.tableId,
      recordId: recordId.value,
      documentId: selectedDocument.value,
      role: linkRole.value,
      order: links.value.length,
    });
    selectedDocument.value = null;
    await loadLinks();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    saving.value = false;
  }
}

async function repairLink(item: RecordDocumentLinkSnapshot): Promise<void> {
  if (!selectedDocument.value) return;
  await service.repairLink(
    item.link.linkId,
    selectedDocument.value,
    item.revision,
  );
  selectedDocument.value = null;
  await loadLinks();
}

async function deleteLink(item: RecordDocumentLinkSnapshot): Promise<void> {
  await service.deleteLink(item.link.linkId, item.revision);
  await loadLinks();
}

watch(() => [props.show, props.tableId, recordId.value] as const, ([show]) => {
  if (!show) fieldSelectOpen.value = false;
  void load();
}, { immediate: true });
watch(() => props.row, (row) => {
  if (!row) return;
  if (committedRow.value && committedValues.value) {
    const committedId = committedRow.value.rowKey ?? committedRow.value.id;
    const incomingId = row.rowKey ?? row.id;
    const reflectsCommit = committedId === incomingId
      && Object.entries(committedValues.value).every(([field, value]) => row[field] === value);
    if (committedId !== incomingId || reflectsCommit) {
      committedRow.value = null;
      committedValues.value = null;
    }
  }
  resetDrafts();
}, { deep: true });
watch(() => [props.show, props.tableId] as const, ([show], [previousShow, previousTable]) => {
  if (!show || (!previousShow && show) || props.tableId !== previousTable) {
    committedRow.value = null;
    committedValues.value = null;
  }
});
</script>

<template>
  <NDrawer
    :show="show"
    :width="760"
    :close-on-esc="!fieldSelectOpen"
    placement="right"
    @update:show="value => { if (!value) emit('close'); }"
  >
    <NDrawerContent closable :title="profile ? '内容记录' : '配置内容记录'">
      <NSpin v-if="loading" class="content-loading" />
      <NAlert v-if="error" type="error" class="content-alert">{{ error }}</NAlert>
      <section v-if="!loading && !profile" class="profile-config" data-testid="content-profile-config">
        <h3>把现有字段组织成内容页</h3>
        <p>配置只引用 SchemaCore 字段；正文仍保存在当前 PocketBase 记录中。</p>
        <label>
          标题字段
          <NSelect
            v-model:value="titleField"
            data-testid="content-profile-title"
            :options="textOptions"
            @update:show="fieldSelectOpen = $event"
          />
        </label>
        <label>
          正文字段
          <NSelect
            v-model:value="bodyField"
            data-testid="content-profile-body"
            :options="bodyOptions"
            @update:show="fieldSelectOpen = $event"
          />
        </label>
        <label>
          摘要字段
          <NSelect
            v-model:value="summaryField"
            data-testid="content-profile-summary"
            clearable
            :options="textOptions"
            @update:show="fieldSelectOpen = $event"
          />
        </label>
        <label>
          进入搜索的字段
          <NSelect
            v-model:value="searchableFields"
            data-testid="content-profile-searchable"
            multiple
            :options="textOptions"
            @update:show="fieldSelectOpen = $event"
          />
        </label>
        <NButton type="primary" data-testid="content-profile-save" :loading="saving" :disabled="!titleField || !bodyField || !searchableFields.length" @click="saveProfile">保存内容配置</NButton>
      </section>
      <section v-else-if="profile" class="content-layout" data-testid="content-record-panel">
        <aside class="field-navigation">
          <strong>字段导航</strong>
          <a v-for="fieldId in profile.searchableFieldIds" :key="fieldId" :href="`#content-${fieldId}`">
            {{ fieldById.get(fieldId)?.displayName ?? fieldId }}
          </a>
        </aside>
        <main class="content-main">
          <NAlert v-if="!visibleRow" type="info">请先在表格中选择一条记录。</NAlert>
          <template v-else>
            <div class="content-actions">
              <NButton size="small" data-testid="content-edit" @click="editing = !editing; resetDrafts()">{{ editing ? '取消编辑' : '编辑内容' }}</NButton>
              <NButton v-if="editing" size="small" type="primary" data-testid="content-record-save" :loading="saving" @click="saveRecord">保存记录</NButton>
            </div>
            <NInput v-if="editing" v-model:value="titleDraft" size="large" />
            <h1 v-else>{{ activeTitle || `记录 ${recordId}` }}</h1>
            <NInput v-if="editing && profile.summaryFieldId" v-model:value="summaryDraft" type="textarea" :autosize="{ minRows: 2, maxRows: 5 }" />
            <p v-else-if="activeSummary" class="content-summary">{{ activeSummary }}</p>
            <NInput v-if="editing" v-model:value="bodyDraft" type="textarea" :autosize="{ minRows: 14, maxRows: 32 }" />
            <article v-else class="content-body">{{ activeBody }}</article>

            <section class="record-links">
              <header><strong>关联文档</strong><span>删除关联不会删除记录或文件</span></header>
              <div class="link-create">
                <NSelect v-model:value="selectedDocument" data-testid="content-link-document" filterable clearable :options="documentOptions" placeholder="选择工作区文件" />
                <NSelect v-model:value="linkRole" data-testid="content-link-role" :options="[
                  { label: '引用', value: 'reference' }, { label: '来源', value: 'source' },
                  { label: '支持材料', value: 'supporting' }, { label: '输出', value: 'output' },
                ]" />
                <NButton data-testid="content-link-create" :disabled="!selectedDocument" @click="createLink">建立关联</NButton>
              </div>
              <article v-for="item in linkedDocuments" :key="item.link.linkId" class="link-card" :data-testid="`content-link-${item.link.linkId}`">
                <div>
                  <strong>{{ item.displayName }}</strong>
                  <small>{{ item.link.role }} · #{{ item.link.order }}</small>
                </div>
                <NTag :type="item.active ? 'success' : 'error'" size="small">
                  {{ item.active ? '正常' : '关联已断开' }}
                </NTag>
                <NButton v-if="!item.active" size="tiny" data-testid="content-link-repair" :disabled="!selectedDocument" @click="repairLink(item)">重新绑定</NButton>
                <NButton size="tiny" quaternary @click="deleteLink(item)">移除关联</NButton>
              </article>
            </section>
          </template>
        </main>
      </section>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.content-loading { display: grid; min-height: 240px; place-items: center; }
.content-alert { margin-bottom: 12px; }
.profile-config { display: grid; max-width: 520px; gap: 14px; padding: 8px 2px; }
.profile-config h3 { margin: 0; font-size: 18px; }
.profile-config p { margin: -8px 0 2px; color: var(--vt-fg-muted); }
.profile-config label { display: grid; gap: 6px; color: var(--vt-fg-secondary); font-size: 12px; font-weight: 600; }
.content-layout { display: grid; grid-template-columns: 138px minmax(0, 1fr); gap: 20px; }
.field-navigation { position: sticky; top: 0; display: flex; align-self: start; flex-direction: column; gap: 5px; padding: 10px; border: 1px solid var(--vt-border); border-radius: 10px; background: var(--vt-bg-subtle); }
.field-navigation strong { margin-bottom: 4px; font-size: 11px; text-transform: uppercase; }
.field-navigation a { overflow: hidden; padding: 4px 5px; color: var(--vt-fg-secondary); font-size: 12px; text-decoration: none; text-overflow: ellipsis; white-space: nowrap; }
.content-main { min-width: 0; }
.content-actions { display: flex; justify-content: flex-end; gap: 6px; margin-bottom: 12px; }
.content-main h1 { margin: 0; font-size: 28px; letter-spacing: -.03em; }
.content-summary { margin: 10px 0 22px; color: var(--vt-fg-muted); font-size: 14px; line-height: 1.55; }
.content-body { min-height: 260px; white-space: pre-wrap; font-size: 15px; line-height: 1.75; }
.record-links { display: grid; gap: 8px; margin-top: 30px; padding-top: 18px; border-top: 1px solid var(--vt-border); }
.record-links header { display: flex; align-items: baseline; justify-content: space-between; }
.record-links header span { color: var(--vt-fg-muted); font-size: 11px; }
.link-create { display: grid; grid-template-columns: minmax(0, 1fr) 120px auto; gap: 6px; }
.link-card { display: grid; grid-template-columns: minmax(0, 1fr) auto auto auto; align-items: center; gap: 7px; padding: 8px 9px; border: 1px solid var(--vt-border); border-radius: 8px; }
.link-card div { display: grid; min-width: 0; }
.link-card strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.link-card small { color: var(--vt-fg-muted); }
@media (max-width: 680px) { .content-layout { grid-template-columns: 1fr; } .field-navigation { position: static; flex-direction: row; overflow: auto; } .link-create { grid-template-columns: 1fr; } .link-card { grid-template-columns: 1fr auto; } }
</style>
