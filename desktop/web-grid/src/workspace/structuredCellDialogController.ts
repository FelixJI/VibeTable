import { reactive, readonly } from "vue";

import {
  BridgeOperationError,
  BridgeTimeoutError,
  type HostBridge,
} from "@/bridge/hostBridge";
import type {
  AttachmentListResult,
  AttachmentDownloadResult,
  AttachmentPolicy,
  AttachmentPreviewResult,
  ColumnSchema,
  ManagedAttachmentRef,
} from "@/contracts";
import {
  type StructuredDialogFocus,
  type StructuredDialogFocusLease,
} from "@/services/dialogFocus";

export interface AttachmentAuthority {
  readonly tableId: string | null;
  readonly schemaRevision: string | null;
  readonly expectedDigest: string | null;
}

export interface StructuredCellDialogState {
  readonly attachment: {
    readonly show: boolean;
    readonly rowKey: string | number;
    readonly column: ColumnSchema | null;
    readonly policy: AttachmentPolicy | null;
    readonly files: readonly ManagedAttachmentRef[];
    readonly loading: boolean;
    readonly error: string | null;
  };
  readonly json: {
    readonly show: boolean;
    readonly rowKey: string | number;
    readonly column: ColumnSchema | null;
    readonly originalValue: unknown;
    readonly expectedDigest: string | null;
    readonly value: unknown;
    readonly valid: boolean;
  };
}

interface MutableDialogState {
  attachment: {
    show: boolean;
    rowKey: string | number;
    column: ColumnSchema | null;
    policy: AttachmentPolicy | null;
    files: ManagedAttachmentRef[];
    loading: boolean;
    error: string | null;
  };
  json: {
    show: boolean;
    rowKey: string | number;
    column: ColumnSchema | null;
    originalValue: unknown;
    expectedDigest: string | null;
    value: unknown;
    valid: boolean;
  };
}

export type StructuredCellDialogIntent =
  | {
    readonly type: "attachment.open";
    readonly rowKey: string | number;
    readonly column: ColumnSchema;
    readonly trigger?: HTMLElement | null;
  }
  | { readonly type: "attachment.close" }
  | { readonly type: "attachment.upload" }
  | { readonly type: "attachment.replace"; readonly storedName: string }
  | { readonly type: "attachment.remove"; readonly storedName: string }
  | { readonly type: "attachment.preview"; readonly storedName: string }
  | { readonly type: "attachment.download"; readonly storedName: string }
  | {
    readonly type: "json.open";
    readonly rowKey: string | number;
    readonly column: ColumnSchema;
    readonly value: unknown;
    readonly expectedDigest: string | null;
    readonly trigger?: HTMLElement | null;
  }
  | { readonly type: "json.change"; readonly value: unknown }
  | { readonly type: "json.validity"; readonly valid: boolean }
  | { readonly type: "json.save" }
  | { readonly type: "json.close" };

export type StructuredCellDialogKind = "attachment" | "json";

const structuredDialogCloseLeaseBrand: unique symbol = Symbol("structured-dialog-close-lease");

export interface StructuredDialogCloseLease {
  readonly [structuredDialogCloseLeaseBrand]: true;
  release(): void;
}

export interface StructuredCellDialogController {
  readonly state: StructuredCellDialogState;
  dispatch(intent: StructuredCellDialogIntent): Promise<void>;
  claimCloseLease(dialog: StructuredCellDialogKind): StructuredDialogCloseLease | null;
}

export type StructuredCellBridge = Pick<HostBridge, "request">;

export interface StructuredCellDialogDependencies {
  readonly bridge: StructuredCellBridge;
  readonly resolveAttachmentAuthority: (rowKey: string | number) => AttachmentAuthority;
  readonly commitJson: (edit: {
    readonly rowKey: string | number;
    readonly field: string;
    readonly originalValue: unknown;
    readonly value: unknown;
    readonly expectedDigest: string | null;
  }) => void;
  readonly dialogFocus: StructuredDialogFocus;
  readonly translate: (key: string) => string;
  readonly reportError: (message: string) => void;
  readonly activeElement?: () => HTMLElement | null;
}

function currentActiveElement(): HTMLElement | null {
  return document.activeElement instanceof HTMLElement ? document.activeElement : null;
}

