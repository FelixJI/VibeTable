<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { NIcon } from "naive-ui";
import { Box, PackageOpen, Play, Power, RotateCcw, ShieldAlert, Trash2, Upload } from "lucide-vue-next";
import type { PluginAuditEvent, PluginRisk, PluginSnapshot } from "@/contracts";
import { useSystemTimeZone } from "@/composables/useSystemTimeZone";
import { usePluginStore } from "@/stores/pluginStore";
import { createPluginCommandContext, usePluginService } from "@/services/pluginService";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { projectPluginTheme } from "@/components/plugins/pluginTheme";
import { publicCapabilityPolicy } from "@/services/publicCapabilityPolicy";
import { t } from "@/i18n";

const props = withDefaults(defineProps<{ autoLoad?: boolean }>(), { autoLoad: true });
const store = usePluginStore();
const service = usePluginService();
const ui = useUiStore();
const workspace = useWorkspaceStore();
const table = useTableStore();
const systemTimeZone = useSystemTimeZone();
const plannedUpgradePluginId = ref<string | null>(null);
const cleanupPrivateSettings = ref(false);
const uninstalling = ref(false);
const auditEvents = ref<readonly PluginAuditEvent[]>([]);
const pendingCleanup = ref<readonly PluginAuditEvent[]>([]);

const plugin = computed(() => store.selectedPlugin);
const theme = computed(() => projectPluginTheme({
  themeMode: ui.themeMode,
  locale: ui.locale,
  density: ui.density,
}));

