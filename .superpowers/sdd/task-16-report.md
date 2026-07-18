# Task 16 Report — useTabulator + GridHost + feedback components

## Status
Complete. All verification gates pass: typecheck (0 errors), build (success), full suite (188 tests, no regressions).

## Files created (all under `desktop/web-grid-v2/src/`)
- `composables/useTabulator.ts` — Tabulator lifecycle composable (init on first page, incremental `setData`, `setColumns` on schema change, `destroy` on unmount).
- `composables/useTabulator.test.ts` — 5 tests; mocks `@/grid/createGrid` so jsdom never instantiates real Tabulator.
- `components/grid/GridHost.vue` — wrapper: `<div ref="gridEl">` + `useTabulator(gridEl)` + LoadingOverlay/ErrorOverlay.
- `components/feedback/LoadingOverlay.vue` — absolute overlay with `NSpin`, shown when `show` prop is true.
- `components/feedback/LoadingOverlay.test.ts` — 3 tests (hidden / shown / toggle).
- `components/feedback/ErrorOverlay.vue` — absolute overlay with `NResult status="error"`, bound to `message` prop.
- `components/feedback/ErrorOverlay.test.ts` — 4 tests (hidden / shown / message change / toggle).
- `components/feedback/StatusBar.vue` — bottom bar; derived status text via `t()` + tableStore + workspaceStore.
- `components/feedback/StatusBar.test.ts` — 4 tests (error / loading / loaded / idle fallback).

## Contracts
**Untouched.** `git diff desktop/web-grid-v2/src/contracts/` is empty. All new code only *consumes* existing contracts (`TablePage`, `ColumnSchema`, `DatasetReadyPayload`) and existing stores/services.

## How the createGrid real signature was handled
The brief's design assumed `createGrid(element)` could build an empty grid, then seed data later. The **verified** signature is `createGrid(element, page: TablePage)` — it requires a full page (columns + rows) to initialize and has no empty-init form. Adaptation:

- `useTabulator` does **not** init in `onMounted`. Instead it watches `[() => gridEl.value, () => store.pages.length]` with `{ immediate: true }`. Init fires only once, when **both** (a) the host element is populated (template ref) AND (b) the first `TablePage` has arrived in `store.pages`. It then calls `createGrid(el, pages[0])`.
- This handles both orderings: page-arrives-before-mount and mount-before-page.
- A `lastSeededRows` snapshot + `sameRows()` shallow-identity compare prevents the redundant first `setData` flush (createGrid already embedded those rows in its `data` option) — robust to Vue's flush ordering, unlike a boolean skip-flag.

## How schema changes are handled
- Column-signature string (`name:dataType` joined by `|`) is captured at init and compared in a `store.schema` watcher.
- On a real signature change → `tabulator.setColumns(buildColumns(carrier))`. `setColumns` exists on the env.d.ts `Tabulator` type shim (`env.d.ts:35`).
- `buildColumns(page)` reads only `page.columns`, so the watcher builds a minimal carrier `{ columns: schema } as TablePage` — type-safe without fabricating a full page.
- If `setColumns` ever throws at runtime (Tabulator version quirk), the `catch` falls back to `setData([...store.allRows])`. No destroy+recreate is used (that would re-trigger the architecture-debt path).

## Tabulator CSS path used
`import "tabulator-tables/dist/css/tabulator.min.css"` — verified to exist in `node_modules/tabulator-tables/dist/css/tabulator.min.css` (tabulator-tables@6.5.2). The import is at module-load top of `useTabulator.ts` so Vite bundles the styles before the grid mounts.

## How tests mock createGrid
`useTabulator.test.ts` uses `vi.mock("@/grid/createGrid", ...)` (hoisted by vitest) to replace `createGrid` with a factory returning a fake Tabulator instance exposing `setData` / `setColumns` / `destroy` as `vi.fn()`s. A module-level `lastMock` variable captures the most-recently-created mock so each test can assert on call counts/args. This deliberately avoids instantiating real Tabulator in jsdom (which lacks layout). Tests assert the **lifecycle** invariants:
1. `createGrid` is NOT called before the first page arrives.
2. `createGrid` is called exactly once with `(element, pages[0])` when the first page arrives.
3. A subsequent `datasetReady` (data-only change) calls `setData` once with the flattened rows and does NOT call `createGrid` again (no destroy+rebuild).
4. `destroy` is called exactly once on unmount.
5. Init waits for the element ref to be populated even if the page arrived first.

The feedback components are simple enough to mount directly with `@vue/test-utils` against the real `NSpin` / `NResult`.

## Test counts
- Baseline before task: 172 tests / 21 files.
- Added: 16 tests across 4 new files (5 + 3 + 4 + 4).
- Final: **188 tests / 25 files, all passing, no regressions.**

## typecheck + build output
- `npm run typecheck` (`vue-tsc --noEmit`): **0 errors.**
- `npm run build` (`vue-tsc --noEmit && vite build`): **success**, `dist/` produced (`index-Cp6ABdsC.js` 56.37 kB, `index-C5NIfyrF.css` 1.82 kB).

## Commit
`feat(web-grid-v2): add useTabulator (incremental setData) + GridHost + feedback`
(hash recorded below by git after commit.)

## Concerns
- **`setColumns` runtime behavior is unverified in jsdom** (tests mock createGrid). The env.d.ts shim declares `setColumns(columns: unknown[]): void`, but if the real Tabulator 6.5.2 runtime has quirks on in-place column swaps, the `catch` branch falls back to `setData`. A real-browser smoke test (Task 18 integration) should confirm a column add/remove renders correctly. Schema changes are expected to be rare for Phase A.
- **`buildColumns` carrier cast**: `{ columns: schema } as TablePage` relies on `buildColumns` only reading `.columns`. If `buildColumns` is later extended to read other `TablePage` fields, this carrier would need expanding. Low risk for Phase A.
- **Naive UI `NSpin`/`NResult` mount into `document.body`** (teleport); the feedback tests assert on the overlay wrapper container and rendered text rather than naive-ui internals, so they are stable across naive-ui patch versions.
- **StatusBar i18n**: tests save/restore the module-level locale via `withLocale` to avoid leaking the locale choice to other suites.