function attachmentErrorMessage(
  error: unknown,
  translate: (key: string) => string,
): string {
  if (error instanceof BridgeTimeoutError) {
    return translate("workspace.attachment.error.timeout");
  }
  if (error instanceof BridgeOperationError) {
    switch (error.code) {
      case "CANCELLED":
        return translate("workspace.attachment.error.cancelled");
      case "ATTACHMENT_CONTEXT_INVALID":
      case "edit_conflict":
        return translate("workspace.attachment.staleRow");
      case "ATTACHMENT_UPLOAD_OBJECTS_MISSING":
      case "ATTACHMENT_REPLACE_INVALID":
      case "NATIVE_OBJECTS_UNAVAILABLE":
        return translate("workspace.attachment.error.picker");
      default:
        return translate("workspace.attachment.error.operation");
    }
  }
  if (
    error instanceof Error
    && error.message === translate("workspace.attachment.invalidResponse")
  ) {
    return error.message;
  }
  return translate("workspace.attachment.error.generic");
}

function hasOnlyKeys(
  value: Record<string, unknown>,
  keys: readonly string[],
): boolean {
  const actual = Object.keys(value);
  return actual.length === keys.length && actual.every(key => keys.includes(key));
}

function isAttachmentPreviewResult(value: unknown): value is AttachmentPreviewResult {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const result = value as Record<string, unknown>;
  if (!hasOnlyKeys(result, ["outcome", "reason"])) return false;
  return (result.outcome === "opened" && result.reason === null)
    || (result.outcome === "unavailable"
      && result.reason === "PREVIEW_HANDLER_UNAVAILABLE");
}

function isAttachmentDownloadResult(value: unknown): value is AttachmentDownloadResult {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const result = value as Record<string, unknown>;
  return hasOnlyKeys(result, ["outcome"])
    && (result.outcome === "saved" || result.outcome === "cancelled");
}

