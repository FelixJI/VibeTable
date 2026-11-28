<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NSelect, NSpin } from "naive-ui";
import { BridgeOperationError } from "@/bridge/hostBridge";
import type {
  PluginFieldProtectionDetails, PluginFieldProtectionEntry, PluginFieldProtectionListItem,
  PluginFieldProtectionRef, PluginFieldProtectionSuggested,
} from "@/contracts/pluginFieldProtection";
import { t } from "@/i18n";
import { useHostBridge } from "@/services/bridgeContext";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

const props = defineProps<{ readonly initialPluginId: string | null }>();
const bridge = useHostBridge();
const session = useWorkspaceSessionStore();
const projectKey = computed(() => session.hasOpenWorkspace && !session.isTransitioning && session.activeWorkspaceId
  ? `local:${session.activeWorkspaceId.replaceAll("-", "")}` : null);
const list = ref<readonly PluginFieldProtectionListItem[]>([]);
const selectedPlugin = ref<string | null>(null);
const detail = ref<PluginFieldProtectionDetails | null>(null);
const enableSelection = ref<string[]>([]);
const releaseSelection = ref<string[]>([]);
const busy = ref(false);
const error = ref<string | null>(null);
let requestGeneration = 0;

const options = computed(() => list.value.map(item => ({
  value: item.pluginId,
  label: `${item.pluginId} · ${t(item.installed ? "settings.fieldProtection.activeCount" : "settings.fieldProtection.uninstalledCount", { count: item.protectedCount })}`,
})));
const logicalKey = ({ entity, field }: PluginFieldProtectionSuggested) => `${entity}/${field}`;
const physicalKey = ({ tableId, fieldId }: PluginFieldProtectionRef) => `${tableId}/${fieldId}`;
const enableable = computed(() => detail.value?.suggested.filter(item =>
  item.eligible && (item.state === "unprotected" || item.ownerStale)) ?? []);
const canEnable = computed(() => session.writable && detail.value?.installation?.status === "enabled"
  && detail.value.blockingReasons.length === 0);
const liveMappings = computed(() => new Set(detail.value?.suggested
  .filter(item => !item.diagnostic && item.state === "protected" && !item.ownerStale).map(physicalKey)));

function textError(cause: unknown): string {
  if (cause instanceof BridgeOperationError && cause.code?.startsWith("field_protection."))
    return t(`settings.fieldProtection.error.${cause.code.split(".")[1]}`);
  return t("settings.fieldProtection.error.generic");
}

function resetSelection(): void {
  enableSelection.value = [];
  releaseSelection.value = [];
}

async function read(pluginId: string | null, reloadList = false, fromApplication = false): Promise<void> {
  const workspace = projectKey.value;
  const generation = ++requestGeneration;
  detail.value = null;
  resetSelection();
  error.value = null;
  busy.value = !!workspace;
  if (!workspace) {
    selectedPlugin.value = null;
    list.value = [];
    return;
  }
  try {
    if (reloadList) {
      list.value = [];
      const items = await bridge.request("plugin.protection.list", { projectKey: workspace });
      if (generation !== requestGeneration) return;
      list.value = items;
    }
    if (!fromApplication && !list.value.some(item => item.pluginId === pluginId))
      pluginId = list.value[0]?.pluginId ?? null;
    selectedPlugin.value = pluginId;
    if (!pluginId) return;
    const result = await bridge.request("plugin.protection.describe", { projectKey: workspace, pluginId });
    if (generation !== requestGeneration) return;
    detail.value = result;
    if (!list.value.some(item => item.pluginId === result.pluginId)) {
      list.value = [...list.value, {
        pluginId: result.pluginId, installed: result.installation !== null, protectedCount: result.protected.length,
      }];
    }
  } catch (cause) {
    if (generation === requestGeneration) error.value = textError(cause);
  } finally {
    if (generation === requestGeneration) busy.value = false;
  }
}

watch([projectKey, () => session.sessionEpoch, () => props.initialPluginId],
  ([workspace, epoch, pluginId], previous) => {
    const newApplication = pluginId !== null && pluginId !== previous[2];
    const sameSession = workspace === previous[0] && epoch === previous[1];
    const preferred = newApplication ? pluginId : sameSession ? selectedPlugin.value : null;
    void read(preferred, true, newApplication);
  }, { immediate: true });

