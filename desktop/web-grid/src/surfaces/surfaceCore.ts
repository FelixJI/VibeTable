import type {
  InterfaceAction,
  InterfaceCommitRequest,
  InterfaceDefinition,
  InterfaceElement,
  InterfacePage,
  InterfaceSnapshot,
} from "@/contracts/generated/workbench";
import type { DomainDiagnostic } from "@/dashboard";

export interface SurfaceListEntry {
  readonly interfaceId: string;
  readonly name: string;
  readonly revision: string;
}

export interface SurfaceRepository {
  list(signal: AbortSignal): Promise<readonly SurfaceListEntry[]>;
  load(interfaceId: string, signal: AbortSignal): Promise<InterfaceSnapshot>;
  commit(request: InterfaceCommitRequest, signal: AbortSignal): Promise<InterfaceSnapshot>;
  delete(interfaceId: string, expectedRevision: string, signal: AbortSignal): Promise<void>;
}

export class InMemorySurfaceRepository implements SurfaceRepository {
  private readonly values = new Map<string, InterfaceSnapshot>();

  constructor(initial: readonly InterfaceSnapshot[] = []) {
    for (const snapshot of initial) this.values.set(snapshot.definition.interfaceId, clone(snapshot));
  }

  async list(signal: AbortSignal): Promise<readonly SurfaceListEntry[]> {
    signal.throwIfAborted();
    return [...this.values.values()].map((snapshot) => ({
      interfaceId: snapshot.definition.interfaceId,
      name: snapshot.definition.name,
      revision: snapshot.revision,
    })).sort((left, right) => left.name.localeCompare(right.name));
  }

  async load(interfaceId: string, signal: AbortSignal): Promise<InterfaceSnapshot> {
    signal.throwIfAborted();
    const value = this.values.get(interfaceId);
    if (!value) throw productError("surface.not_found", "Interface not found.");
    return clone(value);
  }

  async commit(request: InterfaceCommitRequest, signal: AbortSignal): Promise<InterfaceSnapshot> {
    signal.throwIfAborted();
    const diagnostics = validateInterfaceDefinition(request.definition);
    if (diagnostics.length > 0) throw productError("surface.definition_invalid", diagnostics[0]!.message);
    const current = this.values.get(request.definition.interfaceId);
    if ((current?.revision ?? null) !== request.expectedRevision) {
      throw productError("surface.edit_conflict", "Interface changed elsewhere.");
    }
    const revision = `surface_${revisionNumber(current?.revision) + 1}`;
    const result = { definition: clone(request.definition), revision };
    this.values.set(request.definition.interfaceId, result);
    return clone(result);
  }

  async delete(interfaceId: string, expectedRevision: string, signal: AbortSignal): Promise<void> {
    signal.throwIfAborted();
    const current = this.values.get(interfaceId);
    if (!current) throw productError("surface.not_found", "Interface not found.");
    if (current.revision !== expectedRevision) throw productError("surface.edit_conflict", "Interface changed elsewhere.");
    this.values.delete(interfaceId);
  }
}

export interface SurfaceRuntimeContext {
  readonly activePageId?: string | null;
  readonly selectedRecordId?: string | null;
  readonly filterValues?: Readonly<Record<string, unknown>>;
}

export interface SurfaceEditorState {
  readonly phase: "idle" | "loading" | "ready" | "saving" | "conflict" | "failed";
  readonly draft: InterfaceDefinition | null;
  readonly revision: string | null;
  readonly dirty: boolean;
  readonly error: string | null;
  readonly diagnostics: readonly DomainDiagnostic[];
}

export type SurfaceEditorCommand =
  | { readonly type: "rename"; readonly name: string }
  | { readonly type: "replace"; readonly definition: InterfaceDefinition }
  | { readonly type: "add-page"; readonly page: InterfacePage }
  | { readonly type: "remove-page"; readonly pageId: string };

export class SurfaceEditorController {
  state: SurfaceEditorState = {
    phase: "idle", draft: null, revision: null, dirty: false, error: null, diagnostics: [],
  };
  runtimeContext: SurfaceRuntimeContext = {};
  private interfaceId: string | null = null;
  private controller: AbortController | null = null;

  constructor(private readonly repository: SurfaceRepository) {}

  start(definition: InterfaceDefinition): void {
    this.controller?.abort();
    this.controller = null;
    this.interfaceId = definition.interfaceId;
    this.state = {
      phase: "ready",
      draft: clone(definition),
      revision: null,
      dirty: true,
      error: null,
      diagnostics: validateInterfaceDefinition(definition),
    };
    this.runtimeContext = { activePageId: definition.pages[0]?.pageId ?? null };
  }

