import { computed, ref, type ComputedRef, type Ref } from "vue";
import type { DropdownOption } from "naive-ui";

import { projectPluginTheme } from "@/components/plugins/pluginTheme";
import type { PluginRisk, PluginSurfaceThemeSnapshot } from "@/contracts";
import { createPluginCommandContext, type usePluginService } from "@/services/pluginService";
import type { usePluginStore } from "@/stores/pluginStore";
import type { useTableStore } from "@/stores/tableStore";
import type { useUiStore } from "@/stores/uiStore";
import type { useWorkspaceStore } from "@/stores/workspaceStore";

type PluginService = ReturnType<typeof usePluginService>;
type PluginStore = ReturnType<typeof usePluginStore>;
type TableStore = ReturnType<typeof useTableStore>;
type UiStore = ReturnType<typeof useUiStore>;
type WorkspaceStore = ReturnType<typeof useWorkspaceStore>;

type PluginStorePort = Pick<PluginStore,
  | "plugins" | "projectKey" | "currentUser" | "hostVersion"
  | "describedAction" | "activeContext" | "activeTask">;
type PluginTablePort = Pick<TableStore, "pages" | "schema">;
type PluginUiPort = Pick<UiStore, "themeMode" | "locale" | "density">;
type PluginWorkspacePort = Pick<WorkspaceStore, "currentTable">;
export type WorkspacePluginServicePort = Pick<PluginService,
  "describeAction" | "startAction" | "resolveInteraction" | "cancelTask">;

interface ContextMenuState {
  show: boolean;
  x: number;
  y: number;
  rowKey: string | number | null;
  field: string | null;
}

interface ColumnMenuState {
  show: boolean;
  x: number;
  y: number;
  field: string | null;
}

interface ToolbarAction {
  readonly key: string;
  readonly label: string;
  readonly risk: PluginRisk;
  readonly disabled: boolean;
}

export type WorkspacePluginIntent =
  | { readonly type: "action.open"; readonly key: string }
  | {
    readonly type: "rowMenu.open";
    readonly rowKey: string | number;
    readonly field?: string;
    readonly x: number;
    readonly y: number;
  }
  | { readonly type: "rowMenu.visible"; readonly show: boolean }
  | { readonly type: "rowMenu.select"; readonly key: string }
  | { readonly type: "columnMenu.open"; readonly field: string; readonly x: number; readonly y: number }
  | { readonly type: "columnMenu.visible"; readonly show: boolean }
  | { readonly type: "columnMenu.select"; readonly key: string }
  | { readonly type: "action.start"; readonly payload: Readonly<Record<string, unknown>> }
  | { readonly type: "interaction.resolve"; readonly decision: "approved" | "rejected" }
  | { readonly type: "task.cancel" };

export interface WorkspacePluginController {
  readonly theme: ComputedRef<PluginSurfaceThemeSnapshot>;
  readonly toolbarActions: ComputedRef<ToolbarAction[]>;
  readonly rowMenuOptions: ComputedRef<DropdownOption[]>;
  readonly columnMenuOptions: DropdownOption[];
  readonly rowMenu: Ref<ContextMenuState>;
  readonly columnMenu: Ref<ColumnMenuState>;
  dispatch(intent: WorkspacePluginIntent): Promise<void>;
}