function displayName(snapshot: PluginSnapshot): string {
  return localize(snapshot.manifest.displayName, snapshot.pluginId);
}
function localize(values: Readonly<Record<string, string>>, fallback: string): string {
  return values[ui.locale] ?? values["zh-CN"] ?? values["en-US"] ?? Object.values(values)[0] ?? fallback;
}
function statusLabel(value: PluginSnapshot["status"]): string {
  return ({ enabled: "运行正常", disabled: "整体禁用", error: "整体阻断" })[value];
}
function riskLabel(value: PluginRisk): string {
  return t(`plugin.risk.${value}`);
}
function formatAuditTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleString(ui.locale, { timeZone: systemTimeZone.value });
}
function permissions(snapshot: PluginSnapshot): string[] {
  return flatten(snapshot.manifest.permissions);
}
function blockingReasons(snapshot: PluginSnapshot): readonly string[] {
  if (snapshot.blockingReasons?.length) return snapshot.blockingReasons;
  return snapshot.disabledReason ? [snapshot.disabledReason] : [];
}
function compatibility(value: Readonly<Record<string, unknown>>): string {
  return Object.entries(value).map(([key, item]) => `${key}: ${String(item)}`).join(" · ") || "未声明";
}
function flatten(value: unknown, prefix = ""): string[] {
  if (Array.isArray(value)) return value.flatMap((item) => flatten(item, prefix));
  if (typeof value === "object" && value !== null) {
    return Object.entries(value).flatMap(([key, item]) => flatten(item, prefix ? `${prefix}.${key}` : key));
  }
  if (value === false || value === null || value === undefined) return [];
  return [prefix ? `${prefix}:${String(value)}` : String(value)];
}
function commandContext() {
  return createPluginCommandContext({
    projectKey: store.projectKey,
    collection: workspace.currentTable,
    selectedKeys: [],
    querySnapshot: table.pages[0]?.querySnapshot ?? null,
    locale: ui.locale,
    theme: theme.value.mode,
    density: ui.density,
    user: store.currentUser,
    hostVersion: store.hostVersion,
  });
}
async function safely(task: () => Promise<unknown>): Promise<void> {
  try { await task(); } catch { /* store owns the user-visible error */ }
}
async function inspectInstall(sourceToken: "host-picker:package" | "host-picker:folder"): Promise<void> {
  await safely(async () => {
    const plan = await service.inspectInstall(sourceToken);
    plannedUpgradePluginId.value = store.plugins.find(
      (item) => item.pluginId === plan.manifest.pluginId,
    )?.pluginId ?? null;
  });
}
async function commitPlan(): Promise<void> {
  const plan = store.installPlan;
  if (!plan) return;
  await safely(async () => {
    const existing = plannedUpgradePluginId.value
      ? store.plugins.find((item) => item.pluginId === plannedUpgradePluginId.value)
      : null;
    if (existing) {
      if (!publicCapabilityPolicy.pluginLifecycleMutations) return;
      await service.upgrade(existing, plan);
    }
    else {
      const installed = await service.commitInstall(plan);
      // The commit consumes the plan even if the follow-up convenience
      // enable fails. Clear it before issuing a second RPC so the UI cannot
      // offer a duplicate commit for an installation that already succeeded.
      plannedUpgradePluginId.value = null;
      store.setInstallPlan(null);
      // The install button is the user's explicit permission approval. Keep
      // the registry's safe disabled-by-default primitive, but complete the
      // product action by enabling the newly approved installation.
      const userDisabledOnly = installed.status === "disabled"
        && installed.disabledReason === "disabled_by_user"
        && !(installed.blockingReasons?.length);
      if (userDisabledOnly) await service.setEnabled(installed, true);
      return;
    }
    plannedUpgradePluginId.value = null;
    store.setInstallPlan(null);
  });
}
function cancelPlan(): void {
  plannedUpgradePluginId.value = null;
  store.setInstallPlan(null);
}
async function inspectUpgrade(current: PluginSnapshot): Promise<void> {
  await safely(async () => {
    const sourceToken = current.sourceType === "local-folder"
      ? "host-picker:folder"
      : "host-picker:package";
    const plan = await service.inspectInstall(sourceToken);
    if (plan.manifest.pluginId !== current.pluginId) {
      store.setInstallPlan(null);
      throw new Error("所选来源不是当前插件的新版本");
    }
    plannedUpgradePluginId.value = current.pluginId;
  });
}
async function openAction(pluginId: string, actionId: string): Promise<void> {
  await safely(() => service.describeAction(pluginId, actionId, commandContext()));
}
async function selectPlugin(pluginId: string): Promise<void> {
  store.selectPlugin(pluginId);
  auditEvents.value = await service.listAudit(pluginId);
}
async function retryCleanup(pluginId: string): Promise<void> {
  await safely(async () => {
    await service.retryCleanup(pluginId);
    pendingCleanup.value = await service.listPendingCleanup();
  });
}

onMounted(() => {
  service.init();
  if (props.autoLoad) void safely(async () => {
    await service.list();
    pendingCleanup.value = await service.listPendingCleanup();
    if (store.selectedPlugin) auditEvents.value = await service.listAudit(store.selectedPlugin.pluginId);
  });
});
onBeforeUnmount(() => service.dispose());
</script>

