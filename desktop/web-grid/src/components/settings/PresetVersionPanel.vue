<script setup lang="ts">
import { ref } from "vue";
import { NButton, NInput, NPopconfirm, NTag } from "naive-ui";
import { usePresetVersionService } from "@/services/presetVersionService";
import { usePresetVersionStore } from "@/stores/presetVersionStore";

const store = usePresetVersionStore();
const collection = ref("");
const itemId = ref("");
const name = ref("");

function service() {
  return usePresetVersionService();
}

function defaultView() {
  return { filters: [], sorts: [], search: "", visibleFields: [], layout: "table" } as const;
}

async function addPreset(): Promise<void> {
  if (!collection.value.trim() || !name.value.trim()) return;
  await service().savePreset(collection.value.trim(), name.value.trim(), defaultView());
  name.value = "";
}

async function addVersion(): Promise<void> {
  if (!collection.value.trim() || !itemId.value.trim()) return;
  await service().createVersion(
    collection.value.trim(),
    itemId.value.trim(),
    "",
    name.value.trim(),
  );
  name.value = "";
}
</script>

<template>
  <section class="preset-version-panel" data-testid="preset-version-panel">
    <header>
      <strong>本机数据中的可复用视图预设与记录草稿</strong>
      <small>视图预设用于保存常用的筛选、排序、显示列和布局，例如“一键查看本月待办”或“只显示库存不足的商品”。预设只保存在当前本机数据中，不涉及多人共享。</small>
      <small>记录草稿用于在大幅修改前留底、比较不同填写方案，或暂存尚未确认的内容。草稿是主记录某一时刻的快照；可以与当前主记录比较，也可以由你确认后覆盖主记录，但不会发布、不会自动合并。</small>
    </header>
    <div class="target-fields">
      <NInput v-model:value="collection" size="small" placeholder="集合键" aria-label="集合键" />
      <NInput v-model:value="itemId" size="small" placeholder="记录 ID（记录草稿）" aria-label="记录 ID" />
      <NInput v-model:value="name" size="small" placeholder="名称" aria-label="名称" />
    </div>
    <div class="panel-actions">
      <NButton size="small" :disabled="!collection.trim()" @click="service().listPresets(collection.trim())">加载预设</NButton>
      <NButton size="small" type="primary" :disabled="!collection.trim() || !name.trim()" @click="addPreset">保存预设</NButton>
      <NButton size="small" :disabled="!collection.trim() || !itemId.trim()" @click="service().listVersions(collection.trim(), itemId.trim())">加载记录草稿</NButton>
      <NButton size="small" type="primary" :disabled="!collection.trim() || !itemId.trim()" @click="addVersion">创建记录草稿</NButton>
    </div>

    <p v-if="store.error" class="panel-error" role="alert">{{ store.error }}</p>
    <p v-else-if="store.loading" class="panel-status">正在处理…</p>

    <div v-if="store.presets.length" class="entry-list" aria-label="预设列表">
      <article v-for="preset in store.presets" :key="preset.id">
        <div><strong>{{ preset.name }}</strong><small>{{ preset.scope }} · {{ preset.view.layout }}</small></div>
        <NPopconfirm :disabled="!preset.revision" positive-text="删除" negative-text="取消" @positive-click="service().deletePreset(preset.id, preset.revision)">
          <template #trigger><NButton size="tiny" quaternary type="error" :disabled="!preset.revision">删除</NButton></template>
          确认删除该预设？
        </NPopconfirm>
      </article>
    </div>

    <div v-if="store.versions.length" class="entry-list" aria-label="记录草稿列表">
      <article v-for="version in store.versions" :key="version.id">
        <div>
          <strong>{{ version.name || version.key }}</strong>
          <small>{{ version.key }}</small>
        </div>
        <NTag v-if="version.outdated" size="small" type="warning">主记录已变化</NTag>
        <NButton size="tiny" quaternary @click="service().saveVersion(collection.trim(), itemId.trim(), version.id)">更新快照</NButton>
        <NButton size="tiny" quaternary @click="service().compareVersion(collection.trim(), itemId.trim(), version.id)">比较</NButton>
        <NPopconfirm
          :disabled="store.comparison?.versionId !== version.id"
          positive-text="应用"
          negative-text="取消"
          @positive-click="store.comparison && service().promoteVersion(collection.trim(), itemId.trim(), version.id, store.comparison.mainHash)"
        >
          <template #trigger>
            <NButton size="tiny" quaternary type="primary" :disabled="store.comparison?.versionId !== version.id">应用到主记录</NButton>
          </template>
          用这个草稿覆盖当前主记录？该操作不是“发布”，只是更新本机主记录。
        </NPopconfirm>
        <NPopconfirm :disabled="!version.revision" positive-text="删除" negative-text="取消" @positive-click="service().deleteVersion(collection.trim(), itemId.trim(), version.id, version.revision)">
          <template #trigger><NButton size="tiny" quaternary type="error" :disabled="!version.revision">删除</NButton></template>
          确认删除该记录草稿？
        </NPopconfirm>
      </article>
    </div>
  </section>
</template>

<style scoped>
.preset-version-panel { display: grid; gap: 12px; margin-top: 16px; padding: 16px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-lg); }
header { display: flex; flex-direction: column; gap: 3px; margin: 0; }
header small, .entry-list small, .panel-status { color: var(--vt-fg-muted); }
.target-fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
.panel-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.entry-list { display: grid; border-top: 1px solid var(--vt-border); }
.entry-list article { display: flex; align-items: center; gap: 8px; min-height: 44px; border-bottom: 1px solid var(--vt-border); }
.entry-list article > div:first-child { display: flex; flex: 1; flex-direction: column; min-width: 0; }
.panel-error { margin: 0; color: var(--vt-color-danger-500, #d03050); }
@media (max-width: 720px) { .target-fields { grid-template-columns: 1fr; } }
</style>
