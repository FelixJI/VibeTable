export interface RuntimeDiagnostics {
  readonly currentDirectory: string;
  readonly programDirectory: string;
  readonly dataDirectory: string;
  readonly operatingSystem: string;
  readonly programVersion: string;
  readonly dotnetVersion: string;
  readonly pocketBaseVersion: string;
  readonly memoryBytes: number;
  readonly dataServiceState: string;
}

export const RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES = [
  "diagnostics.get",
] as const;

export type RuntimeDiagnosticsWebMessageType =
  (typeof RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES)[number];

export const RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES =
  RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES;

export type RuntimeDiagnosticsHostMessageType =
  (typeof RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES)[number];

export interface RuntimeDiagnosticsWebPayloadMap {
  "diagnostics.get": Readonly<Record<string, never>>;
}

export interface RuntimeDiagnosticsHostPayloadMap {
  "diagnostics.get": RuntimeDiagnostics;
}
