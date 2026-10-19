/** Presentation input for code-owned charts; contains no persisted layout identity. */
export interface DashboardChartInput {
  readonly name: string;
  readonly productType: "time-series" | "bar" | "line" | "donut" | "pie";
  readonly options: Readonly<Record<string, unknown>>;
  readonly query: Readonly<Record<string, unknown>>;
}

export interface DomainDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly path?: string;
  readonly severity: "warning" | "error";
}