  async open(interfaceId: string): Promise<void> {
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    this.interfaceId = interfaceId;
    this.state = { ...this.state, phase: "loading", error: null };
    try {
      const snapshot = await this.repository.load(interfaceId, controller.signal);
      if (this.controller !== controller) return;
      this.state = {
        phase: "ready", draft: clone(snapshot.definition), revision: snapshot.revision,
        dirty: false, error: null, diagnostics: [],
      };
      this.runtimeContext = { activePageId: snapshot.definition.pages[0]?.pageId ?? null };
    } catch (error) {
      if (controller.signal.aborted) return;
      this.state = { ...this.state, phase: "failed", error: errorMessage(error) };
    }
  }

  async reload(): Promise<void> {
    if (this.interfaceId) await this.open(this.interfaceId);
  }

  dispatch(command: SurfaceEditorCommand): void {
    const draft = this.state.draft;
    if (!draft) return;
    let next: InterfaceDefinition;
    switch (command.type) {
      case "rename":
        next = { ...draft, name: command.name };
        break;
      case "replace":
        next = clone(command.definition);
        break;
      case "add-page":
        next = { ...draft, pages: [...draft.pages, clone(command.page)] };
        break;
      case "remove-page":
        next = { ...draft, pages: draft.pages.filter((page) => page.pageId !== command.pageId) };
        break;
    }
    this.state = {
      ...this.state,
      phase: "ready",
      draft: next,
      dirty: true,
      error: null,
      diagnostics: validateInterfaceDefinition(next),
    };
  }

  setRuntimeContext(context: SurfaceRuntimeContext): void {
    this.runtimeContext = clone(context);
  }

  canClose(confirmDiscard: () => boolean): boolean {
    return !this.state.dirty || confirmDiscard();
  }

  async save(idempotencyKey: string): Promise<void> {
    const draft = this.state.draft;
    if (!draft || !this.state.dirty) return;
    const diagnostics = validateInterfaceDefinition(draft);
    if (diagnostics.length > 0) {
      this.state = { ...this.state, phase: "failed", diagnostics, error: diagnostics[0]!.message };
      return;
    }
    const controller = new AbortController();
    this.controller = controller;
    this.state = { ...this.state, phase: "saving", error: null };
    try {
      const snapshot = await this.repository.commit({
        definition: clone(draft), expectedRevision: this.state.revision, idempotencyKey,
      }, controller.signal);
      if (this.controller !== controller) return;
      this.state = {
        phase: "ready", draft: clone(snapshot.definition), revision: snapshot.revision,
        dirty: false, error: null, diagnostics: [],
      };
    } catch (error) {
      if (controller.signal.aborted) return;
      this.state = {
        ...this.state,
        phase: errorCode(error) === "surface.edit_conflict" ? "conflict" : "failed",
        error: errorMessage(error),
      };
    }
  }
}

export interface ActionRuntimeContext {
  readonly definition: InterfaceDefinition;
  readonly values: Readonly<Record<string, unknown>>;
  readonly recordId?: string | number | null;
}

export interface ActionRuntimePorts {
  createRecord(bindingId: string, values: Readonly<Record<string, unknown>>, signal: AbortSignal): Promise<unknown>;
  updateRecord(bindingId: string, recordId: string | number, values: Readonly<Record<string, unknown>>, signal: AbortSignal): Promise<unknown>;
  refreshBinding(bindingId: string, signal: AbortSignal): Promise<unknown>;
  navigate(pageId: string, signal: AbortSignal): Promise<unknown>;
  runPluginAction(pluginId: string, actionId: string, values: Readonly<Record<string, unknown>>, signal: AbortSignal): Promise<unknown>;
  confirm(action: InterfaceAction): Promise<boolean>;
}

export type ActionResult =
  | { readonly state: "succeeded"; readonly value: unknown }
  | { readonly state: "rejected" }
  | { readonly state: "cancelled" }
  | { readonly state: "failed"; readonly error: { readonly code: string; readonly message: string } };

export class ActionRuntime {
  constructor(private readonly ports: ActionRuntimePorts) {}

  describe(definition: InterfaceDefinition): readonly InterfaceAction[] {
    return definition.actions.map(clone);
  }

  async execute(action: InterfaceAction, context: ActionRuntimeContext, signal: AbortSignal): Promise<ActionResult> {
    try {
      signal.throwIfAborted();
      if (action.requiresConfirmation && !await this.ports.confirm(action)) return { state: "rejected" };
      signal.throwIfAborted();
      let value: unknown;
      switch (action.kind) {
        case "record.create":
          value = await this.ports.createRecord(required(action.bindingId, "bindingId"), context.values, signal);
          break;
        case "record.update":
          value = await this.ports.updateRecord(
            required(action.bindingId, "bindingId"), required(context.recordId, "recordId"), context.values, signal,
          );
          break;
        case "binding.refresh":
          value = await this.ports.refreshBinding(required(action.bindingId, "bindingId"), signal);
          break;
        case "navigate":
          value = await this.ports.navigate(required(action.targetPageId, "targetPageId"), signal);
          break;
        case "plugin":
          value = await this.ports.runPluginAction(
            required(action.pluginId, "pluginId"), required(action.pluginActionId, "pluginActionId"), context.values, signal,
          );
          break;
        default:
          return { state: "failed", error: { code: "surface.action_unknown", message: "Action kind is not supported." } };
      }
      signal.throwIfAborted();
      return { state: "succeeded", value };
    } catch (error) {
      if (signal.aborted || isAbortError(error)) return { state: "cancelled" };
      return { state: "failed", error: { code: errorCode(error), message: errorMessage(error) } };
    }
  }
}

