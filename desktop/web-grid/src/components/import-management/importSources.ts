/**
 * Registrable import source descriptors (#434 AC6).
 *
 * One explicit shape every entry point shares — the management page renders
 * them and WorkspaceView resolves the picker `accept` list from them — so
 * local CSV/XLSX and explicitly-unavailable cloud sources are not hardcoded in
 * scattered components. This is NOT a plugin framework: a future connector
 * only has to provide another descriptor with this same shape, alongside the
 * frozen `ImportHistoryEntry` contract and the existing task
 * progress/diagnostic fields on `DataTaskStatus`.
 */
export type ImportSourceId = "csv" | "xlsx" | "feishu" | "wps";

export interface ImportSourceDescriptor {
  /** Stable identity shared by UI descriptors and future connector wiring. */
  readonly id: ImportSourceId;
  /** i18n label key for the source card. */
  readonly labelKey: string;
  /** File-extension whitelist the native picker opens; `null` for non-file sources. */
  readonly accept: readonly string[] | null;
  /** Local picker sources are available; cloud sources are explicitly not. */
  readonly available: boolean;
  /** Why an unavailable source is disabled; shown verbatim from i18n. */
  readonly unavailableReasonKey: "importManagement.source.unavailableHint"
  | "importManagement.source.unavailableTooltip";
}

/**
 * The single source catalog. Cloud sources (feishu/wps) stay listed but
 * explicitly unavailable: the product never contacts them and never persists
 * credentials. `accept` duplicates the dataIoService picker whitelist for the
 * local sources only; the toolbar entry keeps its existing protocol.
 */
export const IMPORT_SOURCES: readonly ImportSourceDescriptor[] = [
  {
    id: "csv",
    labelKey: "importManagement.source.csv",
    accept: [".csv"],
    available: true,
    unavailableReasonKey: "importManagement.source.unavailableHint",
  },
  {
    id: "xlsx",
    labelKey: "importManagement.source.xlsx",
    accept: [".xlsx", ".xlsm"],
    available: true,
    unavailableReasonKey: "importManagement.source.unavailableHint",
  },
  {
    id: "feishu",
    labelKey: "importManagement.source.feishu",
    accept: null,
    available: false,
    unavailableReasonKey: "importManagement.source.unavailableTooltip",
  },
  {
    id: "wps",
    labelKey: "importManagement.source.wps",
    accept: null,
    available: false,
    unavailableReasonKey: "importManagement.source.unavailableTooltip",
  },
];

export function localFileAcceptList(): readonly string[] {
  return IMPORT_SOURCES.flatMap((source) => source.available ? (source.accept ?? []) : []);
}