export function createStructuredCellDialogController(
  dependencies: StructuredCellDialogDependencies,
): StructuredCellDialogController {
  const state = reactive<MutableDialogState>({
    attachment: {
      show: false,
      rowKey: "",
      column: null,
      policy: null,
      files: [],
      loading: false,
      error: null,
    },
    json: {
      show: false,
      rowKey: "",
      column: null,
      originalValue: null,
      expectedDigest: null,
      value: null,
      valid: true,
    },
  });
  let attachmentEpoch = 0;
  let attachmentActionEpoch = 0;
  const attachmentActionLeases = new Map<string, number>();
  let attachmentFocus: StructuredDialogFocusLease | null = null;
  let jsonFocus: StructuredDialogFocusLease | null = null;
  let attachmentCloseLease: StructuredDialogCloseLease | null = null;
  let jsonCloseLease: StructuredDialogCloseLease | null = null;

  const activeElement = (): HTMLElement | null =>
    dependencies.activeElement?.() ?? currentActiveElement();

  function createCloseLease(
    releaseCurrent: (lease: StructuredDialogCloseLease) => void,
  ): StructuredDialogCloseLease {
    let released = false;
    const lease: StructuredDialogCloseLease = {
      [structuredDialogCloseLeaseBrand]: true,
      release(): void {
        if (released) return;
        released = true;
        releaseCurrent(lease);
      },
    };
    return lease;
  }

  function closeAttachment(): void {
    if (!state.attachment.show) return;
    attachmentEpoch += 1;
    attachmentActionEpoch += 1;
    attachmentActionLeases.clear();
    state.attachment.show = false;
    attachmentCloseLease = createCloseLease((lease) => {
      if (attachmentCloseLease !== lease || state.attachment.show) return;
      attachmentCloseLease = null;
      const focus = attachmentFocus;
      attachmentFocus = null;
      focus?.restore();
    });
  }

  async function openAttachment(
    rowKey: string | number,
    column: ColumnSchema,
    triggerElement?: HTMLElement | null,
  ): Promise<void> {
    const policy = column.attachmentPolicy;
    const fieldId = column.fieldId;
    const authority = dependencies.resolveAttachmentAuthority(rowKey);
    if (!policy || !authority.tableId || !fieldId) {
      dependencies.reportError(dependencies.translate("workspace.attachment.invalidField"));
      return;
    }
    const focused = activeElement();
    const trigger = triggerElement ?? focused;
    attachmentCloseLease = null;
    attachmentFocus = dependencies.dialogFocus.capture({
      element: trigger,
      rowKey,
      field: column.name,
      target: "attachment",
    });
    focused?.blur();
    if (trigger !== focused) trigger?.blur();
    const epoch = ++attachmentEpoch;
    attachmentActionEpoch += 1;
    attachmentActionLeases.clear();
    Object.assign(state.attachment, {
      show: true,
      rowKey,
      column,
      policy,
      files: [],
      loading: true,
      error: null,
    });
    try {
      const result = await dependencies.bridge.request("file.list", {
        tableId: authority.tableId,
        recordId: String(rowKey),
        fieldId,
      }) as AttachmentListResult;
      if (!Array.isArray(result.attachments)) {
        throw new Error(dependencies.translate("workspace.attachment.invalidResponse"));
      }
      if (epoch !== attachmentEpoch || !state.attachment.show) return;
      state.attachment.files = [...result.attachments];
    } catch (error) {
      if (epoch !== attachmentEpoch || !state.attachment.show) return;
      state.attachment.error = attachmentErrorMessage(error, dependencies.translate);
    } finally {
      if (epoch === attachmentEpoch) state.attachment.loading = false;
    }
  }

  function actionContext(): {
    tableId: string;
    recordId: string;
    fieldId: string;
    schemaRevision: string;
    expectedDigest: string;
  } | null {
    const fieldId = state.attachment.column?.fieldId;
    const authority = dependencies.resolveAttachmentAuthority(state.attachment.rowKey);
    if (
      !authority.tableId
      || !fieldId
      || !authority.schemaRevision
      || !authority.expectedDigest
      || !/^sha256:[0-9a-f]{64}$/u.test(authority.expectedDigest)
    ) {
      state.attachment.error = dependencies.translate("workspace.attachment.staleRow");
      return null;
    }
    return {
      tableId: authority.tableId,
      recordId: String(state.attachment.rowKey),
      fieldId,
      schemaRevision: authority.schemaRevision,
      expectedDigest: authority.expectedDigest,
    };
  }

  async function mutateAttachment(
    type: "file.uploadRequested" | "file.replaceRequested" | "file.removeRequested",
    storedName?: string,
  ): Promise<void> {
    const context = actionContext();
    if (!context) return;
    const epoch = attachmentEpoch;
    state.attachment.loading = true;
    state.attachment.error = null;
    try {
      if (type === "file.uploadRequested") {
        await dependencies.bridge.request(type, context);
      } else if (type === "file.replaceRequested") {
        await dependencies.bridge.request(type, { ...context, storedName: storedName ?? "" });
      } else {
        await dependencies.bridge.request(type, { ...context, storedName: storedName ?? "" });
      }
      if (epoch !== attachmentEpoch || !state.attachment.show) return;
      if (type === "file.removeRequested") {
        state.attachment.files = state.attachment.files.filter(
          item => item.storedName !== storedName,
        );
      } else {
        closeAttachment();
      }
    } catch (error) {
      if (epoch !== attachmentEpoch || !state.attachment.show) return;
      state.attachment.error = attachmentErrorMessage(error, dependencies.translate);
    } finally {
      if (epoch === attachmentEpoch) state.attachment.loading = false;
    }
  }

  async function runNativeAttachmentAction(
    type: "file.previewRequested" | "file.downloadRequested",
    storedName: string,
  ): Promise<void> {
    const file = state.attachment.files.find(item => item.storedName === storedName);
    const fieldId = state.attachment.column?.fieldId;
    const rowKey = state.attachment.rowKey;
    const { tableId } = dependencies.resolveAttachmentAuthority(rowKey);
    if (!file || !tableId || !fieldId) return;
    const recordId = String(rowKey);
    const leaseKey = JSON.stringify([type, tableId, recordId, fieldId, storedName]);
    if (attachmentActionLeases.has(leaseKey)) return;
    const dialogEpoch = attachmentEpoch;
    const actionEpoch = ++attachmentActionEpoch;
    attachmentActionLeases.set(leaseKey, actionEpoch);
    const isCurrent = (): boolean =>
      dialogEpoch === attachmentEpoch
      && attachmentActionLeases.get(leaseKey) === actionEpoch
      && state.attachment.show
      && String(state.attachment.rowKey) === recordId
      && state.attachment.column?.fieldId === fieldId
      && dependencies.resolveAttachmentAuthority(state.attachment.rowKey).tableId === tableId
      && state.attachment.files.some(item => item.storedName === storedName);
    state.attachment.error = null;
    try {
      const result = await dependencies.bridge.request(type, {
        tableId,
        recordId,
        fieldId,
        storedName,
        originalName: file.originalName,
      });
      if (!isCurrent()) return;
      if (type === "file.previewRequested") {
        if (!isAttachmentPreviewResult(result)) {
          throw new Error(dependencies.translate("workspace.attachment.invalidResponse"));
        }
        if (result.outcome === "unavailable") {
          state.attachment.error = dependencies.translate(
            "workspace.attachment.previewUnavailable",
          );
          return;
        }
        return;
      }
      if (!isAttachmentDownloadResult(result)) {
        throw new Error(dependencies.translate("workspace.attachment.invalidResponse"));
      }
    } catch (error) {
      if (!isCurrent()) return;
      state.attachment.error = attachmentErrorMessage(error, dependencies.translate);
    } finally {
      if (attachmentActionLeases.get(leaseKey) === actionEpoch) {
        attachmentActionLeases.delete(leaseKey);
      }
    }
  }

  function openJson(intent: Extract<StructuredCellDialogIntent, { type: "json.open" }>): void {
    const focused = activeElement();
    jsonCloseLease = null;
    jsonFocus = dependencies.dialogFocus.capture({
      element: intent.trigger ?? focused,
      rowKey: intent.rowKey,
      field: intent.column.name,
      target: "json",
    });
    focused?.blur();
    Object.assign(state.json, {
      show: true,
      rowKey: intent.rowKey,
      column: intent.column,
      originalValue: intent.value,
      expectedDigest: intent.expectedDigest,
      value: intent.value,
      valid: true,
    });
  }

  function closeJson(): void {
    if (!state.json.show) return;
    state.json.show = false;
    jsonCloseLease = createCloseLease((lease) => {
      if (jsonCloseLease !== lease || state.json.show) return;
      jsonCloseLease = null;
      const focus = jsonFocus;
      jsonFocus = null;
      focus?.restore();
    });
  }

  function claimCloseLease(dialog: StructuredCellDialogKind): StructuredDialogCloseLease | null {
    if (dialog === "attachment") {
      return state.attachment.show ? null : attachmentCloseLease;
    }
    return state.json.show ? null : jsonCloseLease;
  }

  async function dispatch(intent: StructuredCellDialogIntent): Promise<void> {
    switch (intent.type) {
      case "attachment.open":
        await openAttachment(intent.rowKey, intent.column, intent.trigger);
        return;
      case "attachment.close":
        closeAttachment();
        return;
      case "attachment.upload":
        await mutateAttachment("file.uploadRequested");
        return;
      case "attachment.replace":
        await mutateAttachment("file.replaceRequested", intent.storedName);
        return;
      case "attachment.remove":
        await mutateAttachment("file.removeRequested", intent.storedName);
        return;
      case "attachment.preview":
        await runNativeAttachmentAction("file.previewRequested", intent.storedName);
        return;
      case "attachment.download":
        await runNativeAttachmentAction("file.downloadRequested", intent.storedName);
        return;
      case "json.open":
        openJson(intent);
        return;
      case "json.change":
        state.json.value = intent.value;
        return;
      case "json.validity":
        state.json.valid = intent.valid;
        return;
      case "json.save": {
        const json = state.json;
        if (!json.valid || !json.column) return;
        dependencies.commitJson({
          rowKey: json.rowKey,
          field: json.column.name,
          originalValue: json.originalValue,
          value: json.value,
          expectedDigest: json.expectedDigest,
        });
        closeJson();
        return;
      }
      case "json.close":
        closeJson();
        return;
    }
  }

  return {
    state: readonly(state) as StructuredCellDialogState,
    dispatch,
    claimCloseLease,
  };
}
