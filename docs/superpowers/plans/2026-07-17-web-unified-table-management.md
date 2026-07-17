# Web-Unified Table Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move native WPF table-management (list/create/delete) into the existing WebView2 TypeScript web-grid as a sidebar + main layout, leaving the C# host as a pure process-orchestration shell.

**Architecture:** Web sidebar posts new bridge request types (`tableAdmin.createRequested` / `tableAdmin.deleteRequested`) → C# `WorkspaceRequestDispatcher` calls the same `IDirectusRpcGateway` the native windows used → on success the host re-lists collections and pushes `database.collectionsChanged`. The table list's single source of truth is the host. No new auth notification; the sidebar enables on `database.opened`. Native `TableManagementWindow` / `TableAdminWindow` / the `表管理` button are deleted at the end.

**Tech Stack:** C# (.NET 10 WPF, MSTest), TypeScript 6 + Vite 8 + Tabulator 6 (vitest, jsdom), Python BFF (zero changes).

**Spec:** `docs/superpowers/specs/2026-07-17-web-unified-table-management-design.md`

## Global Constraints

- Field types are exactly `["string","integer","decimal","date","boolean","text"]` — verbatim from `backend/contracts/table_admin.py:22`. A third copy is added in TS; all three must stay in sync.
- Identifier regex is exactly `^[A-Za-z][A-Za-z0-9_]{0,63}$` — verbatim from `TableAdminWindow.xaml.cs` and `backend/contracts/table_admin.py:32`. A TS copy is added.
- `IsUserTable` filter excludes collections starting with `directus_`, `vibetable_document`, or `vibetable_workspace` — verbatim from `TableManagementWindow.xaml.cs:70-86`.
- CSP stays `connect-src 'none'` (web-grid makes zero network calls; all data via postMessage).
- WebView2 navigation stays locked to `https://app.vibetable.local/` — no new origins.
- Tests: C# uses MSTest (`[TestClass]`/`[TestMethod]`); TS uses vitest with jsdom.
- Commits: Chinese-or-English conventional-commit messages; commit after each green test cycle.
- Do NOT modify: `desktop/src/VibeTable.Infrastructure/Directus/*`, any `Directus*Window.xaml(.cs)`, `backend/*`.

---

## File Structure

### C# — Create
- `desktop/src/VibeTable.Desktop/Services/DirectusCollectionFilter.cs` — shared static `IsUserTable(collection)` + `FilterUserTables(...)` (extracted from `TableManagementWindow`).
- `desktop/tests/VibeTable.Desktop.Tests/FakeDirectusRpcGateway.cs` — in-memory `IDirectusRpcGateway` fake for dispatcher tests.

### C# — Modify
- `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs` — add `_directusGateway` field + `SetDirectusGateway` setter; add two switch cases + two handler methods.
- `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs` — add two entries to `WebRequestWhitelist`, one to `HostNotificationWhitelist`.
- `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs` — call `_dispatcher.SetDirectusGateway(...)` after gateway creation (line 642 area); remove `OnManageTables` (~835-849) and `ManageTablesButton.IsEnabled` (~227).
- `desktop/src/VibeTable.Desktop/MainWindow.xaml` — remove the `表管理` button (`ManageTablesButton`, lines 27-34).
- `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml(.cs)` — point `IsUserTable` call at the shared filter first (transitional), then DELETE both files in Task 9.
- `desktop/tests/VibeTable.Desktop.Tests/WorkspaceRequestDispatcherTests.cs` — add create/delete tests; add `FakeDirectusRpcGateway` (or separate file).
- `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs` — add whitelist assertions for the three new types.

### C# — Delete (Task 9)
- `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml`
- `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml.cs`
- `desktop/src/VibeTable.Desktop/TableAdminWindow.xaml`
- `desktop/src/VibeTable.Desktop/TableAdminWindow.xaml.cs`

### TS — Create
- `desktop/web-grid/src/tableAdminFlow.ts` — pure reducers + async orchestrators for sidebar state.
- `desktop/web-grid/src/tableAdminFlow.test.ts` — reducer + orchestrator tests.
- `desktop/web-grid/src/tableAdminValidation.ts` — `TABLE_NAME_PATTERN`, `TABLE_FIELD_TYPES`, `validateTableName`, `validateFields`.
- `desktop/web-grid/src/tableAdminValidation.test.ts` — validation tests.

### TS — Modify
- `desktop/web-grid/src/contracts.ts` — add message types + payload interfaces + payload-map entries.
- `desktop/web-grid/src/hostBridge.ts` — add types to runtime `WEB_MESSAGE_TYPES` / `HOST_EVENT_TYPES` Sets.
- `desktop/web-grid/src/main.ts` — wire sidebar, modal, remove `#table-select` logic.
- `desktop/web-grid/index.html` — add `#sidebar` markup, remove `#table-select`, add modal markup.
- `desktop/web-grid/src/styles.css` — `.sidebar`, `.table-list`, modal styles.

---

## Task 1: Extract shared `DirectusCollectionFilter` (C#)

**Files:**
- Create: `desktop/src/VibeTable.Desktop/Services/DirectusCollectionFilter.cs`
- Create test: `desktop/tests/VibeTable.Desktop.Tests/DirectusCollectionFilterTests.cs`

**Interfaces:**
- Produces: `VibeTable.Desktop.Services.DirectusCollectionFilter.IsUserTable(string) → bool` and `.FilterUserTables(IEnumerable<string>) → IReadOnlyList<string>` (filtered + `OrderBy(OrdinalIgnoreCase)`).

- [ ] **Step 1: Write the failing test**

Create `desktop/tests/VibeTable.Desktop.Tests/DirectusCollectionFilterTests.cs`:

```csharp
using System;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DirectusCollectionFilterTests
{
    [TestMethod]
    public void IsUserTable_ExcludesDirectusSystemCollections()
    {
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("directus_users"));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("directus_collections"));
    }

    [TestMethod]
    public void IsUserTable_ExcludesVibetableDocumentAndWorkspaceCollections()
    {
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("vibetable_document_things"));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("vibetable_workspace_main"));
    }

    [TestMethod]
    public void IsUserTable_AcceptsOrdinaryUserCollections()
    {
        Assert.IsTrue(DirectusCollectionFilter.IsUserTable("projects"));
        Assert.IsTrue(DirectusCollectionFilter.IsUserTable("my_table_2"));
    }

    [TestMethod]
    public void IsUserTable_RejectsEmptyAndWhitespace()
    {
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable(""));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable("   "));
        Assert.IsFalse(DirectusCollectionFilter.IsUserTable(null!));
    }

    [TestMethod]
    public void FilterUserTables_RemovesSystemAndSortsCaseInsensitively()
    {
        var input = new[] { "Zebra", "directus_users", "apple", "vibetable_document_x", "mango" };
        var result = DirectusCollectionFilter.FilterUserTables(input);
        CollectionAssert.AreEqual(
            new[] { "apple", "mango", "Zebra" },
            result);
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run from repo root:
```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~DirectusCollectionFilterTests"
```
Expected: FAIL with compile error `DirectusCollectionFilter does not exist`.

- [ ] **Step 3: Write minimal implementation**

Create `desktop/src/VibeTable.Desktop/Services/DirectusCollectionFilter.cs`:

```csharp
using System;
using System.Collections.Generic;
using System.Linq;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Shared filter that distinguishes user-created Directus collections from
/// Directus system collections and VibeTable's own system collections.
/// Extracted verbatim from the former <c>TableManagementWindow.IsUserTable</c>
/// so both the web-bridge dispatcher and (transitively) the legacy window
/// share one implementation.
/// </summary>
public static class DirectusCollectionFilter
{
    public static bool IsUserTable(string? collection)
    {
        if (string.IsNullOrWhiteSpace(collection))
        {
            return false;
        }
        if (collection.StartsWith("directus_", StringComparison.Ordinal))
        {
            return false;
        }
        if (collection.StartsWith("vibetable_document", StringComparison.Ordinal)
            || collection.StartsWith("vibetable_workspace", StringComparison.Ordinal))
        {
            return false;
        }
        return true;
    }

