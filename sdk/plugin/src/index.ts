export { PluginCapabilityError } from "./capabilities.js";
export type { ReadGrant, WriteGrant } from "./capabilities.js";
export { createSurfaceClient, finishHeadlessRun, SURFACE_API_CONTRACT } from "./surface.js";
export type { SurfaceLifecycle, SurfaceCapabilities, SurfaceIoTask, SurfaceIoStatus, SurfaceImportPreview, SurfaceAttachment, SurfaceAttachmentTarget, SurfaceSubscription, SurfaceDataEvent, SurfaceMutationPlan, SurfaceMutationPreview, SurfaceMutationOutcome, SurfaceTransport } from "./surface.js";
export type {
  CommandContext,
  Density,
  JsonObject,
  JsonPrimitive,
  JsonValue,
  SurfacePhase,
  ThemeMode,
} from "./types.js";

export type { DataOperator, DataFilter, DataSort, DataField, DataTable, DataCatalog, DataDescription, DataDescribeRequest, DataQueryRequest, DataQueryPage, ComputedCell } from "./data.js";

export { describeCellValue, formatCellValue, isMissingCell } from "./display.js";
export type { CellDisplay, CellValueKind } from "./display.js";

export { SURFACE_UI_METHODS, SURFACE_UI_UNSUPPORTED, SURFACE_LIFECYCLE_CONTRACT } from "./surface-ui.js";
export type { SurfacePresentation, SurfaceTask, SurfaceParentTransport } from "./surface-ui.js";
export type { ModelOutputFileField, SurfaceModel, SurfaceModelBinding, SurfaceModelStatus } from "./surface-model.js";
export { FIELD_PROTECTION_CONTRACT, FIELD_PROTECTION_LIMITS, FIELD_PROTECTION_HOST_METHODS, FieldProtectionDeclarationError, validateFieldProtectionDeclaration, readFieldProtectionDeclaration } from "./surface-protection.js";
export type { FieldProtectionDeclaredField, FieldProtectionDeclaration, FieldProtectionModel, FieldProtectionModelEntity } from "./surface-protection.js";
export type { SurfaceFieldProtection, SurfaceFieldProtectionField, SurfaceFieldProtectionStatus } from "./surface-protection.js";
export { SURFACE_DATASET_CONTRACT } from "./surface-dataset.js";
export type { DatasetSource, DatasetVersion, DatasetInfo, DatasetPage, DatasetSum, DatasetSummary, SurfaceDataset, SurfaceDatasets } from "./surface-dataset.js";
export { SURFACE_DATA_VERSIONS_CONTRACT, DATA_VERSIONS_LIMITS, isDataVersion, sameDataVersion } from "./surface-versions.js";
export type { DataVersion, DataVersionSource, DataVersionsRequest, DataVersions, SurfaceDataVersions } from "./surface-versions.js";
export { SURFACE_SNAPSHOT_CONTRACT, SNAPSHOT_LIMITS } from "./surface-snapshot.js";
export type { DataSnapshotSource, DataSnapshotRequest, DataSnapshotVersion, DataSnapshotSourceInfo, DataSnapshotInfo, DataSnapshotChunk, DataSnapshotPage, DataSnapshotPosition, DataSnapshotBatch, DataSnapshot, SurfaceDataSnapshot } from "./surface-snapshot.js";
export type { SurfaceTransaction, TransactionDeclaration, TransactionDecimal, TransactionRule, GroupTarget, GroupReference, StateRelationCase, ReversalSpec, AuthorityRef, AllocationSpec, SettlementSpec, ChangeEvidenceSpec, TransactionReference, TransactionValue,
  TransactionOperation, TransactionPlan, TransactionEffect, TransactionPreview, TransactionApplied, TransactionUnknown, TransactionStatus } from "./surface-transaction.js";

export { SURFACE_OUTPUT_CONTRACT, SURFACE_OUTPUT_MAX_BYTES } from "./surface-output.js";
export type { SurfaceOutput, SurfaceOutputClient, SurfaceOutputOptions, SurfaceOutputReceipt, SurfaceOutputSource } from "./surface-output.js";
