/// <reference types="vite/client" />

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, never>, Record<string, never>, unknown>;
  export default component;
}

// Copied verbatim from web-grid/src/env.d.ts (Tabulator 6.5.2 ships no types).
declare module "tabulator-tables" {
  export interface TabulatorRowComponent {
    getData(): Record<string, unknown>;
    getElement?(): HTMLElement;
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
    getColumns(): unknown[];
    getRanges(): TabulatorRangeComponent[];
    getRows(range?: "active" | "visible" | "selected" | "all"): TabulatorRowComponent[];
    getSelectedData(): Record<string, unknown>[];
    setData(data: unknown[]): Promise<void>;
    setColumns(columns: unknown[]): void;
    destroy?(): void;
  }
  export class TabulatorFull extends Tabulator {}
}

declare module "*.css";