    /// <summary>Filters a raw collection list to user tables, sorted case-insensitively.</summary>
    public static IReadOnlyList<string> FilterUserTables(IEnumerable<string> collections)
        => collections
            .Where(IsUserTable)
            .OrderBy(c => c, StringComparer.OrdinalIgnoreCase)
            .ToList();
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~DirectusCollectionFilterTests"
```
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add desktop/src/VibeTable.Desktop/Services/DirectusCollectionFilter.cs desktop/tests/VibeTable.Desktop.Tests/DirectusCollectionFilterTests.cs
git commit -m "refactor: extract DirectusCollectionFilter shared by dispatcher and legacy window"
```

---

## Task 2: Route legacy window through the shared filter (C#)

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml.cs` (lines ~70-86)

**Interfaces:**
- Consumes: `DirectusCollectionFilter.IsUserTable` (from Task 1).

This is a transitional, behavior-preserving change so `TableManagementWindow` uses the shared filter before the window is deleted in Task 9. It guarantees the extraction is correct by reusing it immediately.

- [ ] **Step 1: Read the current `IsUserTable` in `TableManagementWindow.xaml.cs`**

Confirm the private method at ~lines 70-86 matches the extracted logic (same prefix checks). It should, since Task 1 copied it verbatim.

- [ ] **Step 2: Replace the private method with a call to the shared filter**

In `TableManagementWindow.xaml.cs`, delete the private `IsUserTable` method and update its single caller in `RefreshAsync` (~line 33-63). Find where collections are filtered — the line looks like `collections.Where(IsUserTable)` or similar — and replace with `DirectusCollectionFilter.FilterUserTables(...)`.

Concretely, locate the filtering call site and change it from the local filter to the shared one. Remove the now-unused private `IsUserTable` method entirely. Add `using VibeTable.Desktop.Services;` only if the file's namespace is not already `VibeTable.Desktop` (it is `VibeTable.Desktop` for this window, so no using needed — `DirectusCollectionFilter` is in `VibeTable.Desktop.Services`; check the file's namespace declaration and add the using if required).

- [ ] **Step 3: Build to verify it compiles**

```bash
dotnet build desktop/VibeTable.sln
```
Expected: Build succeeded, no errors.

- [ ] **Step 4: Commit**

```bash
git add desktop/src/VibeTable.Desktop/TableManagementWindow.xaml.cs
git commit -m "refactor: TableManagementWindow reuses DirectusCollectionFilter"
```

---

## Task 3: Extend the bridge contract (TS)

**Files:**
- Modify: `desktop/web-grid/src/contracts.ts`

**Interfaces:**
- Produces: `TableFieldType`, `TABLE_FIELD_TYPES`, `TABLE_NAME_PATTERN` constants; `TableAdminCreatePayload`, `TableAdminDeletePayload`, `CollectionsChangedPayload` interfaces; new entries in `WebMessageType` / `HostMessageType` unions and `WebPayloadMap` / `HostPayloadMap`.

- [ ] **Step 1: Add field-type + pattern constants**

In `desktop/web-grid/src/contracts.ts`, add near the other domain constants (after the existing type/union block, before the payload maps):

```ts
/** Field types supported by the backend table_admin contract.
 *  Mirrors backend/contracts/table_admin.py:FieldType and
 *  TableAdminWindow.SupportedFieldTypes. Keep all three in sync. */
export const TABLE_FIELD_TYPES = [
  "string",
  "integer",
  "decimal",
  "date",
  "boolean",
  "text",
] as const;
export type TableFieldType = (typeof TABLE_FIELD_TYPES)[number];

/** Identifier rule for table names and field keys.
 *  Mirrors backend/contracts/table_admin.py:_IDENTIFIER. */
export const TABLE_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
```

- [ ] **Step 2: Add the two new outbound message types**

In the `WebMessageType` union (search for `type WebMessageType =`), add these two members to the union (before the closing of the union):

```ts
  | "tableAdmin.createRequested"
  | "tableAdmin.deleteRequested"
```

- [ ] **Step 3: Add the one new inbound message type**

In the `HostMessageType` union (search for `type HostMessageType =`), add:

```ts
  | "database.collectionsChanged"
```

- [ ] **Step 4: Add payload interfaces**

Add near the other payload interfaces:

```ts
export interface TableAdminFieldInput {
  readonly key: string;
  readonly type: TableFieldType;
}
export interface TableAdminCreatePayload {
  readonly name: string;
  readonly fields: readonly TableAdminFieldInput[];
}
export interface TableAdminDeletePayload {
  readonly collection: string;
}
export interface CollectionsChangedPayload {
  readonly tables: readonly string[];
  readonly capabilityHashes?: Readonly<Record<string, string>>;
}
```

- [ ] **Step 5: Register payloads in the maps**

In `WebPayloadMap` (search for `WebPayloadMap`), add:

```ts
  "tableAdmin.createRequested": TableAdminCreatePayload;
  "tableAdmin.deleteRequested": TableAdminDeletePayload;
```

In `HostPayloadMap` (search for `HostPayloadMap`), add:

```ts
  "database.collectionsChanged": CollectionsChangedPayload;
```

- [ ] **Step 6: Typecheck**

```bash
cd desktop/web-grid && npm run build
```
Expected: `tsc --noEmit` passes (vite build also runs; that's fine).

- [ ] **Step 7: Commit**

```bash
git add desktop/web-grid/src/contracts.ts
git commit -m "feat(contracts): add tableAdmin + collectionsChanged bridge types"
```

---

## Task 4: Extend runtime whitelists (TS)

**Files:**
- Modify: `desktop/web-grid/src/hostBridge.ts`

**Interfaces:**
- Consumes: the new message types from Task 3.
- Produces: `bridge.request("tableAdmin.createRequested", payload)` and `bridge.request("tableAdmin.deleteRequested", payload)` become accepted at runtime; `bridge.on("database.collectionsChanged", ...)` becomes accepted at runtime.

- [ ] **Step 1: Read the runtime Sets**

In `desktop/web-grid/src/hostBridge.ts`, find `WEB_MESSAGE_TYPES` (a `Set<string>`) and `HOST_EVENT_TYPES` (a `Set<string>`), around lines 107-143.

- [ ] **Step 2: Add the two outbound types to `WEB_MESSAGE_TYPES`**

Add inside the `WEB_MESSAGE_TYPES` set literal:

```ts
  "tableAdmin.createRequested",
  "tableAdmin.deleteRequested",
```

- [ ] **Step 3: Add the inbound type to `HOST_EVENT_TYPES`**

Add inside the `HOST_EVENT_TYPES` set literal:

```ts
  "database.collectionsChanged",
```

- [ ] **Step 4: Typecheck + run existing tests**

```bash
cd desktop/web-grid && npm run build && npm test
```
Expected: build passes; all existing vitest tests still pass (no regressions — these Sets are additive).

- [ ] **Step 5: Commit**

```bash
git add desktop/web-grid/src/hostBridge.ts
git commit -m "feat(hostBridge): whitelist tableAdmin + collectionsChanged at runtime"
```

---

## Task 5: TS validation module (TDD)

**Files:**
- Create test: `desktop/web-grid/src/tableAdminValidation.test.ts`
- Create: `desktop/web-grid/src/tableAdminValidation.ts`

**Interfaces:**
- Produces: `validateTableName(name) → string | null` (null = valid, string = error message); `validateFields(rows) → { fields: TableAdminFieldInput[]; errors: string[] }` (skips blank-named rows, validates non-blank names against the pattern).

- [ ] **Step 1: Write the failing test**

Create `desktop/web-grid/src/tableAdminValidation.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import {
  TABLE_FIELD_TYPES,
  TABLE_NAME_PATTERN,
  validateFields,
  validateTableName,
} from "./tableAdminValidation";

describe("validateTableName", () => {
  it("accepts a valid identifier", () => {
    expect(validateTableName("projects")).toBeNull();
    expect(validateTableName("my_table_2")).toBeNull();
  });

  it("rejects empty/whitespace", () => {
    expect(validateTableName("")).not.toBeNull();
    expect(validateTableName("   ")).not.toBeNull();
  });

  it("rejects names starting with a digit or underscore", () => {
    expect(validateTableName("1table")).not.toBeNull();
    expect(validateTableName("_private")).not.toBeNull();
  });

  it("rejects names over 64 chars", () => {
    expect(validateTableName("a".repeat(65))).not.toBeNull();
    expect(validateTableName("a".repeat(64))).toBeNull();
  });
});

describe("validateFields", () => {
  it("skips rows whose key is blank", () => {
    const result = validateFields([
      { key: "  ", type: "string" },
      { key: "name", type: "string" },
    ]);
    expect(result.errors).toEqual([]);
    expect(result.fields).toEqual([{ key: "name", type: "string" }]);
  });

  it("rejects a non-blank invalid key and returns no fields for it", () => {
    const result = validateFields([{ key: "1bad", type: "string" }]);
    expect(result.errors.length).toBe(1);
    expect(result.fields).toEqual([]);
  });

  it("trims keys", () => {
    const result = validateFields([{ key: "  name  ", type: "string" }]);
    expect(result.fields).toEqual([{ key: "name", type: "string" }]);
  });
});

describe("TABLE_FIELD_TYPES / TABLE_NAME_PATTERN", () => {
  it("exposes exactly the six backend field types", () => {
    expect(TABLE_FIELD_TYPES).toEqual([
      "string",
      "integer",
      "decimal",
      "date",
      "boolean",
      "text",
    ]);
  });

  it("TABLE_NAME_PATTERN matches a valid name", () => {
    expect(TABLE_NAME_PATTERN.test("good_name1")).toBe(true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd desktop/web-grid && npx vitest run src/tableAdminValidation.test.ts
```
Expected: FAIL — `Cannot find module './tableAdminValidation'`.

- [ ] **Step 3: Write minimal implementation**

Create `desktop/web-grid/src/tableAdminValidation.ts`:

```ts
import {
  TABLE_FIELD_TYPES,
  TABLE_NAME_PATTERN,
  type TableAdminFieldInput,
  type TableFieldType,
} from "./contracts";

export { TABLE_FIELD_TYPES, TABLE_NAME_PATTERN };

/** Returns null if valid, or a human-readable error message if invalid. */
export function validateTableName(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed.length === 0) {
    return "请输入表名。";
  }
  if (!TABLE_NAME_PATTERN.test(trimmed)) {
    return "表名只能用英文字母、数字和下划线，且必须以字母开头（最多 64 个字符）。";
  }
  return null;
}

export interface ValidatedFields {
  readonly fields: TableAdminFieldInput[];
  readonly errors: string[];
}

/**
 * Validate field rows. Rows whose key is blank/whitespace are SKIPPED
 * (matching the legacy TableAdminWindow behavior). Non-blank keys are
 * trimmed and validated; invalid keys produce an error and are excluded.
 * Types are assumed already constrained to the union by the UI <select>.
 */
export function validateFields(
  rows: ReadonlyArray<{ key: string; type: TableFieldType }>,
): ValidatedFields {
  const fields: TableAdminFieldInput[] = [];
  const errors: string[] = [];
  for (const row of rows) {
    const key = row.key.trim();
    if (key.length === 0) {
      continue;
    }
    if (!TABLE_NAME_PATTERN.test(key)) {
      errors.push(`字段名『${row.key}』无效：只能用英文字母、数字和下划线，且必须以字母开头。`);
      continue;
    }
    fields.push({ key, type: row.type });
  }
  return { fields, errors };
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd desktop/web-grid && npx vitest run src/tableAdminValidation.test.ts
```
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add desktop/web-grid/src/tableAdminValidation.ts desktop/web-grid/src/tableAdminValidation.test.ts
git commit -m "feat(tableAdmin): add client-side name/field validation"
```

---

## Task 6: TS `tableAdminFlow` state machine (TDD)

**Files:**
- Create test: `desktop/web-grid/src/tableAdminFlow.test.ts`
- Create: `desktop/web-grid/src/tableAdminFlow.ts`

**Interfaces:**
- Consumes: `TableAdminCreatePayload`, `TableAdminDeletePayload`, `CollectionsChangedPayload` from `contracts.ts` (Task 3); a bridge-like object with `request(type, payload): Promise<void>`.
- Produces: `TableAdminState`, `initialTableAdminState`, reducer functions (`applyCreateStarted`, `applyCreateSucceeded`, `applyCreateFailed`, `applyDeleteStarted`, `applyDeleteSucceeded`, `applyDeleteFailed`, `applyCollectionsChanged`), and async orchestrators (`requestCreate`, `requestDelete`).

- [ ] **Step 1: Write the failing test**

Create `desktop/web-grid/src/tableAdminFlow.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import {
  applyCollectionsChanged,
  applyCreateFailed,
  applyCreateStarted,
  applyCreateSucceeded,
  applyDeleteFailed,
  applyDeleteStarted,
  applyDeleteSucceeded,
  initialTableAdminState,
  requestCreate,
  requestDelete,
} from "./tableAdminFlow";
import type { HostBridgeLike } from "./tableAdminFlow";

function makeBridge(): HostBridgeLike & {
  requests: Array<{ type: string; payload: unknown }>;
  rejectNextWith?: Error;
} {
  const requests: Array<{ type: string; payload: unknown }> = [];
  return {
    requests,
    async request(type, payload) {
      requests.push({ type, payload });
      return undefined;
    },
  };
}

describe("reducers", () => {
  it("applyCollectionsChanged sets tables and clears status", () => {
    const next = applyCollectionsChanged(initialTableAdminState, ["a", "b"]);
    expect(next.collections).toEqual(["a", "b"]);
    expect(next.status).toBe("idle");
    expect(next.error).toBeNull();
  });

  it("create lifecycle: started → succeeded → idle", () => {
    const started = applyCreateStarted(initialTableAdminState);
    expect(started.status).toBe("creating");
    const succeeded = applyCreateSucceeded(started);
    expect(succeeded.status).toBe("idle");
  });

  it("createFailed stores the error message", () => {
    const started = applyCreateStarted(initialTableAdminState);
    const failed = applyCreateFailed(started, "boom");
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("boom");
  });

  it("delete lifecycle mirrors create", () => {
    const started = applyDeleteStarted(initialTableAdminState);
    expect(started.status).toBe("deleting");
    const failed = applyDeleteFailed(started, "nope");
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("nope");
    const succeeded = applyDeleteSucceeded(applyDeleteStarted(initialTableAdminState));
    expect(succeeded.status).toBe("idle");
  });
});

describe("orchestrators", () => {
  it("requestCreate posts createRequested and resolves on success", async () => {
    const bridge = makeBridge();
    const events: string[] = [];
    await requestCreate(bridge, "projects", [{ key: "name", type: "string" }], (e) =>
      events.push(e.type),
    );
    expect(bridge.requests).toEqual([
      { type: "tableAdmin.createRequested", payload: { name: "projects", fields: [{ key: "name", type: "string" }] } },
    ]);
    expect(events).toEqual(["createStarted", "createSucceeded"]);
  });

  it("requestCreate dispatches createFailed on rejection", async () => {
    const bridge = makeBridge();
    bridge.request = async () => {
      throw new Error("backend said no");
    };
    const events: string[] = [];
    await requestCreate(bridge, "x", [], (e) => events.push(e.type));
    expect(events).toEqual(["createStarted", "createFailed"]);
  });

  it("requestDelete posts deleteRequested", async () => {
    const bridge = makeBridge();
    const events: string[] = [];
    await requestDelete(bridge, "old_table", (e) => events.push(e.type));
    expect(bridge.requests).toEqual([
      { type: "tableAdmin.deleteRequested", payload: { collection: "old_table" } },
    ]);
    expect(events).toEqual(["deleteStarted", "deleteSucceeded"]);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd desktop/web-grid && npx vitest run src/tableAdminFlow.test.ts
```
Expected: FAIL — `Cannot find module './tableAdminFlow'`.

- [ ] **Step 3: Write minimal implementation**

Create `desktop/web-grid/src/tableAdminFlow.ts`:

```ts
/**
 * tableAdminFlow — sidebar state machine for the table-management UI.
 *
 * Pattern: pure reducer functions (no I/O) + thin async orchestrators that
 * call the bridge and dispatch reducer events. Modeled on fieldHistoryFlow.
 *
 * State is held by the caller (main.ts); the flow does not retain state.
 * The table list is NOT updated on create/delete success here — the host
 * pushes `database.collectionsChanged` after a successful mutation, and
 * `applyCollectionsChanged` is what updates `collections`.
 */
import type {
  CollectionsChangedPayload,
  TableAdminCreatePayload,
  TableAdminDeletePayload,
} from "./contracts";

export type TableAdminStatus = "idle" | "creating" | "deleting" | "error";

export interface TableAdminState {
  readonly collections: readonly string[];
  readonly status: TableAdminStatus;
  readonly error: string | null;
}

export const initialTableAdminState: TableAdminState = {
  collections: [],
  status: "idle",
  error: null,
};

/** Minimal bridge surface this flow needs. HostBridge satisfies this. */
export interface HostBridgeLike {
  request<T = void>(
    type: "tableAdmin.createRequested",
    payload: TableAdminCreatePayload,
  ): Promise<T>;
  request<T = void>(
    type: "tableAdmin.deleteRequested",
    payload: TableAdminDeletePayload,
  ): Promise<T>;
}

export type TableAdminEvent =
  | { readonly type: "createStarted" }
  | { readonly type: "createSucceeded" }
  | { readonly type: "createFailed"; readonly message: string }
  | { readonly type: "deleteStarted" }
  | { readonly type: "deleteSucceeded" }
  | { readonly type: "deleteFailed"; readonly message: string }
  | {
      readonly type: "collectionsChanged";
      readonly tables: readonly string[];
    };

// --- pure reducers ---

export function applyCreateStarted(s: TableAdminState): TableAdminState {
  return { ...s, status: "creating", error: null };
}
export function applyCreateSucceeded(s: TableAdminState): TableAdminState {
  return { ...s, status: "idle", error: null };
}
export function applyCreateFailed(s: TableAdminState, message: string): TableAdminState {
  return { ...s, status: "error", error: message };
}
export function applyDeleteStarted(s: TableAdminState): TableAdminState {
  return { ...s, status: "deleting", error: null };
}
export function applyDeleteSucceeded(s: TableAdminState): TableAdminState {
  return { ...s, status: "idle", error: null };
}
export function applyDeleteFailed(s: TableAdminState, message: string): TableAdminState {
  return { ...s, status: "error", error: message };
}
export function applyCollectionsChanged(
  s: TableAdminState,
  tables: readonly string[],
): TableAdminState {
  return { ...s, collections: tables, status: "idle", error: null };
}

export function reduce(state: TableAdminState, event: TableAdminEvent): TableAdminState {
  switch (event.type) {
    case "createStarted":
      return applyCreateStarted(state);
    case "createSucceeded":
      return applyCreateSucceeded(state);
    case "createFailed":
      return applyCreateFailed(state, event.message);
    case "deleteStarted":
      return applyDeleteStarted(state);
    case "deleteSucceeded":
      return applyDeleteSucceeded(state);
    case "deleteFailed":
      return applyDeleteFailed(state, event.message);
    case "collectionsChanged":
      return applyCollectionsChanged(state, event.tables);
  }
}

// --- async orchestrators ---

export async function requestCreate(
  bridge: HostBridgeLike,
  name: string,
  fields: TableAdminCreatePayload["fields"],
  dispatch: (event: TableAdminEvent) => void,
): Promise<void> {
  dispatch({ type: "createStarted" });
  try {
    await bridge.request("tableAdmin.createRequested", { name, fields });
    dispatch({ type: "createSucceeded" });
  } catch (e) {
    dispatch({ type: "createFailed", message: (e as Error).message });
  }
}

export async function requestDelete(
  bridge: HostBridgeLike,
  collection: string,
  dispatch: (event: TableAdminEvent) => void,
): Promise<void> {
  dispatch({ type: "deleteStarted" });
  try {
    await bridge.request("tableAdmin.deleteRequested", { collection });
    dispatch({ type: "deleteSucceeded" });
  } catch (e) {
    dispatch({ type: "deleteFailed", message: (e as Error).message });
  }
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd desktop/web-grid && npx vitest run src/tableAdminFlow.test.ts
```
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add desktop/web-grid/src/tableAdminFlow.ts desktop/web-grid/src/tableAdminFlow.test.ts
git commit -m "feat(tableAdmin): add sidebar state machine (reducers + orchestrators)"
```

---

## Task 7: Extend C# whitelists (TDD)

**Files:**
- Modify test: `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs`
- Modify: `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs`

**Interfaces:**
- Produces: `WebMessageRouter` accepts the two new inbound request types and allows the one new outbound notification.

- [ ] **Step 1: Read the existing whitelist test**

In `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs`, find `IsHostNotificationAllowed_ReturnsTrue_OnlyForKnownNotifications` (around line 202) and any inbound-whitelist test, to mirror their style.

- [ ] **Step 2: Add failing assertions**

Add (or extend) a test method in `WebMessageRouterTests`:

```csharp
[TestMethod]
public void Whitelists_AcceptTableAdminRequestsAndCollectionsChangedNotification()
{
    var router = new WebMessageRouter(_ => { });

    // Inbound: tableAdmin requests are accepted (return null reply = dispatched).
    var createReq = new RoutedWebRequest(
        "tableAdmin.createRequested", "req-c",
        JsonDocument.Parse("""{"name":"t","fields":[]}""").RootElement.Clone(), "");
    Assert.IsNull(router.Route(CreateRaw("tableAdmin.createRequested", "req-c")),
        "tableAdmin.createRequested should be whitelisted inbound");

    var deleteReq = new RoutedWebRequest(
        "tableAdmin.deleteRequested", "req-d",
        JsonDocument.Parse("""{"collection":"t"}""").RootElement.Clone(), "");
    Assert.IsNull(router.Route(CreateRaw("tableAdmin.deleteRequested", "req-d")),
        "tableAdmin.deleteRequested should be whitelisted inbound");

    // Outbound: collectionsChanged notification is allowed.
    Assert.IsTrue(WebMessageRouter.IsHostNotificationAllowed("database.collectionsChanged"));
}

private static string CreateRaw(string type, string requestId)
    => $$"""{"type":"{{type}}","requestId":"{{requestId}}","payload":{}}""";
```

Note: inspect the existing tests for the exact way they construct `RoutedWebRequest` and call `Route` — some tests pass `Raw` and let the router parse, others construct the record directly. Mirror whichever pattern the existing inbound-acceptance test uses. The key assertion is `IsHostNotificationAllowed("database.collectionsChanged")` returns `true`, and the two inbound types are accepted (not rejected as out-of-whitelist).

- [ ] **Step 3: Run test to verify it fails**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~Whitelists_AcceptTableAdmin"
```
Expected: FAIL — the new types are rejected/dropped.

- [ ] **Step 4: Add the three whitelist entries**

In `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs`, in `WebRequestWhitelist` (around line 52-68), add:

```csharp
        // Table management (web sidebar).
        "tableAdmin.createRequested",
        "tableAdmin.deleteRequested",
```

In `HostNotificationWhitelist` (around line 76-93), add:

```csharp
        // Table management: host pushes refreshed collection list after create/delete.
        "database.collectionsChanged",
```

- [ ] **Step 5: Run test to verify it passes**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~Whitelists_AcceptTableAdmin"
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs
git commit -m "feat(router): whitelist tableAdmin requests + collectionsChanged notification"
```

---

## Task 8: C# dispatcher create/delete handlers (TDD)

**Files:**
- Create: `desktop/tests/VibeTable.Desktop.Tests/FakeDirectusRpcGateway.cs`
- Modify test: `desktop/tests/VibeTable.Desktop.Tests/WorkspaceRequestDispatcherTests.cs`
- Modify: `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs`

**Interfaces:**
- Consumes: `IDirectusRpcGateway` (`ListCollectionsAsync`, `CreateTableAsync`, `DeleteTableAsync`), `DirectusCollectionFilter` (Task 1), `IWebReplySink`, `RoutedWebRequest`, helper extractors `TryGetString`/`TryGetProperty`.
- Produces: `WorkspaceRequestDispatcher.SetDirectusGateway(IDirectusRpcGateway)`; handler methods invoked for `tableAdmin.createRequested` / `tableAdmin.deleteRequested` that post `database.collectionsChanged` on success or `operation.failed` on failure / null-gateway.

- [ ] **Step 1: Create the fake gateway**

Create `desktop/tests/VibeTable.Desktop.Tests/FakeDirectusRpcGateway.cs`:

```csharp
using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// In-memory IDirectusRpcGateway for dispatcher tests. Records calls and
/// returns programmable results. Throws by default for methods the test
/// does not configure (so accidental calls are loud).
/// </summary>
internal sealed class FakeDirectusRpcGateway : IDirectusRpcGateway
{
    public List<string> ListCollectionsCalls { get; } = new();
    public DirectusCollectionList ListCollectionsResult { get; set; }
    public Exception? ListCollectionsException { get; set; }

    public List<(string Name, IReadOnlyList<FieldDefinition> Fields)> CreateTableCalls { get; } = new();
    public CreateTableResult? CreateTableResult { get; set; }
    public Exception? CreateTableException { get; set; }

    public List<string> DeleteTableCalls { get; } = new();
    public DeleteTableResult? DeleteTableResult { get; set; }
    public Exception? DeleteTableException { get; set; }

    public Task<DirectusCollectionList> ListCollectionsAsync(CancellationToken token)
    {
        ListCollectionsCalls.Add("list");
        if (ListCollectionsException is not null) throw ListCollectionsException;
        return Task.FromResult(ListCollectionsResult);
    }

    public Task<CreateTableResult> CreateTableAsync(
        string name, IReadOnlyList<FieldDefinition> fields, CancellationToken token)
    {
        CreateTableCalls.Add((name, fields));
        if (CreateTableException is not null) throw CreateTableException;
        return Task.FromResult(CreateTableResult
            ?? new CreateTableResult(name, "id", new[] { "id" }));
    }

    public Task<DeleteTableResult> DeleteTableAsync(string name, CancellationToken token)
    {
        DeleteTableCalls.Add(name);
        if (DeleteTableException is not null) throw DeleteTableException;
        return Task.FromResult(DeleteTableResult
            ?? new DeleteTableResult(name, Deleted: true));
    }

    // The rest of the interface is unused by the dispatcher; throw to keep tests honest.
    public event Action<DirectusChange>? Changed { add { } remove { } }
    public Task<DirectusSessionStatus> LoginAsync(string e, string p, string? o, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> RefreshAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> LogoutAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> GetStatusAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusServerInfo> GetServerInfoAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusCurrentUser> GetCurrentUserAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSchema> GetSchemaAsync(string c, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusPage> ReadAsync(string c, TableQuery q, bool a, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> CreateAsync(string c, IReadOnlyDictionary<string, object?> v, string? r, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> UpdateAsync(string c, string i, IReadOnlyDictionary<string, object?> v, string? d, string? r, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> ArchiveAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> RestoreAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> DeleteAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSubscription> SubscribeAsync(string u, string c, IReadOnlyList<string> f, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSubscription> UnsubscribeAsync(string u, CancellationToken t) => throw new NotImplementedException();
    public void Dispose() { }
}
```

- [ ] **Step 2: Write failing dispatcher tests**

Append to `desktop/tests/VibeTable.Desktop.Tests/WorkspaceRequestDispatcherTests.cs` (inside the test class):

```csharp
[TestMethod]
public async Task CreateTableRequested_WhenGatewayNull_PostsNotAuthenticated()
{
    var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
    var sink = new FakeWebReplySink();
    var dispatcher = new WorkspaceRequestDispatcher(
        workspace, new FakeDatabasePicker("db"), sink);
    // No SetDirectusGateway call -> gateway is null.

    var payload = JsonDocument.Parse(
        """{"name":"projects","fields":[{"key":"name","type":"string"}]}""").RootElement.Clone();
    dispatcher.Dispatch(new RoutedWebRequest(
        "tableAdmin.createRequested", "req-c", payload, ""));

    var failed = await sink.WaitForFailedAsync();
    Assert.IsNotNull(failed);
    Assert.AreEqual("NOT_AUTHENTICATED", ((dynamic)failed!.Payload).code);
}

[TestMethod]
public async Task CreateTableRequested_CallsGatewayAndPostsCollectionsChanged()
{
    var directus = new FakeDirectusRpcGateway
    {
        // After create, list returns these (incl. system tables to prove filtering).
        ListCollectionsResult = new DirectusCollectionList(
            new[] { "projects", "directus_users", "tasks" },
            new Dictionary<string, string>()),
    };
    var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
    var sink = new FakeWebReplySink();
    var dispatcher = new WorkspaceRequestDispatcher(
        workspace, new FakeDatabasePicker("db"), sink);
    dispatcher.SetDirectusGateway(directus);

    var payload = JsonDocument.Parse(
        """{"name":"projects","fields":[{"key":"name","type":"string"}]}""").RootElement.Clone();
    dispatcher.Dispatch(new RoutedWebRequest(
        "tableAdmin.createRequested", "req-c", payload, ""));

    var notif = await sink.WaitForAsync("database.collectionsChanged");
    Assert.IsNotNull(notif);
    Assert.AreEqual(1, directus.CreateTableCalls.Count);
    Assert.AreEqual("projects", directus.CreateTableCalls[0].Name);
    // The notification payload must contain the FILTERED + SORTED list
    // (directus_users removed; projects before tasks).
    dynamic payload = notif!.Payload!;
    var tables = (System.Collections.Generic.List<string>)payload.tables;
    CollectionAssert.AreEqual(new[] { "projects", "tasks" }, tables);
}

[TestMethod]
public async Task DeleteTableRequested_CallsGatewayAndPostsCollectionsChanged()
{
    var directus = new FakeDirectusRpcGateway
    {
        DeleteTableResult = new DeleteTableResult("old", Deleted: true),
        ListCollectionsResult = new DirectusCollectionList(
            new[] { "remaining" }, new Dictionary<string, string>()),
    };
    var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
    var sink = new FakeWebReplySink();
    var dispatcher = new WorkspaceRequestDispatcher(
        workspace, new FakeDatabasePicker("db"), sink);
    dispatcher.SetDirectusGateway(directus);

    var payload = JsonDocument.Parse("""{"collection":"old"}""").RootElement.Clone();
    dispatcher.Dispatch(new RoutedWebRequest(
        "tableAdmin.deleteRequested", "req-d", payload, ""));

    var notif = await sink.WaitForAsync("database.collectionsChanged");
    Assert.IsNotNull(notif);
    Assert.AreEqual(1, directus.DeleteTableCalls.Count);
    Assert.AreEqual("old", directus.DeleteTableCalls[0]);
}

[TestMethod]
public async Task CreateTableRequested_OnBackendError_PostsOperationFailed()
{
    var directus = new FakeDirectusRpcGateway
    {
        CreateTableException = new InvalidOperationException("name already exists"),
    };
    var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
    var sink = new FakeWebReplySink();
    var dispatcher = new WorkspaceRequestDispatcher(
        workspace, new FakeDatabasePicker("db"), sink);
    dispatcher.SetDirectusGateway(directus);

    var payload = JsonDocument.Parse(
        """{"name":"x","fields":[]}""").RootElement.Clone();
    dispatcher.Dispatch(new RoutedWebRequest(
        "tableAdmin.createRequested", "req-c", payload, ""));

    var failed = await sink.WaitForFailedAsync();
    Assert.IsNotNull(failed);
    StringAssert.Contains((string)((dynamic)failed!.Payload).message, "name already exists");
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~CreateTableRequested|FullyQualifiedName~DeleteTableRequested"
```
Expected: FAIL — `SetDirectusGateway` does not exist; switch cases missing.

- [ ] **Step 4: Add the gateway field + setter + two switch cases + two handlers**

In `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs`:

4a. Add a nullable field and setter after `_coordinator` (around line 48):

```csharp
    private IDirectusRpcGateway? _directusGateway;

    /// <summary>
    /// Injects the Directus RPC gateway used by table-management handlers.
    /// Called by MainWindow after the session is authenticated; null before
    /// that (handlers return operation.failed code NOT_AUTHENTICATED).
    /// </summary>
    public void SetDirectusGateway(IDirectusRpcGateway gateway)
        => _directusGateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
```

Add the necessary usings at the top if `IDirectusRpcGateway` is not already in scope (the file is in `VibeTable.Desktop.Services`, same namespace as the interface, so it should resolve; `FieldDefinition`/`CreateTableResult` are in `VibeTable.Contracts` — check the existing `using VibeTable.Contracts;` is present, it is).

4b. Add two cases to the `DispatchAsync` switch (before the `default:`):

```csharp
            case "tableAdmin.createRequested":
                await OnCreateTableRequestedAsync(request).ConfigureAwait(false);
                break;
            case "tableAdmin.deleteRequested":
                await OnDeleteTableRequestedAsync(request).ConfigureAwait(false);
                break;
```

4c. Add the two handler methods (near the other `On...Async` handlers, e.g. after `OnApplyPasteRequestedAsync`):

```csharp
    private async Task OnCreateTableRequestedAsync(RoutedWebRequest request)
    {
        if (_directusGateway is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Directus 尚未登录。", code: "NOT_AUTHENTICATED");
            return;
        }
        string? name = TryGetString(request.Payload, "name");
        if (string.IsNullOrWhiteSpace(name))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少表名。", code: "BAD_PAYLOAD");
            return;
        }
        var fields = new List<FieldDefinition>();
        if (TryGetProperty(request.Payload, "fields", out var fieldsEl)
            && fieldsEl.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in fieldsEl.EnumerateArray())
            {
                string? key = item.ValueKind == JsonValueKind.Object
                    && item.TryGetProperty("key", out var kEl) && kEl.ValueKind == JsonValueKind.String
                    ? kEl.GetString() : null;
                string? type = item.ValueKind == JsonValueKind.Object
                    && item.TryGetProperty("type", out var tEl) && tEl.ValueKind == JsonValueKind.String
                    ? tEl.GetString() : null;
                if (!string.IsNullOrWhiteSpace(key) && !string.IsNullOrWhiteSpace(type))
                {
                    fields.Add(new FieldDefinition(key!, type!));
                }
            }
        }
        try
        {
            await _directusGateway.CreateTableAsync(name, fields, CancellationToken.None)
                .ConfigureAwait(false);
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "CREATE_TABLE_FAILED");
        }
    }

    private async Task OnDeleteTableRequestedAsync(RoutedWebRequest request)
    {
        if (_directusGateway is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Directus 尚未登录。", code: "NOT_AUTHENTICATED");
            return;
        }
        string? collection = TryGetString(request.Payload, "collection");
        if (string.IsNullOrWhiteSpace(collection))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少表名。", code: "BAD_PAYLOAD");
            return;
        }
        try
        {
            await _directusGateway.DeleteTableAsync(collection, CancellationToken.None)
                .ConfigureAwait(false);
            await PostCollectionsChangedAsync().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _reply.PostOperationFailed(request.RequestId, ex.Message, code: "DELETE_TABLE_FAILED");
        }
    }

    /// <summary>
    /// Re-lists collections, filters to user tables, and pushes
    /// database.collectionsChanged so the sidebar refreshes.
    /// </summary>
    private async Task PostCollectionsChangedAsync()
    {
        var list = await _directusGateway!.ListCollectionsAsync(CancellationToken.None)
            .ConfigureAwait(false);
        var tables = DirectusCollectionFilter.FilterUserTables(list.Collections);
        _reply.PostNotification("database.collectionsChanged", new
        {
            tables,
            capabilityHashes = list.CapabilityHashes,
        });
    }
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~CreateTableRequested|FullyQualifiedName~DeleteTableRequested"
```
Expected: PASS (4 tests).

- [ ] **Step 6: Run the full Desktop.Tests suite to check for regressions**

```bash
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj
```
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs desktop/tests/VibeTable.Desktop.Tests/FakeDirectusRpcGateway.cs desktop/tests/VibeTable.Desktop.Tests/WorkspaceRequestDispatcherTests.cs
git commit -m "feat(dispatcher): handle tableAdmin create/delete, push collectionsChanged"
```

---

## Task 9: Wire gateway setter in MainWindow (C#)

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`

**Interfaces:**
- Consumes: `WorkspaceRequestDispatcher.SetDirectusGateway` (Task 8); `_directusGateway` created in `EnsureDirectusSessionAsync` (~line 642).

- [ ] **Step 1: Locate the gateway creation site**

In `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`, `EnsureDirectusSessionAsync` (~line 640-644):

```csharp
if (_directusGateway is null)
{
    _directusGateway = new JsonRpcDirectusGateway(client);
    _directusGateway.Changed += OnDirectusChanged;
}
```

- [ ] **Step 2: Add the setter call right after gateway creation**

Change to:

```csharp
if (_directusGateway is null)
{
    _directusGateway = new JsonRpcDirectusGateway(client);
    _directusGateway.Changed += OnDirectusChanged;
    _dispatcher.SetDirectusGateway(_directusGateway);
}
```

- [ ] **Step 3: Build to verify it compiles**

```bash
dotnet build desktop/VibeTable.sln
```
Expected: Build succeeded.

- [ ] **Step 4: Commit**

```bash
git add desktop/src/VibeTable.Desktop/MainWindow.xaml.cs
git commit -m "feat(main): inject directus gateway into workspace dispatcher"
```

---

## Task 10: Sidebar markup + styles (TS)

**Files:**
- Modify: `desktop/web-grid/index.html`
- Modify: `desktop/web-grid/src/styles.css`

**Interfaces:**
- Produces: DOM elements `#sidebar`, `#new-table-btn`, `#table-list`, and modal containers (`#create-table-modal`, `#delete-confirm-modal`) for `main.ts` to wire in Task 12. Removes `#table-select` from the toolbar.

- [ ] **Step 1: Restructure index.html**

In `desktop/web-grid/index.html`, change `<div id="app">` to wrap the existing content in `#main` and prepend a `#sidebar`. Replace the existing `<div id="app"> ... </div>` block with:

```html
    <div id="app">
      <aside id="sidebar" class="sidebar">
        <div class="sidebar__head">
          <span class="sidebar__title">表</span>
          <button id="new-table-btn" type="button" class="sidebar__new-btn" disabled>+ 新建表</button>
        </div>
        <ul id="table-list" class="table-list"></ul>
      </aside>
      <div id="main">
        <div id="toolbar" class="toolbar">
          <button id="open-database" type="button" disabled>连接 Directus</button>
          <button id="refresh" type="button" disabled>刷新</button>
          <span id="row-count" class="row-count" aria-live="polite"></span>
        </div>
        <div id="status" class="status">Loading…</div>
        <div id="grid-wrapper" class="grid-wrapper">
          <div id="loading-overlay" class="overlay overlay--loading" hidden>
            <span>加载中…</span>
          </div>
          <div id="error-overlay" class="overlay overlay--error" hidden>
            <span id="error-message"></span>
          </div>
          <div id="grid"></div>
        </div>
      </div>
      <!-- Create-table modal -->
      <div id="create-table-modal" class="modal" hidden>
        <div class="modal__panel">
          <div class="modal__head">
            <h2 class="modal__title">新建表</h2>
            <button id="create-table-close" type="button" class="modal__close" aria-label="关闭">×</button>
          </div>
          <div class="modal__body">
            <label class="field">
              <span class="field__label">表名</span>
              <input id="create-table-name" type="text" class="field__input" maxlength="64" />
            </label>
            <div id="create-table-fields" class="field-rows"></div>
            <button id="create-table-add-field" type="button" class="field__add">+ 添加字段</button>
            <div id="create-table-error" class="modal__error" hidden></div>
          </div>
          <div class="modal__actions">
            <button id="create-table-cancel" type="button" class="btn btn--secondary">取消</button>
            <button id="create-table-submit" type="button" class="btn btn--primary" disabled>创建</button>
          </div>
        </div>
      </div>
      <!-- Delete-confirm modal -->
      <div id="delete-confirm-modal" class="modal" hidden>
        <div class="modal__panel">
          <div class="modal__body">
            <p id="delete-confirm-text" class="modal__text"></p>
          </div>
          <div class="modal__actions">
            <button id="delete-confirm-cancel" type="button" class="btn btn--secondary">取消</button>
            <button id="delete-confirm-ok" type="button" class="btn btn--danger">删除</button>
          </div>
        </div>
      </div>
    </div>
```

This removes the old `#table-select` and its `<label>` from the toolbar (table switching now happens via the sidebar).

- [ ] **Step 2: Add styles**

Append to `desktop/web-grid/src/styles.css`:

```css
/* Sidebar + main layout. */
#app {
  flex-direction: row;
}
#main {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
}
.sidebar {
  flex: 0 0 220px;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--vibetable-border);
  background: #fafafa;
  overflow: hidden;
}
.sidebar__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px;
  border-bottom: 1px solid var(--vibetable-border);
}
.sidebar__title {
  font-weight: 600;
  color: var(--vibetable-fg);
}
.sidebar__new-btn {
  font: inherit;
  padding: 2px 8px;
  border: 1px solid var(--vibetable-border);
  border-radius: 4px;
  background: var(--vibetable-bg);
  color: var(--vibetable-fg);
  cursor: pointer;
}
.sidebar__new-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.table-list {
  list-style: none;
  margin: 0;
  padding: 4px;
  overflow-y: auto;
  flex: 1 1 auto;
}
.table-list__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px;
  border-radius: 4px;
  cursor: pointer;
  color: var(--vibetable-fg);
}
.table-list__item:hover {
  background: #eef2ff;
}
.table-list__item--active {
  background: #dbeafe;
  font-weight: 600;
}
.table-list__name {
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table-list__delete {
  border: none;
  background: transparent;
  color: var(--vibetable-muted);
  cursor: pointer;
  padding: 0 4px;
  border-radius: 4px;
}
.table-list__delete:hover {
  color: #991b1b;
  background: rgba(254, 226, 226, 0.6);
}

/* Modal (create-table, delete-confirm). */
.modal {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.35);
  z-index: 50;
}
.modal[hidden] {
  display: none;
}
.modal__panel {
  background: var(--vibetable-bg);
  border: 1px solid var(--vibetable-border);
  border-radius: 8px;
  min-width: 360px;
  max-width: 560px;
  padding: 16px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.2);
}
.modal__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.modal__title {
  margin: 0;
  font-size: 16px;
}
.modal__close {
  border: none;
  background: transparent;
  font-size: 20px;
  cursor: pointer;
  color: var(--vibetable-muted);
}
.modal__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.modal__text {
  margin: 0;
}
.modal__error {
  color: #991b1b;
  background: rgba(254, 226, 226, 0.9);
  padding: 6px 8px;
  border-radius: 4px;
}
.modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.field__label {
  font-size: 13px;
  color: var(--vibetable-muted);
}
.field__input,
.field__select {
  font: inherit;
  padding: 4px 6px;
  border: 1px solid var(--vibetable-border);
  border-radius: 4px;
}
.field-rows {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 240px;
  overflow-y: auto;
}
.field-row {
  display: grid;
  grid-template-columns: 1fr 140px auto;
  gap: 6px;
  align-items: center;
}
.field__add {
  align-self: flex-start;
  padding: 4px 8px;
  border: 1px dashed var(--vibetable-border);
  border-radius: 4px;
  background: var(--vibetable-bg);
  cursor: pointer;
}
.btn {
  font: inherit;
  padding: 6px 14px;
  border-radius: 4px;
  border: 1px solid var(--vibetable-border);
  cursor: pointer;
}
.btn--primary {
  background: var(--vibetable-accent);
  color: #fff;
  border-color: var(--vibetable-accent);
}
.btn--secondary {
  background: var(--vibetable-bg);
  color: var(--vibetable-fg);
}
.btn--danger {
  background: #dc2626;
  color: #fff;
  border-color: #dc2626;
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 3: Build to verify the HTML/CSS is valid**

```bash
cd desktop/web-grid && npm run build
```
Expected: build succeeds (the markup change may temporarily break `main.ts` references to `#table-select`; that is fixed in Task 12 — but the build will still pass because the references are runtime `getElementById` calls returning null, not compile errors. Verify there are no TS compile errors. If `main.ts` references to `#table-select` cause runtime issues, they are resolved in Task 12.)

- [ ] **Step 4: Commit**

```bash
git add desktop/web-grid/index.html desktop/web-grid/src/styles.css
git commit -m "feat(web-grid): add sidebar + modal markup and styles"
```

---

## Task 11: Sidebar rendering + state wiring (TS, `main.ts`)

**Files:**
- Modify: `desktop/web-grid/src/main.ts`

**Interfaces:**
- Consumes: `tableAdminFlow` (Task 6), `tableAdminValidation` (Task 5), bridge `request`/`on`, `database.opened` + `database.collectionsChanged` notifications.
- Produces: a working sidebar that lists tables (from `database.opened` + `collectionsChanged`), switches tables on click, enables `#new-table-btn` on `database.opened`, and opens the create/delete modals.

This task wires the sidebar and table-switching. The create/delete modal *actions* (calling `requestCreate`/`requestDelete`) are also wired here so the sidebar is fully functional in one task.

- [ ] **Step 1: Read the current `main.ts` structure**

Read `desktop/web-grid/src/main.ts` fully. Note:
- `populateTableSelect` (~line 89-105) populates `#table-select` — will be removed.
- `tableSelect.addEventListener("change", ...)` (~line 361-364) — will be removed.
- `bridge.on("database.opened", ...)` handler (~line 317) calls `flow.onDatabaseOpened` + `populateTableSelect`.
- `flow.selectTable(name, notify)` switches tables.

- [ ] **Step 2: Remove the `#table-select` references**

Delete the `populateTableSelect` function definition and its call site(s). Delete the `tableSelect` DOM lookup (the `const tableSelect = document.getElementById("table-select")...` line) and its `change` listener. Remove any `tableSelect.disabled = ...` / `tableSelect.innerHTML = ...` lines.

- [ ] **Step 3: Add sidebar DOM lookups at the top of the bootstrap section**

Near the other `const xxx = document.getElementById(...)` lines, add:

```ts
const sidebar = document.getElementById("sidebar");
const newTableBtn = document.getElementById("new-table-btn") as HTMLButtonElement | null;
const tableList = document.getElementById("table-list");
const createTableModal = document.getElementById("create-table-modal");
const createTableClose = document.getElementById("create-table-close");
const createTableName = document.getElementById("create-table-name") as HTMLInputElement | null;
const createTableFields = document.getElementById("create-table-fields");
const createTableAddField = document.getElementById("create-table-add-field");
const createTableError = document.getElementById("create-table-error");
const createTableCancel = document.getElementById("create-table-cancel");
const createTableSubmit = document.getElementById("create-table-submit") as HTMLButtonElement | null;
const deleteConfirmModal = document.getElementById("delete-confirm-modal");
const deleteConfirmText = document.getElementById("delete-confirm-text");
const deleteConfirmCancel = document.getElementById("delete-confirm-cancel");
const deleteConfirmOk = document.getElementById("delete-confirm-ok");
```

- [ ] **Step 4: Hold tableAdmin state + render function**

Add near the other state holders:

```ts
import {
  initialTableAdminState,
  reduce as reduceTableAdmin,
  requestCreate,
  requestDelete,
  type TableAdminEvent,
  type TableAdminState,
} from "./tableAdminFlow";
import {
  TABLE_FIELD_TYPES,
  validateFields,
  validateTableName,
} from "./tableAdminValidation";
import type { TableFieldType } from "./contracts";

let tableAdmin: TableAdminState = initialTableAdminState;
let pendingDeleteCollection: string | null = null;

function dispatchTableAdmin(event: TableAdminEvent): void {
  tableAdmin = reduceTableAdmin(tableAdmin, event);
  renderSidebar();
}

function renderSidebar(): void {
  if (!tableList) return;
  tableList.innerHTML = "";
  for (const name of tableAdmin.collections) {
    const li = document.createElement("li");
    li.className = "table-list__item";
    if (name === currentTableName) li.classList.add("table-list__item--active");
    const span = document.createElement("span");
    span.className = "table-list__name";
    span.textContent = name;
    span.addEventListener("click", () => {
      flow.selectTable(name, (type, payload) => bridge.notify(type, payload));
    });
    const del = document.createElement("button");
    del.type = "button";
    del.className = "table-list__delete";
    del.textContent = "删除";
    del.addEventListener("click", (e) => {
      e.stopPropagation();
      openDeleteConfirm(name);
    });
    li.appendChild(span);
    li.appendChild(del);
    tableList.appendChild(li);
  }
}
```

where `currentTableName` is whatever variable already holds the selected table name in `main.ts` (check `tableFlow` state — likely `flow.getState().currentTable`). Replace `currentTableName` with the correct accessor, e.g. `flow.getState().currentTable`. Keep this consistent.

- [ ] **Step 5: Subscribe to `database.collectionsChanged` + extend `database.opened`**

Find the existing `bridge.on("database.opened", ...)` and ensure it ALSO seeds the sidebar. Add a new subscription right after it:

```ts
bridge.on("database.collectionsChanged", (payload) => {
  dispatchTableAdmin({ type: "collectionsChanged", tables: payload.tables });
});
```

In the existing `database.opened` handler, after `flow.onDatabaseOpened(...)`, also dispatch:

```ts
dispatchTableAdmin({ type: "collectionsChanged", tables: payload.tables });
if (newTableBtn) newTableBtn.disabled = false;
```

(This uses the same `payload.tables` from `database.opened`. If `database.opened` payload uses a different field name in `contracts.ts`, check `DatabaseOpenedPayload` and use the correct field — it is `tables`.)

- [ ] **Step 6: Wire the create-table modal**

```ts
function openCreateTableModal(): void {
  if (!createTableModal || !createTableName || !createTableFields) return;
  createTableName.value = "";
  createTableFields.innerHTML = "";
  addFieldRow();
  if (createTableError) createTableError.hidden = true;
  if (createTableSubmit) createTableSubmit.disabled = true;
  createTableModal.hidden = false;
  createTableName.focus();
}

function addFieldRow(): void {
  if (!createTableFields) return;
  const row = document.createElement("div");
  row.className = "field-row";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "field__input";
  input.maxLength = 64;
  input.placeholder = "字段名";
  const select = document.createElement("select");
  select.className = "field__select";
  for (const t of TABLE_FIELD_TYPES) {
    const opt = document.createElement("option");
    opt.value = t;
    opt.textContent = t;
    select.appendChild(opt);
  }
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "btn btn--secondary";
  remove.textContent = "−";
  remove.addEventListener("click", () => row.remove());
  row.appendChild(input);
  row.appendChild(select);
  row.appendChild(remove);
  createTableFields.appendChild(row);
}

function closeCreateTableModal(): void {
  if (createTableModal) createTableModal.hidden = true;
}

function collectFieldRows(): Array<{ key: string; type: TableFieldType }> {
  if (!createTableFields) return [];
  const rows: Array<{ key: string; type: TableFieldType }> = [];
  for (const row of Array.from(createTableFields.querySelectorAll(".field-row"))) {
    const input = row.querySelector(".field__input") as HTMLInputElement | null;
    const select = row.querySelector(".field__select") as HTMLSelectElement | null;
    if (!input || !select) continue;
    rows.push({ key: input.value, type: select.value as TableFieldType });
  }
  return rows;
}

newTableBtn?.addEventListener("click", () => openCreateTableModal());
createTableClose?.addEventListener("click", closeCreateTableModal);
createTableCancel?.addEventListener("click", closeCreateTableModal);
createTableAddField?.addEventListener("click", () => addFieldRow());

createTableSubmit?.addEventListener("click", async () => {
  if (!createTableName) return;
  const nameErr = validateTableName(createTableName.value);
  const { fields, errors } = validateFields(collectFieldRows());
  const allErrors = [nameErr, ...errors].filter((e): e is string => e !== null);
  if (allErrors.length > 0) {
    if (createTableError) {
      createTableError.textContent = allErrors.join(" / ");
      createTableError.hidden = false;
    }
    return;
  }
  if (createTableSubmit) createTableSubmit.disabled = true;
  await requestCreate(
    bridge,
    createTableName.value.trim(),
    fields,
    dispatchTableAdmin,
  );
  // Close on success (status idle and no error). If error, keep open w/ message.
  if (tableAdmin.status === "idle" && tableAdmin.error === null) {
    closeCreateTableModal();
  } else if (createTableError && tableAdmin.error) {
    createTableError.textContent = tableAdmin.error;
    createTableError.hidden = false;
    if (createTableSubmit) createTableSubmit.disabled = false;
  }
});
```

- [ ] **Step 7: Wire the delete-confirm modal**

```ts
function openDeleteConfirm(collection: string): void {
  pendingDeleteCollection = collection;
  if (deleteConfirmText) {
    deleteConfirmText.textContent = `确定要删除表 "${collection}" 吗？该操作将移除集合及其全部数据，且不可恢复。`;
  }
  if (deleteConfirmModal) deleteConfirmModal.hidden = false;
}

deleteConfirmCancel?.addEventListener("click", () => {
  pendingDeleteCollection = null;
  if (deleteConfirmModal) deleteConfirmModal.hidden = true;
});

deleteConfirmOk?.addEventListener("click", async () => {
  const collection = pendingDeleteCollection;
  if (!collection) return;
  pendingDeleteCollection = null;
  if (deleteConfirmModal) deleteConfirmModal.hidden = true;
  await requestDelete(bridge, collection, dispatchTableAdmin);
});
```

- [ ] **Step 8: Update sidebar active state when the table changes**

Whenever the selected table changes (the existing `flow.onTablePageLoaded` / `onStateChange` path that sets `currentTable`), call `renderSidebar()` so the active highlight updates. Find the existing `onStateChange` callback that re-renders the grid and add a `renderSidebar()` call there.

- [ ] **Step 9: Typecheck + build + run tests**

```bash
cd desktop/web-grid && npm run build && npm test
```
Expected: build passes; all vitest tests pass (the new code is integration glue, covered by the flow/validation unit tests; no new test file for main.ts).

- [ ] **Step 10: Commit**

```bash
git add desktop/web-grid/src/main.ts
git commit -m "feat(web-grid): wire sidebar + create/delete modals to tableAdmin flow"
```

---

## Task 12: Delete native table-management windows (C#)

**Files:**
- Delete: `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml`
- Delete: `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml.cs`
- Delete: `desktop/src/VibeTable.Desktop/TableAdminWindow.xaml`
- Delete: `desktop/src/VibeTable.Desktop/TableAdminWindow.xaml.cs`
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml` (remove `ManageTablesButton`)
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs` (remove `OnManageTables`, `ManageTablesButton.IsEnabled`)

- [ ] **Step 1: Remove the button from MainWindow.xaml**

In `desktop/src/VibeTable.Desktop/MainWindow.xaml` (lines 27-34), delete the entire `<Button x:Name="ManageTablesButton" .../>` element.

- [ ] **Step 2: Remove the handler + enable line from MainWindow.xaml.cs**

In `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`:
- Delete the `OnManageTables` method (~lines 835-849).
- Delete the line `ManageTablesButton.IsEnabled = authenticated;` (~line 227).

- [ ] **Step 3: Delete the two window files**

```bash
git rm desktop/src/VibeTable.Desktop/TableManagementWindow.xaml desktop/src/VibeTable.Desktop/TableManagementWindow.xaml.cs desktop/src/VibeTable.Desktop/TableAdminWindow.xaml desktop/src/VibeTable.Desktop/TableAdminWindow.xaml.cs
```

- [ ] **Step 4: Build the whole solution**

```bash
dotnet build desktop/VibeTable.sln
```
Expected: Build succeeded. If there are leftover references to `TableManagementWindow` / `TableAdminWindow` anywhere, grep and remove them:

```bash
grep -rn "TableManagementWindow\|TableAdminWindow\|ManageTablesButton" desktop/src desktop/tests
```
Expected: no matches (after deletions).

- [ ] **Step 5: Run all C# tests**

```bash
dotnet test desktop/VibeTable.sln
```
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add -A desktop/src desktop/tests
git commit -m "refactor: remove native table-management windows (now in web sidebar)"
```

---

## Task 13: Manual smoke test + final verification

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

```bash
cd desktop/web-grid && npm run build
cd ../.. && dotnet build desktop/VibeTable.sln
```
Expected: both succeed.

- [ ] **Step 2: Run all tests (C# + TS)**

```bash
dotnet test desktop/VibeTable.sln
cd desktop/web-grid && npm test
```
Expected: all green.

- [ ] **Step 3: Launch the desktop app and verify manually**

Launch the WPF app (via the IDE or `dotnet run --project desktop/src/VibeTable.Desktop`). After Directus startup completes and the workspace opens, verify:

1. **Sidebar appears** on the left with a `+ 新建表` button and a table list seeded from `database.opened`.
2. **`#table-select` is gone** — the toolbar no longer has the dropdown.
3. **Click a table** in the sidebar → the main grid switches to that table.
4. **Active highlight** follows the selected table.
5. **Create a table**: click `+ 新建表` → enter name `smoke_test` → add a field `name` (string) → click 创建 → modal closes → `smoke_test` appears in the sidebar (via `database.collectionsChanged`).
6. **Validation**: try creating with an empty name or a name starting with a digit → error message shows, modal stays open.
7. **Delete a table**: click 删除 on `smoke_test` → confirm dialog → confirm → `smoke_test` disappears from the sidebar.
8. **Error path**: stop the Python backend, try to create a table → an `operation.failed` surfaces (as the modal error or status).

- [ ] **Step 4: Commit any smoke-test fixes**

If smoke testing reveals issues, fix them and commit with clear messages. If no fixes needed, no commit.

- [ ] **Step 5: Final commit (if the plan was executed on a branch, this is where the branch is ready for merge)**

```bash
git log --oneline main..HEAD
```
Confirm the task-by-task commits are present.

---

## Notes for the implementer

- **Field-type triple sync**: `backend/contracts/table_admin.py:22`, `TableAdminWindow` (deleted in Task 12, but the constant lived there), and `desktop/web-grid/src/contracts.ts` (`TABLE_FIELD_TYPES`). After Task 12, the only client-side copy is the TS one; keep it in sync with the Python source of truth.
- **No HMR**: the dev loop is `npm run build` in `desktop/web-grid` then relaunch the WPF host. Use `window.__vibeTableBridge` in devtools (debug builds) to poke the bridge.
- **`main.ts` line numbers** in this plan are approximate (from the spec exploration); re-locate symbols by grep before editing.
- **The `currentTableName` accessor** in Task 11 Step 4: confirm against `tableFlow.ts`'s `TableFlowState.currentTable`. Use `flow.getState().currentTable`.
- **Runtime Set staleness** (known item): `hostBridge.ts` `WEB_MESSAGE_TYPES`/`HOST_EVENT_TYPES` are missing G1/G3 types that exist in the type unions. Task 4 only adds the table-management types; it does NOT fix the G1/G3 gap. Do not be tempted to "fix" it here — that is out of scope and would expand the change.
