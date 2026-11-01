import {
  RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES,
  RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES,
} from "@/contracts/generated/hostBridgeCatalog";

export {
  RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES,
  RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES,
};

export interface RuntimeDiagnostics {
  readonly bundleVersion: string;
  readonly generatedAt: string;
  readonly operatingSystem: string;
  readonly programVersion: string;
  readonly dotnetVersion: string;
  readonly pocketBaseVersion: string;
  readonly memoryBytes: number;
  readonly dataDirectory: string;
  readonly logsDirectory: string;
  readonly components: readonly { readonly component: string; readonly state: string }[];
  readonly jobs: {
    readonly queued: number; readonly running: number; readonly succeeded: number;
    readonly failed: number; readonly cancelled: number;
  };
  readonly index: {
    readonly state: string; readonly generation: number; readonly processed: number;
    readonly total: number | null; readonly errorCode: string | null;
  };
  readonly pendingMutationRevision: number;
  /** Availability of the workspace-scoped part; the bundle is still usable when it failed. */
  readonly workspaceDiagnostics: {
    readonly state: "available" | "unavailable" | "failed";
    readonly errorCode: string | null;
  };
  readonly recentErrorCounts: readonly { readonly errorCode: string; readonly count: number }[];
  readonly logs: readonly {
    readonly timestamp: string; readonly level: string; readonly module: string;
    readonly event: string; readonly errorCode: string | null; readonly requestId: string | null;
    readonly operationId: string | null; readonly workspaceId: string | null;
    readonly sessionEpoch: number | null; readonly jobId: string | null;
    readonly durationMs: number | null;
  }[];
}

export type RuntimeDiagnosticsWebMessageType =
  (typeof RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES)[number];

export type RuntimeDiagnosticsHostMessageType =
  (typeof RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES)[number];

export interface RuntimeDiagnosticsWebPayloadMap {
  "diagnostics.get": Readonly<Record<string, never>>;
}

export interface RuntimeDiagnosticsHostPayloadMap {
  "diagnostics.get": RuntimeDiagnostics;
}