<template>
  <div class="plugin-center">
    <header class="center-header">
      <div>
        <span class="section-code">PROJECT / PLUGINS</span>
        <h1>插件中心</h1>
        <p>安装、权限和运行状态均限定在当前项目。</p>
      </div>
      <div class="install-source">
        <button class="install-button" type="button" data-testid="plugin-install-package" @click="inspectInstall('host-picker:package')">
          <NIcon :size="15"><Upload /></NIcon> 选择 .vtplugin
        </button>
        <button class="install-button" type="button" data-testid="plugin-install-folder" @click="inspectInstall('host-picker:folder')">
          <NIcon :size="15"><PackageOpen /></NIcon> 加载本地文件夹
        </button>
      </div>
    </header>

    <div v-if="store.lastError" class="global-error" role="alert"><ShieldAlert :size="15" />{{ store.lastError }}</div>

    <section v-if="pendingCleanup.length" class="cleanup-queue" aria-label="待清理托管资源">
      <strong>可重试的插件资源清理</strong>
      <div v-for="event in pendingCleanup" :key="event.pluginId"><code>{{ event.pluginId }}</code><span>{{ String(event.details.cleanupError ?? '本地插件资源暂时不可用') }}</span><button type="button" @click="retryCleanup(event.pluginId)">立即重试</button></div>
    </section>

    <section v-if="store.installPlan" class="install-plan" data-testid="plugin-install-plan">
      <div class="plan-title">
        <span>INSTALL PLAN / READY</span>
        <strong>{{ localize(store.installPlan.manifest.displayName, store.installPlan.manifest.pluginId) }} <code>{{ store.installPlan.manifest.version }}</code></strong>
      </div>
      <div><small>PACKAGE HASH</small><code>{{ store.installPlan.packageHash }}</code></div>
      <div><small>PERMISSIONS</small><p>{{ flatten(store.installPlan.manifest.permissions).join(' · ') || '无额外权限' }}</p></div>
      <div><small>COMPATIBILITY</small><p>{{ compatibility(store.installPlan.manifest.compatibility) }}</p></div>
      <div><small>ACTIONS / RISK</small><p>{{ store.installPlan.manifest.actions.map((action) => `${localize(action.displayName, action.actionId)}:${riskLabel(action.risk)}`).join(' · ') || '无动作' }}</p></div>
      <div><small>LOCAL WORKERS</small><p>{{ store.installPlan.manifest.actions.map((action) => action.workerEntry).join(' · ') || '无本地动作' }}</p></div>
      <div><small>PROJECT REVISION</small><code>{{ store.installPlan.projectRevision }}</code></div>
      <div class="plan-actions">
        <button type="button" class="quiet-button" @click="cancelPlan">取消</button>
        <span
          v-if="plannedUpgradePluginId && !publicCapabilityPolicy.pluginLifecycleMutations"
          data-testid="plugin-upgrade-internal-only"
        >升级提交暂未公开</span>
        <button
          v-else
          data-testid="plugin-install-commit"
          type="button"
          class="primary-button"
          @click="commitPlan"
        >{{ plannedUpgradePluginId ? '批准变更并升级' : '批准权限并安装' }}</button>
      </div>
    </section>

    <div class="center-grid">
      <aside class="catalog" aria-label="已安装插件">
        <div class="catalog-caption"><span>INSTALLED</span><b>{{ store.plugins.length }}</b></div>
        <button
          v-for="item in store.plugins"
          :key="item.pluginId"
          type="button"
          class="plugin-row"
          :class="{ active: item.pluginId === plugin?.pluginId }"
          :aria-current="item.pluginId === plugin?.pluginId ? 'page' : undefined"
          @click="selectPlugin(item.pluginId)"
        >
          <span class="package-glyph"><Box :size="16" /></span>
          <span class="plugin-row-copy"><strong>{{ displayName(item) }}</strong><code>{{ item.pluginId }} · v{{ item.version }}</code></span>
          <i :data-health="item.status"></i>
        </button>
        <div v-if="!store.plugins.length" class="empty-catalog"><PackageOpen :size="24" /><strong>当前项目尚未安装插件</strong><span>先检查 `.vtplugin` 包或本地文件夹。</span></div>
      </aside>

      <main v-if="plugin" class="plugin-detail">
        <div class="identity-line">
          <div><span class="section-code">{{ plugin.pluginId }}</span><h2>{{ displayName(plugin) }}</h2></div>
          <button data-testid="plugin-toggle" class="toggle-button" :class="{ enabled: plugin.status === 'enabled' }" type="button" @click="safely(() => service.setEnabled(plugin!, plugin!.status !== 'enabled'))">
            <Power :size="14" />{{ plugin.status === 'enabled' ? '整体启用' : '整体禁用' }}
          </button>
        </div>

        <div class="status-strip" :data-health="plugin.status">
          <div><span>PROJECT STATE</span><strong>{{ statusLabel(plugin.status) }}</strong></div>
          <div><span>VERSION</span><strong>{{ plugin.version }}</strong></div>
          <div><span>SOURCE</span><strong>{{ plugin.sourceType === 'local-folder' ? '本地文件夹' : '离线包' }}</strong></div>
          <div><span>REVISION</span><strong>#{{ plugin.revision }}</strong></div>
        </div>

        <div v-if="plugin.sourceChanged" data-testid="plugin-source-changed" class="global-error" role="status">
          <Upload :size="15" />本地开发文件夹已变化。请检查新的权限与本地执行计划后显式重新加载。
        </div>

        <section v-if="blockingReasons(plugin).length" class="blockers">
          <header><ShieldAlert :size="15" /><strong>插件整体不可用</strong><span>{{ blockingReasons(plugin).length }} 个阻断原因</span></header>
          <ul><li v-for="reason in blockingReasons(plugin)" :key="reason">{{ reason }}</li></ul>
        </section>

        <section class="detail-section package-facts">
          <header><span>PACKAGE / CAPABILITIES</span><h3>包与权限</h3></header>
          <dl><div><dt>包哈希</dt><dd><code>{{ plugin.packageHash }}</code></dd></div><div><dt>权限声明</dt><dd><span v-for="permission in permissions(plugin)" :key="permission" class="permission-chip">{{ permission }}</span><em v-if="!permissions(plugin).length">无额外权限</em></dd></div></dl>
        </section>

        <section class="detail-section audit-log">
          <header><span>TASKS / AUDIT</span><h3>任务摘要与审计</h3></header>
          <div v-if="store.activeTask?.pluginId === plugin.pluginId" class="task-summary"><strong>{{ store.activeTask.actionId }}</strong><span>{{ store.activeTask.state }}</span><code>{{ store.activeTask.runId }}</code></div>
          <article v-for="event in auditEvents" :key="event.eventId">
            <time>{{ formatAuditTimestamp(event.startedAt) }}</time><strong>{{ event.eventType }}</strong><span :data-outcome="event.outcome">{{ event.outcome }}</span><code>{{ event.runId ?? event.packageHash }}</code><small v-if="event.durationMs !== null">{{ event.durationMs }} ms</small>
          </article>
          <p v-if="!auditEvents.length" class="empty-line">暂无审计记录。</p>
        </section>

        <section class="detail-section">
          <header><span>ACTIONS / RISK</span><h3>插件动作</h3></header>
          <article v-for="action in plugin.manifest.actions" :key="action.actionId" class="action-row">
            <div><strong>{{ localize(action.displayName, action.actionId) }}</strong><code>{{ action.actionId }}</code></div>
            <span class="risk-chip" :data-risk="action.risk">{{ riskLabel(action.risk) }}</span>
            <p>{{ localize(action.description, action.mode) }}</p>
            <button class="run-button" type="button" :disabled="plugin.status !== 'enabled' || action.invocation !== 'manual'" @click="openAction(plugin.pluginId, action.actionId)"><Play :size="13" />运行</button>
          </article>
          <p v-if="!plugin.manifest.actions.length" class="empty-line">此插件不注册桌面动作。</p>
        </section>

        <footer class="lifecycle-actions">
          <button data-testid="plugin-upgrade" type="button" class="quiet-button" title="选择新版本并先检查变更计划" @click="inspectUpgrade(plugin)"><Upload :size="13" />{{ plugin.sourceType === 'local-folder' ? (plugin.sourceChanged ? '检查文件夹变更' : '检查并重新加载文件夹') : '检查离线升级' }}</button>
          <button v-if="publicCapabilityPolicy.pluginLifecycleMutations" data-testid="plugin-rollback" type="button" class="quiet-button" @click="safely(() => service.rollback(plugin!))"><RotateCcw :size="13" />回滚上一版本</button>
          <button v-if="publicCapabilityPolicy.pluginLifecycleMutations && !uninstalling" data-testid="plugin-uninstall" type="button" class="danger-text-button" @click="uninstalling = true"><Trash2 :size="13" />卸载</button>
          <div v-else-if="publicCapabilityPolicy.pluginLifecycleMutations" class="uninstall-confirm"><span>将清理插件本地资源。</span><label><input v-model="cleanupPrivateSettings" data-testid="plugin-uninstall-private-settings" type="checkbox" />清理私有设置</label><button type="button" @click="uninstalling = false">取消</button><button data-testid="plugin-uninstall-confirm" type="button" @click="safely(() => service.uninstall(plugin!, cleanupPrivateSettings))">确认卸载</button></div>
        </footer>
      </main>

      <main v-else class="no-selection"><Box :size="32" /><h2>选择一个插件查看项目状态</h2></main>
    </div>

  </div>
