import type { HostBridge } from "@/bridge/hostBridge";
import type { InsertRowResult, UpdateCellResult } from "@/contracts";
import type {
  DataBinding,
  InterfaceAction,
  InterfaceDefinition,
  InterfaceElement,
} from "@/contracts/generated/workbench";
import type { DashboardSchemaCatalog } from "@/services/dashboardBindingPorts";
import type { ActionRuntimePorts } from "./surfaceCore";

export interface SurfaceBindingData {
  readonly state: "loading" | "ready" | "failed";
  readonly rows: readonly Readonly<Record<string, unknown>>[];
  readonly offset: number;
  readonly filteredRows: number;
  readonly error: string | null;
}

export interface SurfaceBindingReadResult {
  readonly rows: readonly Readonly<Record<string, unknown>>[];
  readonly offset: number;
  readonly filteredRows: number;
}

export interface SurfaceBindingReader {
  read(
    binding: DataBinding,
    signal: AbortSignal,
  ): Promise<SurfaceBindingReadResult>;
}

export class SurfaceRuntimeController {
  data: Readonly<Record<string, SurfaceBindingData>> = {};
  activePageId: string | null = null;
  private controller: AbortController | null = null;

  constructor(private readonly reader: SurfaceBindingReader) {}

  async activate(
    definition: InterfaceDefinition,
    pageId: string,
    onReady?: (binding: DataBinding, result: SurfaceBindingData) => void,
  ): Promise<void> {
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    this.activePageId = pageId;
    const page = definition.pages.find((item) => item.pageId === pageId);
    if (!page) {
      this.data = {};
      return;
    }
    const visibleBindings = collectPageBindingIds(page.elements);
    const bindings = definition.bindings.filter((binding) => visibleBindings.has(binding.bindingId));
    this.data = Object.fromEntries(bindings.map((binding) => [
      binding.bindingId,
      { state: "loading", rows: [], offset: 0, filteredRows: 0, error: null },
    ]));
    const layers = bindingDependencyLayers(bindings);
    if (!layers) {
      this.data = Object.fromEntries(bindings.map((binding) => [binding.bindingId, {
        state: "failed", rows: [], offset: 0, filteredRows: 0,
        error: "surface.binding_variable_cycle",
      }]));
      return;
    }
    for (const layer of layers) {
      await Promise.all(layer.map(async (binding) => {
        try {
          const result = await this.reader.read(binding, controller.signal);
          if (this.controller !== controller || controller.signal.aborted) return;
          const ready: SurfaceBindingData = { state: "ready", ...result, error: null };
          this.data = { ...this.data, [binding.bindingId]: ready };
          onReady?.(binding, ready);
        } catch (error) {
          if (controller.signal.aborted) return;
          this.data = {
            ...this.data,
            [binding.bindingId]: {
              state: "failed", rows: [], offset: 0, filteredRows: 0, error: message(error),
            },
          };
        }
      }));
    }
  }

  dispose(): void {
    this.controller?.abort();
    this.controller = null;
    this.data = {};
  }
}

export function bindingDependencyLayers(
  bindings: readonly DataBinding[],
): readonly (readonly DataBinding[])[] | null {
  const byId = new Map(bindings.map((binding) => [binding.bindingId, binding]));
  const remaining = new Map(bindings.map((binding) => [
    binding.bindingId,
    new Set(binding.variables.flatMap((variable) =>
      variable.source === "selectedRecordField"
      && variable.sourceBindingId
      && byId.has(variable.sourceBindingId)
        ? [variable.sourceBindingId]
        : [])),
  ]));
  const layers: DataBinding[][] = [];
  while (remaining.size > 0) {
    const ready = [...remaining.entries()]
      .filter(([, dependencies]) => dependencies.size === 0)
      .map(([bindingId]) => byId.get(bindingId)!)
      .sort((left, right) => left.bindingId.localeCompare(right.bindingId));
    if (ready.length === 0) return null;
    layers.push(ready);
    for (const binding of ready) remaining.delete(binding.bindingId);
    for (const dependencies of remaining.values()) {
      for (const binding of ready) dependencies.delete(binding.bindingId);
    }
  }
  return layers;
}

