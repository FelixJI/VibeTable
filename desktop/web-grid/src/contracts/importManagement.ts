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

export interface ImportHistoryResult {
  readonly items: readonly ImportHistoryEntry[];
}
