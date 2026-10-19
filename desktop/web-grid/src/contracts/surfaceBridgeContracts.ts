/**
 * #485 (Goal #480): the visual Interface/dashboard builder is retired. This
 * contract now describes the ONLY legacy-layout bridge surface: a Host-owned,
 * read-only preservation channel. The renderer may request a detection report
 * and ask the Host to save an authoritative preservation snapshot; it never
 * supplies export content or a target path, and no import/execution entry
 * exists on this contract. Abandoning an in-flight request uses the
 * fire-and-forget `legacyLayout.cancelRequested` envelope: the Host cancels
 * the correlated target request (freeing its single concurrency gate) and
 * settles the TARGET with `CANCELLED`; the cancel itself gets no reply.
 */

export type LegacyLayoutWebMessageType =
  | "legacyLayout.reportRequested"
  | "legacyLayout.exportRequested"
  | "legacyLayout.cancelRequested";

export type LegacyLayoutHostMessageType =
  | "legacyLayout.reportLoaded"
  | "legacyLayout.exportCompleted";

export interface LegacyLayoutDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly severity: "warning" | "error";
  /** Stable record/logical id the diagnostic refers to, when known. */
  readonly itemId?: string;
  readonly path?: string | null;
}

export interface LegacyLayoutReport {
  readonly interfaces: number;
  readonly dashboards: number;
  readonly panels: number;
  readonly diagnostics: readonly LegacyLayoutDiagnostic[];
  readonly diagnosticCount?: number;
  readonly diagnosticsTruncated?: boolean;
  readonly collectedAt: string;
}

export interface LegacyLayoutReportRequest {
  readonly reason?: "detect" | "refresh";
}

export type LegacyLayoutExportRequest = Readonly<Record<string, never>>;

/**
 * Renderer cancellation of an in-flight preservation request: the Host
 * correlates `targetRequestId` and settles the TARGET (`CANCELLED`); the
 * cancel envelope itself is notify-only (no correlated reply) but must carry
 * the current workspace scope just like the requests it cancels.
 */
export interface LegacyLayoutCancelRequest {
  readonly targetRequestId: string;
}

export interface LegacyLayoutExportResult {
  readonly cancelled: boolean;
  /** Display name of the saved file; never a filesystem path. */
  readonly outputDisplayName: string | null;
}

export interface LegacyLayoutWebPayloadMap {
  "legacyLayout.reportRequested": LegacyLayoutReportRequest;
  "legacyLayout.exportRequested": LegacyLayoutExportRequest;
  "legacyLayout.cancelRequested": LegacyLayoutCancelRequest;
}

export interface LegacyLayoutHostPayloadMap {
  "legacyLayout.reportLoaded": LegacyLayoutReport;
  "legacyLayout.exportCompleted": LegacyLayoutExportResult;
}

export const LEGACY_LAYOUT_WEB_MESSAGE_TYPES = [
  "legacyLayout.reportRequested",
  "legacyLayout.exportRequested",
  "legacyLayout.cancelRequested",
] as const satisfies readonly LegacyLayoutWebMessageType[];

export const LEGACY_LAYOUT_HOST_MESSAGE_TYPES = [
  "legacyLayout.reportLoaded",
  "legacyLayout.exportCompleted",
] as const satisfies readonly LegacyLayoutHostMessageType[];

export interface LegacyLayoutRequestResults {
  "legacyLayout.reportRequested": LegacyLayoutReport;
  "legacyLayout.exportRequested": LegacyLayoutExportResult;
  /** Cancellation is notify-only; the Host settles the target request instead. */
  "legacyLayout.cancelRequested": never;
}