async function commit(kind: "enable" | "release"): Promise<void> {
  const current = detail.value;
  if (!current || !session.writable || busy.value || current.projectKey !== projectKey.value) return;
  const identity = { projectKey: current.projectKey, pluginId: current.pluginId };
  const fieldsToEnable = enableable.value.filter(item => enableSelection.value.includes(logicalKey(item)))
    .map(({ entity, field, tableId, fieldId }) => ({ entity, field, tableId, fieldId }));
  const fieldsToRelease = current.protected.filter(item => releaseSelection.value.includes(physicalKey(item)))
    .map(({ tableId, fieldId }) => ({ tableId, fieldId }));
  const generation = ++requestGeneration;
  busy.value = true;
  error.value = null;
  try {
    const installation = current.installation;
    let result: PluginFieldProtectionDetails;
    if (kind === "enable") {
      if (!canEnable.value || !installation || fieldsToEnable.length === 0) return;
      result = await bridge.request("plugin.protection.enable", {
        ...identity, installationId: installation.installationId,
        installationRevision: installation.revision, fields: fieldsToEnable,
      });
    } else {
      if (fieldsToRelease.length === 0) return;
      result = await bridge.request("plugin.protection.release", { ...identity, fields: fieldsToRelease });
    }
    if (generation !== requestGeneration) return;
    detail.value = result;
    list.value = list.value.map(item => item.pluginId === current.pluginId
      ? { ...item, installed: result.installation !== null, protectedCount: result.protected.length } : item);
    resetSelection();
  } catch (cause) {
    if (generation === requestGeneration) error.value = textError(cause);
  } finally {
    if (generation === requestGeneration) busy.value = false;
  }
}

function fieldState(item: PluginFieldProtectionSuggested): string {
  if (item.state === "protected_by_other") {
    return t("settings.fieldProtection.other", { pluginId: item.owner?.pluginId ?? "" });
  }
  if (item.diagnostic) return t("settings.fieldProtection.unmapped");
  if (item.ownerStale) return t("settings.fieldProtection.previousInstallation");
  return t(`settings.fieldProtection.${item.state}`);
}

function storedState(item: PluginFieldProtectionEntry): string {
  if (item.orphaned) return t("settings.fieldProtection.orphaned");
  if (detail.value?.installation?.installationId !== item.installationId)
    return t("settings.fieldProtection.previousInstallation");
  return t(liveMappings.value.has(physicalKey(item))
    ? "settings.fieldProtection.protected" : "settings.fieldProtection.previousMapping");
}
</script>

