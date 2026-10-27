/**
 * Host-owned archival export of retired layouts and retained plugin records.
 * The renderer supplies neither export contents nor paths. Cancellation
 * targets the original request; it does not create a separate reply.
 */

export type PreservationWebMessageType =
  | "legacyLayout.reportRequested"
  | "legacyLayout.exportRequested"
  | "legacyLayout.cancelRequested"
  | "pluginPreservation.exportRequested"
  | "pluginPreservation.cancelRequested";

export type PreservationHostMessageType =
  | "legacyLayout.reportLoaded"
  | "legacyLayout.exportCompleted"
  | "pluginPreservation.exportCompleted";

import {
  PRESERVATION_HOST_MESSAGE_TYPES,
  PRESERVATION_WEB_MESSAGE_TYPES,
} from "@/contracts/generated/hostBridgeCatalog";

export { PRESERVATION_HOST_MESSAGE_TYPES, PRESERVATION_WEB_MESSAGE_TYPES };

// Compile-time coupling to the payload-map unions above (replaces the old
// inline `satisfies` now that the names are catalog-owned).
export const PRESERVATION_WEB_NAMES_ASSIGNABLE: readonly PreservationWebMessageType[] =
  PRESERVATION_WEB_MESSAGE_TYPES;
export const PRESERVATION_HOST_NAMES_ASSIGNABLE: readonly PreservationHostMessageType[] =
  PRESERVATION_HOST_MESSAGE_TYPES;

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

export interface PluginPreservationExportRequest {
  readonly pluginId: string;
  readonly installationId: string;
  readonly installationRevision: number;
}
export interface PluginPreservationExportResult extends LegacyLayoutExportResult {
  readonly missingPackages: number;
}

export interface PreservationWebPayloadMap {
  "pluginPreservation.exportRequested": PluginPreservationExportRequest;
  "pluginPreservation.cancelRequested": LegacyLayoutCancelRequest;
  "legacyLayout.reportRequested": LegacyLayoutReportRequest;
  "legacyLayout.exportRequested": LegacyLayoutExportRequest;
  "legacyLayout.cancelRequested": LegacyLayoutCancelRequest;
}

export interface PreservationHostPayloadMap {
  "pluginPreservation.exportCompleted": PluginPreservationExportResult;
  "legacyLayout.reportLoaded": LegacyLayoutReport;
  "legacyLayout.exportCompleted": LegacyLayoutExportResult;
}

export interface PreservationRequestResults {
  "pluginPreservation.exportRequested": PluginPreservationExportResult;
  "pluginPreservation.cancelRequested": never;
  "legacyLayout.reportRequested": LegacyLayoutReport;
  "legacyLayout.exportRequested": LegacyLayoutExportResult;
  /** Cancellation is notify-only; the Host settles the target request instead. */
  "legacyLayout.cancelRequested": never;
}
