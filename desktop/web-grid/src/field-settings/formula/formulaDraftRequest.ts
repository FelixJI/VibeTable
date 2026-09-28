import type { FormulaTextRange } from "@/contracts/generated/workbench";

/**
 * Editor -> service formula draft requests. The single-argument payload keeps
 * the existing `@validate-formula` forwarding in WorkspaceView compatible.
 */
export type FormulaDraftValidateRequest =
  /** Input changed: drop the previous validation/preview at once, no request. */
  | { readonly kind: "invalidate"; readonly discardDocument?: boolean }
  /** Ask Go to restore an author document from persisted canonical text. */
  | {
    readonly kind: "restore";
    readonly displaySource: string;
    /**
     * False only for the `displaySource: "0"` catalog bootstrap of an empty
     * formula: the catalog is kept but "0" never becomes the user draft.
     */
    readonly adoptDocument: boolean;
  }
  /** Validate the working display source against its stable tokens. */
  | {
    readonly kind: "document";
    readonly displaySource: string;
    readonly authorDocument: import("@/contracts/generated/workbench").FormulaAuthorDocument;
  };

/** Structured validation failure kept for the editor UI (UTF-16 range). */
export interface FormulaDraftDiagnostic {
  readonly message: string;
  readonly code: string | null;
  readonly range: FormulaTextRange | null;
}