export function validateInterfaceDefinition(definition: InterfaceDefinition): DomainDiagnostic[] {
  const diagnostics: DomainDiagnostic[] = [];
  const bindings = uniqueIds(definition.bindings.map((item) => item.bindingId), "bindings", diagnostics);
  const pages = uniqueIds(definition.pages.map((item) => item.pageId), "pages", diagnostics);
  const actions = uniqueIds(definition.actions.map((item) => item.actionId), "actions", diagnostics);
  const elementIds = new Set<string>();
  let elementCount = 0;
  for (const [pageIndex, page] of definition.pages.entries()) {
    for (const [elementIndex, element] of page.elements.entries()) {
      visitElement(element, `pages.${pageIndex}.elements.${elementIndex}`, 1);
    }
  }
  for (const [index, action] of definition.actions.entries()) {
    const path = `actions.${index}`;
    if (!["record.create", "record.update", "binding.refresh", "navigate", "plugin"].includes(action.kind)) {
      diagnostics.push(error("surface.action_unknown", "Action kind is not supported.", `${path}.kind`));
      continue;
    }
    if (["record.create", "record.update", "binding.refresh"].includes(action.kind) &&
        (!action.bindingId || !bindings.has(action.bindingId))) {
      diagnostics.push(error("surface.binding_missing", "Action binding does not exist.", `${path}.bindingId`));
    }
    if (action.kind === "navigate" && (!action.targetPageId || !pages.has(action.targetPageId))) {
      diagnostics.push(error("surface.page_missing", "Navigation target page does not exist.", `${path}.targetPageId`));
    }
    if (action.kind === "plugin" && (!action.pluginId || !action.pluginActionId)) {
      diagnostics.push(error("surface.plugin_action_invalid", "Plugin action identity is incomplete.", path));
    }
  }
  return diagnostics;

  function visitElement(element: InterfaceElement, path: string, depth: number): void {
    elementCount += 1;
    if (elementCount > 200) diagnostics.push(error("surface.element_limit", "An interface can contain at most 200 elements.", path));
    if (depth > 8) diagnostics.push(error("surface.element_depth", "Element nesting cannot exceed 8 levels.", path));
    if (elementIds.has(element.elementId)) diagnostics.push(error("surface.element_duplicate", "Element IDs must be unique.", `${path}.elementId`));
    elementIds.add(element.elementId);
    const kind = element.kind as string;
    const known = ["section", "columns", "tabs", "text", "metric", "chart", "record-list", "record-detail", "form", "button", "navigation"];
    if (!known.includes(kind)) diagnostics.push(error("surface.element_unknown", "Element kind is not supported.", `${path}.kind`));
    if (element.bindingId && !bindings.has(element.bindingId)) {
      diagnostics.push(error("surface.binding_missing", "Element binding does not exist.", `${path}.bindingId`));
    }
    if (element.actionId && !actions.has(element.actionId)) {
      diagnostics.push(error("surface.action_missing", "Element action does not exist.", `${path}.actionId`));
    }
    const structural = ["section", "columns", "tabs"].includes(kind);
    if (!structural && element.children.length > 0) {
      diagnostics.push(error("surface.children_invalid", "Only structural elements can contain children.", `${path}.children`));
    }
    element.children.forEach((child, index) => visitElement(child, `${path}.children.${index}`, depth + 1));
  }
}

function uniqueIds(values: readonly string[], path: string, diagnostics: DomainDiagnostic[]): Set<string> {
  const result = new Set<string>();
  values.forEach((value, index) => {
    if (result.has(value)) diagnostics.push(error("surface.id_duplicate", "IDs must be unique.", `${path}.${index}`));
    result.add(value);
  });
  return result;
}

function error(code: string, message: string, path: string): DomainDiagnostic {
  return { code, message, path, severity: "error" };
}
function revisionNumber(value: string | undefined): number {
  const parsed = Number(value?.replace(/^surface_/, "") ?? 0);
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : 0;
}
function required<T extends string | number>(value: T | null | undefined, path: string): T {
  if (!value) throw productError("surface.action_invalid", `${path} is required.`);
  return value;
}
function productError(code: string, message: string): Error & { code: string } {
  return Object.assign(new Error(message), { code });
}
function errorCode(error: unknown): string {
  return typeof error === "object" && error !== null && "code" in error && typeof error.code === "string"
    ? error.code : "surface.operation_failed";
}
function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
function isAbortError(error: unknown): boolean { return error instanceof DOMException && error.name === "AbortError"; }
function clone<T>(value: T): T { return structuredClone(value); }
