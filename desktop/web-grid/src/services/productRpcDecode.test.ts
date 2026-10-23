import { describe, expect, it } from "vitest";
import {
  decodeContentVersionEntry,
  decodeDeletePresetVersionResult,
  decodePresetEntry,
  decodePresetsResult,
  decodeVersionCompareResult,
  decodeVersionPromoteResult,
  decodeVersionSaveResult,
  decodeVersionsResult,
} from "./productRpcDecode";

// Corpus-shaped fixtures from contracts/v2/preset-python-oracle.json and
// content-version-python-oracle.json (Go producer output).
const corpusView = {
  filters: [], sorts: [], groups: [], summaries: [], search: "",
  visibleFields: ["title"], layout: "table", kind: "calendar", dateField: "due",
  endDateField: null, titleField: "title", groupField: null, coverField: null,
  columns: [{ key: "open" }], density: "comfortable", isDefault: false,
  collapsedGroupKeys: ["bucket:0"],
};
const corpusPreset = {
  id: "z", collection: "orders", name: "Existing", scope: "role",
  view: corpusView, userId: null, revision: "revision-scripted-before",
  changeSetId: null, emittedEvents: [],
};
const corpusVersion = {
  id: "version-a", key: "alpha", name: "命名 \"修订", outdated: true,
  mainHash: "bbbb", revision: "metadata-old", changeSetId: null, emittedEvents: [],
};