<template>
  <section class="field-protection" data-testid="plugin-field-protection-settings">
    <header>
      <h1>{{ t("settings.fieldProtection.title") }}</h1>
      <p>{{ t("settings.fieldProtection.description") }}</p>
    </header>
    <NAlert v-if="!projectKey" type="info">{{ t("settings.fieldProtection.openWorkspace") }}</NAlert>
    <template v-else>
      <div class="field-protection-toolbar">
        <NSelect
          :value="selectedPlugin"
          :options="options"
          :disabled="busy || !options.length"
          :placeholder="t('settings.fieldProtection.selectPlugin')"
          :aria-label="t('settings.fieldProtection.selectPlugin')"
          data-testid="field-protection-plugin"
          @update:value="pluginId => read(pluginId)"
        />
        <NButton :disabled="busy" data-testid="field-protection-refresh" @click="read(selectedPlugin, true)">
          {{ t("settings.fieldProtection.refresh") }}
        </NButton>
      </div>
      <NAlert v-if="error" type="error" data-testid="field-protection-error">{{ error }}</NAlert>
      <NAlert v-if="!session.writable" type="info">{{ t("settings.fieldProtection.readOnly") }}</NAlert>
      <NSpin :show="busy">
        <p v-if="!busy && !error && !detail">{{ t("settings.fieldProtection.empty") }}</p>
        <template v-if="detail">
          <p v-if="detail.blockingReasons.length">{{ detail.blockingReasons.map(reason => t(`settings.fieldProtection.reason.${reason}`)).join(" · ") }}</p>
          <div v-if="detail.suggested.length" class="field-protection-group">
            <h2>{{ t("settings.fieldProtection.suggested") }}</h2>
            <div class="field-protection-actions">
              <NButton :disabled="busy || !canEnable || !enableable.length" @click="enableSelection = enableable.map(logicalKey)">
                {{ t("settings.fieldProtection.selectEnableable") }}
              </NButton>
              <NButton type="primary" :disabled="busy || !canEnable || !enableSelection.length" data-testid="field-protection-enable" @click="commit('enable')">
                {{ t("settings.fieldProtection.enable", { count: enableSelection.length }) }}
              </NButton>
            </div>
            <table>
              <thead><tr><th scope="col">{{ t("settings.fieldProtection.select") }}</th><th scope="col">{{ t("settings.fieldProtection.column") }}</th><th scope="col">{{ t("settings.fieldProtection.state") }}</th></tr></thead>
              <tbody>
                <tr v-for="item in detail.suggested" :key="logicalKey(item)">
                  <td><input v-model="enableSelection" type="checkbox" :value="logicalKey(item)"
                    :aria-label="`${t('settings.fieldProtection.select')}: ${item.tableName || item.entity} / ${item.fieldName || item.field}`"
                    :disabled="busy || !canEnable || !item.eligible || item.state === 'protected' && !item.ownerStale"></td>
                  <td>{{ item.tableName || item.entity }} / {{ item.fieldName || item.field }}
                    <small v-if="item.tableId && item.fieldId">{{ item.tableId }} / {{ item.fieldId }}</small>
                  </td>
                  <td>{{ fieldState(item) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="field-protection-group">
            <h2>{{ t("settings.fieldProtection.stored") }}</h2>
            <p v-if="!detail.protected.length">{{ t("settings.fieldProtection.noneProtected") }}</p>
            <template v-else>
              <div class="field-protection-actions">
                <NButton :disabled="busy || !session.writable" @click="releaseSelection = detail!.protected.map(physicalKey)">
                  {{ t("settings.fieldProtection.selectStored") }}
                </NButton>
                <NButton :disabled="busy || !session.writable || !releaseSelection.length" data-testid="field-protection-release" @click="commit('release')">
                  {{ t("settings.fieldProtection.release", { count: releaseSelection.length }) }}
                </NButton>
              </div>
              <table data-testid="field-protection-stored">
                <thead><tr><th scope="col">{{ t("settings.fieldProtection.select") }}</th><th scope="col">{{ t("settings.fieldProtection.column") }}</th><th scope="col">{{ t("settings.fieldProtection.state") }}</th></tr></thead>
                <tbody>
                  <tr v-for="item in detail.protected" :key="physicalKey(item)">
                    <td><input v-model="releaseSelection" type="checkbox" :value="physicalKey(item)"
                      :aria-label="`${t('settings.fieldProtection.select')}: ${item.tableName} / ${item.fieldName}`"
                      :disabled="busy || !session.writable"></td>
                    <td>{{ item.tableName }} / {{ item.fieldName }}<small>{{ item.tableId }} / {{ item.fieldId }}</small></td>
                    <td>{{ storedState(item) }}</td>
                  </tr>
                </tbody>
              </table>
            </template>
          </div>
        </template>
      </NSpin>
    </template>
  </section>
</template>

<style scoped>
.field-protection { display: grid; gap: 14px; }
.field-protection h2 { margin: 0; font-size: 15px; }
.field-protection header p, .field-protection-group p { color: var(--vt-fg-secondary); margin: 4px 0; }
.field-protection-toolbar, .field-protection-actions { display: flex; gap: 8px; align-items: center; }
.field-protection-toolbar > :first-child { max-width: 420px; }
.field-protection-group { margin: 0 0 16px; padding: 14px; border: 1px solid var(--vt-border); border-radius: var(--vt-radius-sm); }
.field-protection-actions { margin: 10px 0; flex-wrap: wrap; }
.field-protection table { width: 100%; border-collapse: collapse; text-align: left; }
.field-protection th, .field-protection td { border-bottom: 1px solid var(--vt-border); padding: 6px; }
.field-protection th:first-child { width: 54px; }
.field-protection td { overflow-wrap: anywhere; }
.field-protection small { display: block; color: var(--vt-fg-secondary); }
</style>
