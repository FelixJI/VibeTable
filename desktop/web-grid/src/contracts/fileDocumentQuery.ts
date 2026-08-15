export type FileDocumentFilterField =
  | "documentId"
  | "displayName"
  | "relativePath"
  | "extension"
  | "mimeType"
  | "sizeBytes"
  | "effectiveRevisionCreatedAt"
  | "status";

export type FileDocumentSortField = FileDocumentFilterField | "formalVersion";

export interface FileDocumentFilter {
  readonly field: FileDocumentFilterField;
  readonly operator:
    | "eq"
    | "contains"
    | "in"
    | "gt"
    | "gte"
    | "lt"
    | "lte"
    | "between"
    | "before"
    | "after";
  readonly value: unknown;
}

export interface FileDocumentQuery {
  readonly logic: "and" | "or";
  readonly filters: readonly FileDocumentFilter[];
  readonly sort: readonly {
    readonly field: FileDocumentSortField;
    readonly direction: "asc" | "desc";
  }[];
  readonly limit: number;
  readonly cursor: string | null;
}

export interface FileDocumentSummary {
  readonly contractVersion: "2.0";
  readonly documentId: string;
  readonly relativePath: string;
  readonly displayName: string;
  readonly extension: string;
  readonly mimeType: string;
  readonly sizeBytes: number;
  readonly effectiveRevisionId: string;
  readonly effectiveRevisionCreatedAt: string;
  readonly formalVersion: number | null;
  readonly status: "active" | "deleted";
}
