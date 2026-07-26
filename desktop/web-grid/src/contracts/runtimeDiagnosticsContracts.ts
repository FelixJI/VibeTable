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

export interface RuntimeDiagnosticsWebPayloadMap {
  "diagnostics.get": Readonly<Record<string, never>>;
}

export interface RuntimeDiagnosticsHostPayloadMap {
  "diagnostics.get": RuntimeDiagnostics;
}
