/**
 * fieldHistoryFlow — G1 full-field history UI state machine.
 *
 * Manages the lifecycle of the field history panel:
 *   idle → loading → loaded (with ChangeSets timeline)
 *   loaded → previewing → previewReady (restore diagnostics)
 *   previewReady → applying → applied (or conflict/error)
 *
 * The flow communicates with the host via HostBridge:
 *   - history.readRequested → history.loaded
 *   - history.previewRestoreRequested → history.restorePreviewReady
 *   - history.applyRestoreRequested → history.restoreApplied
 *
 * On restore success the caller should refresh the current table row and
 * re-read history to show the new restore revision.
 */

import type {
  HistoryApplyRestorePayload,
  HistoryPage,
  HistoryPreviewRestorePayload,
  HistoryReadPayload,
  RestorePreview,
  RestoreResult,
} from "./contracts";

export type FieldHistoryState =
  | "idle"
  | "loading"
  | "loaded"
  | "error"
  | "previewing"
  | "previewReady"
  | "applying"
  | "applied"
  | "conflict";

export interface FieldHistoryModel {
  readonly state: FieldHistoryState;
  readonly collection: string;
  readonly itemId: string;
  readonly page: HistoryPage | null;
  readonly preview: RestorePreview | null;
  readonly result: RestoreResult | null;
  readonly errorMessage: string | null;
}

export const initialFieldHistoryModel: FieldHistoryModel = {
  state: "idle",
  collection: "",
  itemId: "",
  page: null,
  preview: null,
  result: null,
  errorMessage: null,
};

export interface FieldHistoryRequesters {
  readonly readHistory: (payload: HistoryReadPayload) => Promise<HistoryPage>;
  readonly previewRestore: (payload: HistoryPreviewRestorePayload) => Promise<RestorePreview>;
  readonly applyRestore: (payload: HistoryApplyRestorePayload) => Promise<RestoreResult>;
}

/** Transition: start loading ChangeSets for (collection, itemId). */
export function startRead(
  model: FieldHistoryModel,
  collection: string,
  itemId: string,
): FieldHistoryModel {
  return {
    ...model,
    state: "loading",
    collection,
    itemId,
    page: null,
    preview: null,
    result: null,
    errorMessage: null,
  };
}

/** Transition: history.loaded arrived. */
export function loaded(model: FieldHistoryModel, page: HistoryPage): FieldHistoryModel {
  return { ...model, state: "loaded", page, errorMessage: null };
}

/** Transition: error occurred. */
export function failed(model: FieldHistoryModel, message: string): FieldHistoryModel {
  return { ...model, state: "error", errorMessage: message };
}

/** Transition: start previewing a restore. */
export function startPreview(
  model: FieldHistoryModel,
  _targetRevision: string,
): FieldHistoryModel {
  if (model.state !== "loaded") {
    return failed(model, "Cannot preview restore without loaded history.");
  }
  return { ...model, state: "previewing", preview: null };
}

/** Transition: restore preview ready. */
export function previewReady(
  model: FieldHistoryModel,
  preview: RestorePreview,
): FieldHistoryModel {
  // If any diagnostic has severity "error", the restore is blocked.
  const hasErrors = preview.diagnostics.some((d) => d.severity === "error");
  return {
    ...model,
    state: hasErrors ? "error" : "previewReady",
    preview,
    errorMessage: hasErrors
      ? preview.diagnostics.filter((d) => d.severity === "error").map((d) => d.message).join("; ")
      : null,
  };
}

/** Transition: start applying a restore. */
export function startApply(model: FieldHistoryModel): FieldHistoryModel {
  if (model.state !== "previewReady" || !model.preview) {
    return failed(model, "Cannot apply restore without a valid preview.");
  }
  return { ...model, state: "applying" };
}

/** Transition: restore applied. */
export function applied(model: FieldHistoryModel, result: RestoreResult): FieldHistoryModel {
  return { ...model, state: "applied", result, errorMessage: null };
}

/** Transition: restore conflict (item changed since preview). */
export function conflict(model: FieldHistoryModel, message: string): FieldHistoryModel {
  return { ...model, state: "conflict", errorMessage: message };
}

/** Reset to idle. */
export function reset(_model: FieldHistoryModel): FieldHistoryModel {
  return { ...initialFieldHistoryModel };
}

// ---------------------------------------------------------------------------
// Async orchestration (uses the requesters to drive transitions)
// ---------------------------------------------------------------------------

export async function loadHistory(
  model: FieldHistoryModel,
  requesters: FieldHistoryRequesters,
  collection: string,
  itemId: string,
  limit = 50,
  offset = 0,
): Promise<FieldHistoryModel> {
  const loading = startRead(model, collection, itemId);
  try {
    const page = await requesters.readHistory({ collection, itemId, limit, offset });
    return loaded(loading, page);
  } catch (err) {
    return failed(loading, err instanceof Error ? err.message : String(err));
  }
}

export async function requestPreview(
  model: FieldHistoryModel,
  requesters: FieldHistoryRequesters,
  targetRevision: string,
): Promise<FieldHistoryModel> {
  const previewing = startPreview(model, targetRevision);
  if (previewing.state === "error") {
    return previewing;
  }
  try {
    const preview = await requesters.previewRestore({
      collection: model.collection,
      itemId: model.itemId,
      targetRevision,
    });
    return previewReady(previewing, preview);
  } catch (err) {
    return failed(previewing, err instanceof Error ? err.message : String(err));
  }
}

export async function confirmRestore(
  model: FieldHistoryModel,
  requesters: FieldHistoryRequesters,
): Promise<FieldHistoryModel> {
  const applying = startApply(model);
  if (applying.state === "error") {
    return applying;
  }
  if (!model.preview) {
    return failed(model, "No preview token available.");
  }
  try {
    const result = await requesters.applyRestore({
      collection: model.collection,
      itemId: model.itemId,
      token: model.preview.token,
    });
    return applied(applying, result);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (message.includes("conflict") || message.includes("changed")) {
      return conflict(applying, message);
    }
    return failed(applying, message);
  }
}
