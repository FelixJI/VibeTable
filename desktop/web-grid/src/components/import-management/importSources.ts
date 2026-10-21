/** Local file pickers and the trusted Host .base wizard share this catalog. */
export type ImportSourceId = "csv" | "xlsx" | "feishuBase";

export interface ImportSourceDescriptor {
  /** Stable identity shared by UI descriptors and the Host wizard request. */
  readonly id: ImportSourceId;
  /** i18n label key for the source card. */
  readonly labelKey: string;
  /** File-extension whitelist the native picker opens; `null` for the Host wizard. */
  readonly accept: readonly string[] | null;
  /** Availability of the local picker or trusted native source wizard. */
  readonly available: boolean;
  /** Why an unavailable source is disabled; shown verbatim from i18n. */
  readonly unavailableReasonKey: "importManagement.source.unavailableHint"
  | "importManagement.source.unavailableTooltip";
}

/**
 * Formal sources: local CSV/XLSX pickers plus the Feishu .base file wizard.
 * Retired online cloud entries ("feishu"/"wps") stay out of the catalog; the
 * Host rejects their wizard requests, and historical receipts keep rendering
 * their provider tags from the durable journal.
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
    id: "feishuBase",
    labelKey: "importManagement.source.feishuBase",
    accept: null,
    available: true,
    unavailableReasonKey: "importManagement.source.unavailableTooltip",
  },
];

export function localFileAcceptList(): readonly string[] {
  return IMPORT_SOURCES.flatMap((source) => source.available ? (source.accept ?? []) : []);
}
