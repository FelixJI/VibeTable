/** Import results are a projection of Go receipts; Host owns live execution. */
export interface ImportHistoryEntry {
  readonly taskId: string;
  readonly collection: string;
  readonly sourceType: "csv" | "xlsx";
  readonly sourceName: string;
  readonly state: "queued" | "running" | "interrupted" | "succeeded" | "failed" | "cancelled" | "aborted";
  readonly commitState: "unknown" | "committed";
  readonly createdCount: number | null;
  readonly updatedCount: number | null;
  readonly startedAt: string;
  readonly finishedAt: string | null;
  readonly sessionEpoch: number;
  readonly errorCode: string | null;
}

/**
 * Durable source-migration receipts (`sidecar/internal/sourceimport`). The
 * Host appends them to the `data.importHistory` result as `migrations`; the
 * raw wire value is validated into SourceImportEntry before any UI state.
 * Field-level provenance summaries (source definitions/identities) are never
 * projected into UI state — only their policy counts survive validation.
 */
export const SOURCE_IMPORT_CONTRACT = "vibetable.source-import.v1";

export type SourceImportState =
  | "queued"
  | "running"
  | "aborted"
  | "interrupted"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "unknown";

/** Execution stages in committed order; "settled" closes a full migration. */
export type SourceImportStage =
  | "schema"
  | "constraints"
  | "records"
  | "relations"
  | "attachments"
  | "settled";

export interface SourceImportTarget {
  readonly sourceTableId: string;
  readonly tableId: string;
  readonly name: string;
  /**
   * Physical collection name committed in the same transaction as the table
   * create (Go journal target projection). Absent on legacy receipts; when
   * present it must be non-empty — the frontend never derives tbl_ →
   * physical names or guesses by label.
   */
  readonly collection?: string;
}

export interface SourceImportBatch {
  readonly jobId: string;
  readonly batchId: string;
  readonly stage: string;
  readonly tableId: string;
  readonly created: number;
  readonly relationWrites: number;
  readonly attachmentWrites: number;
}

/** Diagnostic codes map to fixed localized copy; raw messages never render. */
export interface SourceImportDiagnostic {
  readonly code: string;
  readonly blocking: boolean;
}

export interface SourceImportReadWindow {
  readonly startedAt: string;
  readonly finishedAt: string;
  readonly consistency: "snapshot" | "window";
}

export interface SourceImportEntry {
  readonly jobId: string;
  readonly provider: string;
  readonly containerId: string;
  readonly sourceName: string;
  readonly state: SourceImportState;
  readonly stage: string;
  readonly created: number;
  readonly total: number;
  readonly notSubmitted: number;
  readonly unknownRecords: number;
  readonly unknownBatch: string | null;
  readonly targets: readonly SourceImportTarget[];
  readonly batches: readonly SourceImportBatch[];
  readonly diagnostics: readonly SourceImportDiagnostic[];
  readonly startedAt: string;
  readonly finishedAt: string | null;
  readonly sessionEpoch: number;
  readonly readWindow: SourceImportReadWindow | null;
  /** Confirmed snapshot/skip policy counts; raw definitions never reach UI. */
  readonly snapshotFieldCount: number;
  readonly skippedFieldCount: number;
}

export interface ImportHistoryResult {
  readonly items: readonly ImportHistoryEntry[];
  /** Absent on legacy hosts; treated as an empty migration history. */
  readonly migrations?: readonly unknown[];
}