describe("productRpcDecode", () => {
  it("accepts corpus-shaped preset entries and preserves them by reference", () => {
    expect(decodePresetEntry(corpusPreset, { collection: "orders" })).toBe(corpusPreset);
    expect(decodePresetsResult({ collection: "orders", presets: [corpusPreset] }, { collection: "orders" }))
      .toMatchObject({ collection: "orders", presets: [corpusPreset] });
  });

  it("rejects preset entries with contract violations", () => {
    const cases: unknown[] = [
      null,
      "preset",
      { ...corpusPreset, revision: "" },
      { ...corpusPreset, scope: "team" },
      { ...corpusPreset, emittedEvents: "none" },
      { ...corpusPreset, emittedEvents: [1] },
      { ...corpusPreset, userId: 7 },
      { ...corpusPreset, changeSetId: 7 },
      { ...corpusPreset, view: null },
      { ...corpusPreset, view: { ...corpusView, filters: "x" } },
      { ...corpusPreset, view: { ...corpusView, visibleFields: [3] } },
      { ...corpusPreset, view: { ...corpusView, visibleFields: "title" } },
      { ...corpusPreset, view: { ...corpusView, groups: [null] } },
      { ...corpusPreset, view: { ...corpusView, summaries: "none" } },
      { ...corpusPreset, view: { ...corpusView, collapsedGroupKeys: [2] } },
      { ...corpusPreset, view: { ...corpusView, columns: "none" } },
      { ...corpusPreset, view: { ...corpusView, kind: "gantt" } },
      { ...corpusPreset, view: { ...corpusView, density: "tight" } },
      { ...corpusPreset, view: { ...corpusView, isDefault: "yes" } },
      { ...corpusPreset, view: { ...corpusView, dateField: 3 } },
      { ...corpusPreset, view: { ...corpusView, search: null } },
      { ...corpusPreset, view: { ...corpusView, layout: null } },
    ];
    for (const value of cases) {
      expect(decodePresetEntry(value, { collection: "orders" })).toBeNull();
    }
  });

  it("rejects preset identities that do not match the request", () => {
    expect(decodePresetEntry(corpusPreset, { collection: "other" })).toBeNull();
    expect(decodePresetEntry(corpusPreset, { collection: "orders", presetId: "other" })).toBeNull();
    expect(decodePresetsResult({ collection: "orders", presets: [corpusPreset] }, { collection: "other" })).toBeNull();
    expect(decodePresetsResult({ collection: "orders", presets: [null] }, { collection: "orders" })).toBeNull();
    expect(decodePresetsResult({ collection: "orders", presets: {} }, { collection: "orders" })).toBeNull();
    expect(decodePresetsResult(null, { collection: "orders" })).toBeNull();
  });

  it("tolerates optional view fields but accepts minimal required views", () => {
    const minimal = { ...corpusPreset, view: { filters: [], sorts: [], search: "", visibleFields: [], layout: "table" } };
    expect(decodePresetEntry(minimal, { collection: "orders" })).toBe(minimal);
  });

  it("accepts corpus-shaped version entries and rejects violations", () => {
    expect(decodeContentVersionEntry(corpusVersion)).toBe(corpusVersion);
    const cases: unknown[] = [
      null,
      { ...corpusVersion, revision: "" },
      { ...corpusVersion, id: "" },
      { ...corpusVersion, outdated: "yes" },
      { ...corpusVersion, outdated: 1 },
      { ...corpusVersion, key: 3 },
      { ...corpusVersion, name: null },
      { ...corpusVersion, mainHash: 3 },
      { ...corpusVersion, changeSetId: 3 },
      { ...corpusVersion, emittedEvents: [3] },
      { ...corpusVersion, emittedEvents: null },
    ];
    for (const value of cases) {
      expect(decodeContentVersionEntry(value)).toBeNull();
    }
  });

  it("binds version list results to the requested scope", () => {
    const result = { collection: "orders", itemId: "row-1", versions: [corpusVersion] };
    expect(decodeVersionsResult(result, { collection: "orders", itemId: "row-1" })).toBe(result);
    expect(decodeVersionsResult(result, { collection: "other", itemId: "row-1" })).toBeNull();
    expect(decodeVersionsResult(result, { collection: "orders", itemId: "row-2" })).toBeNull();
    expect(decodeVersionsResult({ ...result, versions: [null] }, { collection: "orders", itemId: "row-1" })).toBeNull();
  });

  it("requires compare identity, mainHash, versionRevision and difference shape", () => {
    const result = {
      collection: "orders", itemId: "row-1", versionId: "v1", outdated: false,
      mainHash: "hash-1", versionRevision: "rev-v1",
      differences: { title: { main: "new", version: "old" } },
    };
    expect(decodeVersionCompareResult(result, { collection: "orders", itemId: "row-1", versionId: "v1" })).toBe(result);
    const cases: Array<[unknown, object]> = [
      [{ ...result, versionRevision: "" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, versionRevision: undefined }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, mainHash: "" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, outdated: "no" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, versionId: "v2" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, collection: "other" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, itemId: "row-2" }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, differences: null }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, differences: { title: { main: "new" } } }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [{ ...result, differences: { title: "flat" } }, { collection: "orders", itemId: "row-1", versionId: "v1" }],
      [null, { collection: "orders", itemId: "row-1", versionId: "v1" }],
    ];
    for (const [value, expected] of cases) {
      expect(decodeVersionCompareResult(value, expected as never)).toBeNull();
    }
  });

  it("requires write receipts to echo the targeted identity and producer decorations", () => {
    const save = { saved: "v1", revisionId: "revision-current", changeSetId: "metadata-change", status: "applied", emittedEvents: ["metadata-event"] };
    expect(decodeVersionSaveResult(save, { versionId: "v1" })).toBe(save);
    expect(decodeVersionSaveResult({ ...save, saved: "v2" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, saved: "" }, { versionId: "" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, revisionId: "" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, revisionId: undefined }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, changeSetId: "" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, status: "pending" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, status: undefined }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, emittedEvents: "metadata-event" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult({ ...save, emittedEvents: [1] }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionSaveResult("applied", { versionId: "v1" })).toBeNull();

    const promote = { promoted: "v1", restoredToRevision: "revision-saved", result: { ok: true } };
    expect(decodeVersionPromoteResult(promote, { versionId: "v1" })).toBe(promote);
    expect(decodeVersionPromoteResult({ ...promote, promoted: "v2" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionPromoteResult({ ...promote, restoredToRevision: "" }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionPromoteResult({ ...promote, result: null }, { versionId: "v1" })).toBeNull();
    expect(decodeVersionPromoteResult(null, { versionId: "v1" })).toBeNull();

    const deleted = { deleted: "p1", status: "applied", changeSetId: "change-scripted", emittedEvents: ["event-scripted"] };
    expect(decodeDeletePresetVersionResult(deleted, { id: "p1" })).toBe(deleted);
    expect(decodeDeletePresetVersionResult({ deleted: "p1" }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, deleted: "p2" }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, deleted: "" }, { id: "" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, status: "pending" }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, changeSetId: "" }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, changeSetId: null }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, emittedEvents: "event" }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult({ ...deleted, emittedEvents: [1] }, { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult("deleted", { id: "p1" })).toBeNull();
    expect(decodeDeletePresetVersionResult(null, { id: "p1" })).toBeNull();

    // Write receipts for preset.save / version.create must carry the decorated
    // non-empty changeSetId; list entries keep tolerating null.
    expect(decodePresetEntry(corpusPreset, { collection: "orders" })).toBe(corpusPreset);
    expect(decodePresetEntry(corpusPreset, { collection: "orders" }, { writeReceipt: true })).toBeNull();
    expect(decodePresetEntry({ ...corpusPreset, changeSetId: "change-scripted" }, { collection: "orders" }, { writeReceipt: true }))
      .toMatchObject({ changeSetId: "change-scripted" });
    expect(decodeContentVersionEntry(corpusVersion, false)).toBe(corpusVersion);
    expect(decodeContentVersionEntry(corpusVersion, true)).toBeNull();
    expect(decodeContentVersionEntry({ ...corpusVersion, changeSetId: "metadata-change" }, true))
      .toMatchObject({ changeSetId: "metadata-change" });
  });
});
