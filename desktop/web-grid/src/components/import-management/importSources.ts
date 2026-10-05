/** Local file pickers and trusted Host source wizards share this catalog. */
export type ImportSourceId = "csv" | "xlsx" | "feishu" | "wps";

export interface ImportSourceDescriptor {
  /** Stable identity shared by UI descriptors and future connector wiring. */
  readonly id: ImportSourceId;
  /** i18n label key for the source card. */
  readonly labelKey: string;
  /** File-extension whitelist the native picker opens; `null` for non-file sources. */
  readonly accept: readonly string[] | null;
  /** Availability of the local picker or trusted native source wizard. */
  readonly available: boolean;
  /** Why an unavailable source is disabled; shown verbatim from i18n. */
  readonly unavailableReasonKey: "importManagement.source.unavailableHint"
  | "importManagement.source.unavailableTooltip";
}

/** Cloud sources open a trusted native wizard; local sources retain their picker whitelist. */
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
    available: true,
    unavailableReasonKey: "importManagement.source.unavailableTooltip",
  },
  {
    id: "wps",
    labelKey: "importManagement.source.wps",
    accept: null,
    available: true,
    unavailableReasonKey: "importManagement.source.unavailableTooltip",
  },
];

export function localFileAcceptList(): readonly string[] {
  return IMPORT_SOURCES.flatMap((source) => source.available ? (source.accept ?? []) : []);
}