export function collectPageBindingIds(elements: readonly InterfaceElement[]): Set<string> {
  const ids = new Set<string>();
  const visit = (element: InterfaceElement): void => {
    if (element.bindingId) ids.add(element.bindingId);
    element.children.forEach(visit);
  };
  elements.forEach(visit);
  return ids;
}

export interface SurfacePluginActionAdapter {
  run(
    pluginId: string,
    actionId: string,
    values: Readonly<Record<string, unknown>>,
    signal: AbortSignal,
  ): Promise<unknown>;
}

export class HostSurfaceActionPorts implements ActionRuntimePorts {
  constructor(
    private readonly definition: () => InterfaceDefinition,
    private readonly bridge: HostBridge,
    private readonly schemaCatalog: DashboardSchemaCatalog,
    private readonly refresh: (bindingId: string) => Promise<unknown>,
    private readonly navigateTo: (pageId: string) => Promise<unknown>,
    private readonly plugin: SurfacePluginActionAdapter,
    private readonly confirmAction: (action: InterfaceAction) => Promise<boolean>,
  ) {}

  async createRecord(
    bindingId: string,
    values: Readonly<Record<string, unknown>>,
    signal: AbortSignal,
  ): Promise<InsertRowResult> {
    const binding = this.binding(bindingId);
	const schema = await this.schemaCatalog.describe(binding.query.tableId, signal);
    signal.throwIfAborted();
    return await this.bridge.request("table.insertRowRequested", {
		table: binding.query.tableId,
		values: pick(values, binding.query.fields),
      schemaRevision: schema.revision,
    }) as InsertRowResult;
  }

  async updateRecord(
    bindingId: string,
    recordId: string | number,
    values: Readonly<Record<string, unknown>>,
    signal: AbortSignal,
  ): Promise<Readonly<Record<string, unknown>>> {
    const binding = this.binding(bindingId);
	const schema = await this.schemaCatalog.describe(binding.query.tableId, signal);
    const original = values.__surfaceOriginal;
    let current = isRecord(original) ? { ...original } : { ...values };
    let digest = text(current.__vibetableDigest);
    if (!digest) throw productError("surface.record_digest_required", "Record revision is missing.");
	for (const field of binding.query.fields) {
      signal.throwIfAborted();
      const nextValue = values[field];
      const result = await this.bridge.request("table.updateCellRequested", {
			table: binding.query.tableId,
        rowKey: recordId,
        column: field,
        oldValue: current[field],
        newValue: nextValue,
        expectedDigest: digest,
        schemaRevision: schema.revision,
      }) as UpdateCellResult;
      current = { ...result.currentRow };
      digest = text(current.__vibetableDigest);
		if (!digest && field !== binding.query.fields.at(-1)) {
        throw productError("surface.record_digest_required", "Updated record revision is missing.");
      }
    }
    return current;
  }

  refreshBinding(bindingId: string, signal: AbortSignal): Promise<unknown> {
    signal.throwIfAborted();
    return this.refresh(bindingId);
  }

  navigate(pageId: string, signal: AbortSignal): Promise<unknown> {
    signal.throwIfAborted();
    return this.navigateTo(pageId);
  }

  runPluginAction(
    pluginId: string,
    actionId: string,
    values: Readonly<Record<string, unknown>>,
    signal: AbortSignal,
  ): Promise<unknown> {
    return this.plugin.run(pluginId, actionId, values, signal);
  }

  confirm(action: InterfaceAction): Promise<boolean> {
    return this.confirmAction(action);
  }

  private binding(bindingId: string): DataBinding {
    const binding = this.definition().bindings.find((item) => item.bindingId === bindingId);
    if (!binding) throw productError("surface.binding_missing", "Binding does not exist.");
    return binding;
  }
}

function pick(
  values: Readonly<Record<string, unknown>>,
  fields: readonly string[],
): Readonly<Record<string, unknown>> {
  return Object.fromEntries(fields.filter((field) => field in values).map((field) => [field, values[field]]));
}

function text(value: unknown): string | null {
  return typeof value === "string" && value ? value : null;
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function productError(code: string, messageValue: string): Error & { code: string } {
  return Object.assign(new Error(messageValue), { code });
}
