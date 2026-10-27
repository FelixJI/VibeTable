export { PluginCapabilityError } from "./capabilities.js";
export type { ReadGrant, WriteGrant } from "./capabilities.js";
export { createSurfaceClient, finishHeadlessRun, SURFACE_API_CONTRACT } from "./surface.js";
export type { SurfaceCapabilities, SurfaceIoTask, SurfaceIoStatus, SurfaceImportPreview, SurfaceAttachment, SurfaceAttachmentTarget, SurfaceSubscription, SurfaceDataEvent, SurfaceMutationPlan, SurfaceMutationPreview, SurfaceMutationOutcome, SurfaceTransport } from "./surface.js";
export type {
  CommandContext,
  Density,
  JsonObject,
  JsonPrimitive,
  JsonValue,
  ThemeMode,
} from "./types.js";

export type { DataOperator, DataFilter, DataSort, DataField, DataTable, DataCatalog, DataDescription, DataDescribeRequest, DataQueryRequest, DataQueryPage, ComputedCell } from "./data.js";

export { describeCellValue, formatCellValue, isMissingCell } from "./display.js";
export type { CellDisplay, CellValueKind } from "./display.js";

export { SURFACE_UI_METHODS, SURFACE_UI_UNSUPPORTED } from "./surface-ui.js";
export type { SurfacePresentation, SurfaceTask, SurfaceParentTransport } from "./surface-ui.js";
export type { SurfaceModel, SurfaceModelBinding, SurfaceModelStatus } from "./surface-model.js";