export interface WorkspacePluginDependencies {
  readonly workspace: PluginWorkspacePort;
  readonly table: PluginTablePort;
  readonly ui: PluginUiPort;
  readonly plugins: PluginStorePort;
  readonly service: WorkspacePluginServicePort;
  readonly selectedRows: () => readonly Readonly<Record<string, unknown>>[];
  readonly openHistory: (selection: {
    readonly scope: "row" | "cell";
    readonly itemId: string;
    readonly field?: string;
  }) => void;
  readonly openFieldCreate: (tableId: string) => void;
  readonly openFieldEdit: (tableId: string, fieldId: string) => void;
  readonly reportError: (content: string) => void;
  readonly historyCellLabel: () => string;
  readonly historyRowLabel: () => string;
  readonly riskLabel: (risk: string) => string;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function createWorkspacePluginController(
  dependencies: WorkspacePluginDependencies,
): WorkspacePluginController {
  const theme = computed(() => projectPluginTheme({
    themeMode: dependencies.ui.themeMode,
    locale: dependencies.ui.locale,
    density: dependencies.ui.density,
  }));
  const registeredActions = computed(() => dependencies.plugins.plugins.flatMap(plugin =>
    plugin.manifest.actions.map(action => ({
      key: `${plugin.pluginId}/${action.actionId}`,
      plugin,
      action,
      label: action.displayName[dependencies.ui.locale]
        ?? action.displayName["zh-CN"]
        ?? action.displayName["en-US"]
        ?? action.actionId,
    })),
  ));
  const toolbarActions = computed<ToolbarAction[]>(() => registeredActions.value
    .filter(({ action }) => action.placements.includes("table.toolbar"))
    .map(({ key, label, plugin, action }) => ({
      key,
      label,
      risk: action.risk,
      disabled: plugin.status !== "enabled"
        || action.invocation !== "manual"
        || !dependencies.workspace.currentTable,
    })));
  const pluginContextOptions = computed<DropdownOption[]>(() => registeredActions.value
    .filter(({ action }) => action.placements.includes("table.context-menu"))
    .map(({ key, label, plugin, action }) => ({
      key,
      label: `${label} · ${dependencies.riskLabel(action.risk)}`,
      disabled: plugin.status !== "enabled" || action.invocation !== "manual",
    })));
  const rowMenu = ref<ContextMenuState>({
    show: false,
    x: 0,
    y: 0,
    rowKey: null,
    field: null,
  });
  const columnMenu = ref<ColumnMenuState>({ show: false, x: 0, y: 0, field: null });
  const rowMenuOptions = computed<DropdownOption[]>(() => [
    {
      label: dependencies.historyCellLabel(),
      key: "history:cell",
      disabled: !rowMenu.value.field,
    },
    { label: dependencies.historyRowLabel(), key: "history:row", disabled: false },
    ...(pluginContextOptions.value.length
      ? [{ type: "divider" as const, key: "history-divider" },
        ...pluginContextOptions.value]
      : []),
  ]);
  const columnMenuOptions: DropdownOption[] = [
    { label: "字段设置", key: "settings", disabled: false },
    { label: "在右侧新增字段", key: "create", disabled: false },
  ];

  function selectedRowKeys(): readonly (string | number)[] {
    return dependencies.selectedRows().flatMap(row =>
      typeof row.rowKey === "string" || typeof row.rowKey === "number" ? [row.rowKey] : [],
    );
  }

  function commandContext(keys: readonly (string | number)[]) {
    return createPluginCommandContext({
      projectKey: dependencies.plugins.projectKey,
      collection: dependencies.workspace.currentTable,
      selectedKeys: keys,
      querySnapshot: dependencies.table.pages[0]?.querySnapshot ?? null,
      locale: dependencies.ui.locale,
      theme: theme.value.mode,
      density: dependencies.ui.density,
      user: dependencies.plugins.currentUser,
      hostVersion: dependencies.plugins.hostVersion,
    });
  }

  async function openAction(key: string, keys = selectedRowKeys()): Promise<void> {
    const registered = registeredActions.value.find(item => item.key === key);
    if (!registered) return;
    try {
      await dependencies.service.describeAction(
        registered.plugin.pluginId,
        registered.action.actionId,
        commandContext(keys),
      );
    } catch (error) {
      dependencies.reportError(errorMessage(error));
    }
  }

  async function dispatch(intent: WorkspacePluginIntent): Promise<void> {
    switch (intent.type) {
      case "action.open": await openAction(intent.key); return;
      case "rowMenu.open":
        rowMenu.value = { show: true, ...intent, field: intent.field ?? null };
        return;
      case "rowMenu.visible": rowMenu.value.show = intent.show; return;
      case "columnMenu.open":
        rowMenu.value.show = false;
        columnMenu.value = { show: true, field: intent.field, x: intent.x, y: intent.y };
        return;
      case "columnMenu.visible": columnMenu.value.show = intent.show; return;
      case "columnMenu.select": {
        const tableId = dependencies.workspace.currentTable;
        const column = dependencies.table.schema?.find(item => item.name === columnMenu.value.field);
        columnMenu.value.show = false;
        if (!tableId) return;
        if (intent.key === "create") {
          dependencies.openFieldCreate(tableId);
          return;
        }
        if (!column?.fieldId) {
          dependencies.reportError("该列没有产品字段身份，无法打开字段设置");
          return;
        }
        dependencies.openFieldEdit(tableId, column.fieldId);
        return;
      }
      case "rowMenu.select": {
        const { rowKey, field } = rowMenu.value;
        rowMenu.value.show = false;
        if (rowKey !== null && intent.key === "history:cell" && field) {
          dependencies.openHistory({ scope: "cell", itemId: String(rowKey), field });
          return;
        }
        if (rowKey !== null && intent.key === "history:row") {
          dependencies.openHistory({ scope: "row", itemId: String(rowKey) });
          return;
        }
        if (rowKey !== null) await openAction(intent.key, [rowKey]);
        return;
      }
      case "action.start": {
        const description = dependencies.plugins.describedAction;
        const context = dependencies.plugins.activeContext;
        if (!description || !context) return;
        const input: Record<string, unknown> = { ...intent.payload };
        const properties = description.inputSchema.properties;
        if (
          typeof properties === "object"
          && properties !== null
          && !Array.isArray(properties)
          && "collection" in properties
          && input.collection === undefined
          && context.collection
        ) input.collection = context.collection;
        try {
          await dependencies.service.startAction(
            description.pluginId,
            description.actionId,
            input,
            context,
          );
        } catch (error) {
          dependencies.reportError(errorMessage(error));
        }
        return;
      }
      case "interaction.resolve":
        if (!dependencies.plugins.activeTask) return;
        try {
          await dependencies.service.resolveInteraction(
            dependencies.plugins.activeTask,
            intent.decision,
          );
        } catch (error) {
          dependencies.reportError(errorMessage(error));
        }
        return;
      case "task.cancel":
        if (!dependencies.plugins.activeTask) return;
        try {
          await dependencies.service.cancelTask(dependencies.plugins.activeTask.taskId);
        } catch (error) {
          dependencies.reportError(errorMessage(error));
        }
    }
  }

  return {
    theme,
    toolbarActions,
    rowMenuOptions,
    columnMenuOptions,
    rowMenu,
    columnMenu,
    dispatch,
  };
}
