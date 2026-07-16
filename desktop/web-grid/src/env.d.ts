/**
 * Ambient type declarations for assets that have no bundled `.d.ts`.
 *
 * Tabulator 6.5.2 ships JavaScript only (no TypeScript types and no
 * `@types/tabulator-tables` for v6). Rather than depend on a community bundle,
 * we declare a minimal, structurally-typed surface that is sufficient for the
 * Phase-A read-only grid. The runtime export is `Tabulator` (default) and
 * `TabulatorFull` from `tabulator-tables/dist/js/tabulator_esm.mjs`.
 *
 * CSS imports are bundled by Vite and carry no runtime value.
 */

// Tabulator's constructor-options shape is large; for Phase A we only need a
// permissive structural type so `new Tabulator(el, {...})` type-checks. The
// grid code never relies on option autocomplete beyond what it sets itself.
declare module "tabulator-tables" {
  export interface TabulatorRowComponent {
    getData(): Record<string, unknown>;
  }

  export interface TabulatorColumnComponent {
    getField(): string;
  }

  export interface TabulatorRangeComponent {
    getRows(): TabulatorRowComponent[];
    getColumns(): TabulatorColumnComponent[];
  }

  export type TabulatorOptions = Record<string, unknown> & {
    columns?: unknown[];
    data?: unknown[];
    selectableRange?: boolean | unknown;
    selectableRangeCellBDash?: unknown;
    clipboard?: boolean | "copy" | "paste" | "copy paste";
    clipboardPasteAction?: unknown;
    [key: string]: unknown;
  };

  export class Tabulator {
    constructor(element: string | HTMLElement, options: TabulatorOptions);
    // Surface used by tests / future tasks. Keep intentionally small.
    getColumns(): unknown[];
    getRanges(): TabulatorRangeComponent[];
    setData(data: unknown[]): Promise<void>;
    destroy?(): void;
  }

  export class TabulatorFull extends Tabulator {}
}

// Allow CSS imports to type-check under `isolatedModules` + `verbatimModuleSyntax`.
declare module "*.css";
