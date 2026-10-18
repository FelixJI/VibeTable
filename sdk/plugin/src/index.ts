export type {
  CapabilityAdapter,
  DataPage,
  DataReadRequest,
  MutationOperation,
  MutationPlan,
  MutationResult,
  PluginCapabilities,
  PluginProgress,
  ProgressReceipt,
  ReadGrant,
  WriteGrant,
} from "./capabilities.js";
export { createCapabilityClient, mutationPlan, PluginCapabilityError } from "./capabilities.js";
export { createSurfaceClient, finishHeadlessRun, SURFACE_API_CONTRACT } from "./surface.js";
export type { SurfaceCapabilities, SurfaceIoTask, SurfaceIoStatus, SurfaceImportPreview, SurfaceAttachment, SurfaceAttachmentTarget, SurfaceSubscription, SurfaceDataEvent, SurfaceMutationPlan, SurfaceMutationPreview, SurfaceMutationOutcome, SurfaceTransport } from "./surface.js";
export { cancelled, failure, ok } from "./results.js";
export { defineSchema } from "./schema.js";
export type { JsonSchema } from "./schema.js";
export type {
  CommandContext,
  Density,
  JsonObject,
  JsonPrimitive,
  JsonValue,
  PluginAction,
  PluginActionReturn,
  PluginReadAction,
  PluginWriteAction,
  LegacyCancellationSnapshot,
  PluginFailure,
  PluginIntent,
  PluginResult,
  PluginSuccess,
  ThemeMode,
} from "./types.js";

export type { DataOperator, DataFilter, DataSort, DataField, DataTable, DataCatalog, DataDescription, DataDescribeRequest, DataQueryRequest, DataQueryPage, ComputedCell } from "./data.js";

export { describeCellValue, formatCellValue, isMissingCell } from "./display.js";
export type { CellDisplay, CellValueKind } from "./display.js";

export { SURFACE_UI_METHODS, SURFACE_UI_UNSUPPORTED } from "./surface-ui.js";
export type { SurfacePresentation, SurfaceTask, SurfaceParentTransport } from "./surface-ui.js";