</template>

<style scoped>
.plugin-center { position: relative; display: flex; height: 100%; flex-direction: column; overflow: hidden; color: var(--vt-fg); background: var(--vt-bg-subtle); }
.center-header { display: flex; height: 86px; align-items: center; justify-content: space-between; padding: 14px 22px; border-bottom: 1px solid var(--vt-border); background: var(--vt-bg); }
.section-code, .catalog-caption span, .detail-section header > span, .install-plan small { color: var(--vt-fg-muted); font: 650 10px/1.2 ui-monospace, SFMono-Regular, Consolas, monospace; letter-spacing: .12em; }
.center-header h1 { margin: 4px 0 1px; font-size: 20px; letter-spacing: -.02em; }
.center-header p { margin: 0; color: var(--vt-fg-muted); font-size: 12px; }
.install-source { display: flex; align-items: center; gap: 7px; }
.install-source input { width: 260px; height: 32px; padding: 0 9px; color: var(--vt-fg); border: 1px solid var(--vt-border); border-radius: 4px; background: var(--vt-bg); }
.install-button, .primary-button { display: inline-flex; min-height: 32px; align-items: center; gap: 6px; padding: 0 12px; color: #fff; border: 0; border-radius: 4px; background: var(--vt-color-primary-600); cursor: pointer; }
.install-button:disabled, .primary-button:disabled { opacity: .5; cursor: default; }
.global-error { display: flex; gap: 7px; padding: 7px 22px; color: var(--vt-color-danger); border-bottom: 1px solid color-mix(in srgb, var(--vt-color-danger) 22%, var(--vt-border)); background: color-mix(in srgb, var(--vt-color-danger) 7%, var(--vt-bg)); }
.cleanup-queue { display: grid; gap: 6px; padding: 9px 22px; color: var(--vt-color-warning); border-bottom: 1px solid color-mix(in srgb, var(--vt-color-warning) 35%, var(--vt-border)); background: color-mix(in srgb, var(--vt-color-warning) 7%, var(--vt-bg)); }.cleanup-queue > div { display: grid; grid-template-columns: 220px 1fr auto; gap: 10px; align-items: center; }.cleanup-queue button { min-height: 27px; border: 1px solid var(--vt-border); border-radius: 4px; background: var(--vt-bg); }
.install-plan { display: grid; grid-template-columns: 1.2fr 1fr 1.4fr .8fr auto; gap: 16px; min-width: 0; align-items: center; padding: 11px 22px; border-bottom: 1px solid var(--vt-color-warning); background: color-mix(in srgb, var(--vt-color-warning) 7%, var(--vt-bg)); }
.install-plan { grid-template-columns: repeat(4, minmax(140px, 1fr)); }
.install-plan .plan-title, .install-plan .plan-actions { grid-column: span 2; }
.install-plan .plan-title { display: grid; gap: 3px; }.install-plan .plan-title > span { color: var(--vt-color-warning); font: 650 10px ui-monospace, monospace; }.install-plan p { margin: 2px 0 0; }.install-plan ul { margin: 0; color: var(--vt-color-danger); }.plan-actions { display: flex; gap: 6px; }
.install-plan > div, .install-plan code, .install-plan p { min-width: 0; overflow-wrap: anywhere; }
.center-grid { display: grid; grid-template-columns: 252px minmax(0, 1fr); flex: 1 1 auto; min-height: 0; }
.catalog { overflow: auto; border-right: 1px solid var(--vt-border); background: var(--vt-bg); }
.catalog-caption { display: flex; align-items: center; justify-content: space-between; height: 37px; padding: 0 13px; border-bottom: 1px solid var(--vt-border); }.catalog-caption b { font: 600 11px ui-monospace, monospace; }
.plugin-row { display: grid; grid-template-columns: 30px 1fr 8px; gap: 8px; width: 100%; align-items: center; padding: 10px 11px; color: var(--vt-fg); text-align: left; border: 0; border-bottom: 1px solid var(--vt-border); background: transparent; cursor: pointer; }.plugin-row:hover { background: var(--vt-bg-subtle); }.plugin-row.active { background: var(--vt-color-primary-50); box-shadow: inset 3px 0 var(--vt-color-primary-500); }.package-glyph { display: grid; width: 28px; height: 28px; place-items: center; color: var(--vt-color-primary-500); border: 1px solid var(--vt-border); border-radius: 5px; background: var(--vt-bg); }.plugin-row-copy { display: grid; min-width: 0; }.plugin-row-copy code { overflow: hidden; color: var(--vt-fg-muted); font-size: 9px; text-overflow: ellipsis; }.plugin-row > i { width: 7px; height: 7px; border-radius: 50%; background: var(--vt-color-success); }.plugin-row > i[data-health="disabled"] { background: var(--vt-color-warning); }.plugin-row > i[data-health="error"] { background: var(--vt-color-danger); }
.empty-catalog, .no-selection { display: grid; place-items: center; align-content: center; gap: 7px; height: 220px; padding: 20px; color: var(--vt-fg-muted); text-align: center; }.empty-catalog span { font-size: 11px; }
.plugin-detail { overflow: auto; padding: 18px 22px 30px; }.identity-line { display: flex; align-items: flex-start; justify-content: space-between; }.identity-line h2 { margin: 4px 0 12px; font-size: 21px; }.toggle-button { display: inline-flex; align-items: center; gap: 6px; height: 31px; padding: 0 10px; color: var(--vt-color-danger); border: 1px solid currentColor; border-radius: 4px; background: transparent; cursor: pointer; }.toggle-button.enabled { color: var(--vt-color-success); }
.status-strip { display: grid; grid-template-columns: repeat(4, 1fr); border: 1px solid var(--vt-border); border-left: 3px solid var(--vt-color-success); background: var(--vt-bg); }.status-strip[data-health="error"] { border-left-color: var(--vt-color-danger); }.status-strip[data-health="disabled"] { border-left-color: var(--vt-color-warning); }.status-strip div { display: grid; gap: 2px; padding: 8px 11px; border-right: 1px solid var(--vt-border); }.status-strip span { color: var(--vt-fg-muted); font: 600 9px ui-monospace, monospace; letter-spacing: .08em; }.status-strip strong { font-size: 12px; }
.blockers { margin-top: 12px; color: var(--vt-color-danger); border: 1px solid color-mix(in srgb, var(--vt-color-danger) 30%, var(--vt-border)); background: color-mix(in srgb, var(--vt-color-danger) 5%, var(--vt-bg)); }.blockers header { display: flex; gap: 7px; align-items: center; padding: 8px 10px; border-bottom: 1px solid color-mix(in srgb, var(--vt-color-danger) 18%, var(--vt-border)); }.blockers header span { margin-left: auto; font-size: 10px; }.blockers ul { display: grid; gap: 4px; margin: 8px 10px 9px; padding-left: 19px; }
.detail-section { margin-top: 14px; border: 1px solid var(--vt-border); background: var(--vt-bg); }.detail-section > header { display: flex; align-items: baseline; gap: 10px; padding: 9px 11px; border-bottom: 1px solid var(--vt-border); }.detail-section h3 { margin: 0; font-size: 13px; }.package-facts dl { margin: 0; }.package-facts dl > div { display: grid; grid-template-columns: 115px minmax(0, 1fr); padding: 8px 11px; border-bottom: 1px solid var(--vt-border); }.package-facts dt { color: var(--vt-fg-muted); }.package-facts dd { display: flex; min-width: 0; flex-wrap: wrap; gap: 5px; margin: 0; }.package-facts dd code { overflow: hidden; max-width: 100%; text-overflow: ellipsis; white-space: nowrap; }.permission-chip, .risk-chip { max-width: 100%; padding: 2px 6px; overflow-wrap: anywhere; color: var(--vt-fg-secondary); border: 1px solid var(--vt-border); border-radius: 999px; font: 550 10px ui-monospace, monospace; background: var(--vt-bg-subtle); }
.audit-log article, .task-summary { display: grid; grid-template-columns: 145px 1fr 100px minmax(180px, 1fr) auto; gap: 10px; align-items: center; padding: 7px 11px; border-bottom: 1px solid var(--vt-border); }.audit-log time, .audit-log code, .audit-log small { color: var(--vt-fg-muted); font-size: 9px; }.audit-log span[data-outcome="failed"], .audit-log span[data-outcome="pending-cleanup"] { color: var(--vt-color-danger); }.task-summary { background: var(--vt-bg-subtle); }
.action-row { display: grid; grid-template-columns: minmax(140px, .7fr) 60px 1fr auto; gap: 10px; align-items: center; min-height: 50px; padding: 7px 10px; border-bottom: 1px solid var(--vt-border); }.action-row > div { display: grid; }.action-row p { margin: 0; color: var(--vt-fg-muted); }.risk-chip[data-risk="write"] { color: var(--vt-color-warning); }.risk-chip[data-risk="destructive"] { color: var(--vt-color-danger); }.run-button, .quiet-button, .danger-text-button { display: inline-flex; min-height: 29px; align-items: center; justify-content: center; gap: 5px; padding: 0 9px; color: var(--vt-fg); border: 1px solid var(--vt-border); border-radius: 4px; background: var(--vt-bg); cursor: pointer; }.run-button:disabled, .quiet-button:disabled { opacity: .45; cursor: default; }.empty-line { margin: 0; padding: 13px; color: var(--vt-fg-muted); }
.lifecycle-actions { display: flex; gap: 7px; margin-top: 14px; }.danger-text-button { margin-left: auto; color: var(--vt-color-danger); border-color: color-mix(in srgb, var(--vt-color-danger) 30%, var(--vt-border)); }.uninstall-confirm { display: flex; gap: 6px; align-items: center; margin-left: auto; color: var(--vt-color-danger); }.uninstall-confirm button { min-height: 27px; border: 1px solid var(--vt-border); background: var(--vt-bg); }
.action-overlay { position: absolute; z-index: 50; inset: 0; display: grid; place-items: stretch end; background: rgba(10, 15, 22, .38); backdrop-filter: blur(2px); }.action-overlay > :deep(*) { height: 100%; }.action-overlay :deep(.surface-shell) { width: min(920px, calc(100% - 60px)); margin: 30px; }
@media (max-width: 900px) { .center-grid { grid-template-columns: 210px minmax(0, 1fr); }.status-strip { grid-template-columns: repeat(2, 1fr); }.action-row { grid-template-columns: 1fr auto auto; }.action-row p { grid-column: 1 / -1; } }
</style>
