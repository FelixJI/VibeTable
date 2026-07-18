/**
 * Injection key for the Tabulator instance ref shared between WorkspaceView
 * (owner/provider) and GridHost (consumer). Extracted into its own module so
 * both files can import the same `Symbol` without a circular import through
 * the `.vue` file's setup scope.
 *
 * WorkspaceView creates the ref + `provide`s it; GridHost `inject`s it and
 * forwards it to `useTabulator` so the composable populates this ref rather
 * than a fresh internal one. WorkspaceView then reads `tabulator.value` from
 * the same ref when handling the copy/paste/delete keyboard shortcuts.
 */
import type { InjectionKey, Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";

export const TABULATOR_INJECTION_KEY: InjectionKey<Ref<TabulatorFull | null>> =
  Symbol("tabulator");
