import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useContentVersionService } from "./contentVersionService";
import { usePresetVersionService } from "./presetVersionService";

// Fixtures mirror the Go producers (sidecar/internal/metadata/preset.go,
// content_version.go) and the cross-stack corpus
// (contracts/v2/preset-python-oracle.json, content-version-python-oracle.json).
const normalizedView = {
  filters: [], sorts: [], groups: [], summaries: [], search: "",
  visibleFields: [], layout: "table", kind: "table", dateField: null,
  endDateField: null, titleField: null, groupField: null, coverField: null,
  columns: [], density: "comfortable", isDefault: false,
} as const;
const versionEntry = { id: "v1", key: "draft", name: "Draft", outdated: false, mainHash: "hash-1", revision: "rev-v1", emittedEvents: [] };
const authoredView = {
  filters: [], sorts: [], search: "", visibleFields: [], layout: "table",
} as const;

describe("presetVersionService", () => {
  const requests: Array<{ type: string; payload: Record<string, unknown> }> = [];
  let respond: (type: string, payload: Record<string, unknown>) => unknown;

  beforeEach(() => {
    setActivePinia(createPinia());
    requests.length = 0;
    respond = (type: string, payload: Record<string, unknown>): unknown => {
      if (type === "preset.list") {
        return { collection: String(payload.collection), presets: [] };
      }
      if (type === "preset.save") {
        return {
          id: payload.presetId ?? "p1", collection: String(payload.collection),
          name: String(payload.name), scope: "system", view: normalizedView,
          userId: null, revision: "rev-p1", changeSetId: "change-scripted",
          emittedEvents: ["event-scripted"],
        };
      }
      if (type === "preset.delete") {
        return {
          deleted: String(payload.presetId), status: "applied",
          changeSetId: "change-scripted", emittedEvents: ["event-scripted"],
        };
      }
      if (type === "version.create") {
        return {
          id: "v1", key: "draft", name: "Draft", outdated: false,
          mainHash: "hash-1", revision: "rev-v1", changeSetId: "metadata-change",
          emittedEvents: ["metadata-event"],
        };
      }
      if (type === "version.list") {
        return { collection: String(payload.collection), itemId: String(payload.itemId), versions: [] };
      }
      if (type === "version.save") {
        return {
          saved: String(payload.versionId), revisionId: "revision-current",
          metadataRevision: "rev-v1", changeSetId: "metadata-change",
          emittedEvents: ["metadata-event"], status: "applied",
        };
      }
      if (type === "version.compare") {
        return {
          collection: String(payload.collection), itemId: String(payload.itemId),
          versionId: String(payload.versionId), outdated: false,
          mainHash: "hash-1", versionRevision: "rev-v1", differences: {},
        };
      }
      if (type === "version.promote") {
        return {
          promoted: String(payload.versionId), restoredToRevision: "revision-saved",
          result: {
            collection: String(payload.collection), itemId: String(payload.itemId),
            restoredToRevision: "revision-saved", newRevisionId: "revision-restored",
            item: { id: String(payload.itemId), title: "命名修订" },
          },
        };
      }
      return {
        deleted: String(payload.versionId), status: "applied",
        changeSetId: "metadata-change", emittedEvents: ["metadata-event"],
      };
    };
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      requests.push({ type, payload });
      return respond(type, payload);
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => setHostBridgeForTesting(null));

  it.each([null, { id: "p1", revision: "rev-p1" }])(
    "accepts the exact authored name echoed for target %s",
    async (target) => {
      await expect(usePresetVersionService().savePreset("orders", " 我的视图 🌍 ", authoredView, target))
        .resolves.toMatchObject({ name: " 我的视图 🌍 " });
    },
  );

  it.each([null, { id: "p1", revision: "rev-p1" }])(
    "rejects a complete save receipt for a different name with target %s",
    async (target) => {
      const producer = respond;
      respond = (type, payload) => ({
        ...(producer(type, payload) as object), name: "Other view",
      });
      await expect(usePresetVersionService().savePreset("orders", "My view", authoredView, target))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
      expect(requests).toHaveLength(1);
    },
  );

  it("attaches a fresh operationId to every write command", async () => {
    const service = usePresetVersionService();
    const view = {
      filters: [{
        groupLogic: "OR",
        filters: [
          { field: "status", operator: "eq", value: "open" },
          { field: "priority", operator: "eq", value: "urgent" },
        ],
      }],
      sorts: [{ field: "created_at", direction: "desc" }],
      groups: [{ field: "warehouse" }, { field: "created_at", bucket: "month" }],
      summaries: [{ field: "amount", function: "sum" }],
      search: "",
      visibleFields: [],
      layout: "table",
    } as const;

    await service.savePreset("orders", "My view", view, {
      id: "p1",
      revision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    });
    await service.deletePreset("p1", "rev-p1");
    await useContentVersionService().create({ collection: "orders", itemId: "row-1" }, "Draft");
    await useContentVersionService().save({ collection: "orders", itemId: "row-1" }, versionEntry);
    await useContentVersionService().promote({ collection: "orders", itemId: "row-1" }, { collection: "orders", itemId: "row-1", versionId: "v1", versionRevision: "rev-v1", mainHash: "hash-1", outdated: false, differences: {} });
    await useContentVersionService().delete({ collection: "orders", itemId: "row-1" }, versionEntry);

    const writes = requests.filter(({ type }) => [
      "preset.save", "preset.delete", "version.create",
      "version.save", "version.promote", "version.delete",
    ].includes(type));
    expect(writes.map(({ type }) => type)).toEqual([
      "preset.save", "preset.delete", "version.create",
      "version.save", "version.promote", "version.delete",
    ]);
    expect(writes.every(({ payload }) =>
      typeof payload.operationId === "string" && payload.operationId.length > 0,
    )).toBe(true);
    expect(new Set(writes.map(({ payload }) => payload.operationId)).size).toBe(writes.length);
    expect(requests.find(({ type }) => type === "preset.delete")?.payload.expectedRevision)
      .toBe("rev-p1");
    expect(requests.find(({ type }) => type === "preset.save")?.payload.view).toEqual(view);
    expect(requests.find(({ type }) => type === "preset.save")?.payload).toMatchObject({
      presetId: "p1",
      expectedRevision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    });
    expect(requests.find(({ type }) => type === "version.delete")?.payload.expectedRevision)
      .toBe("rev-v1");
  });

  it("keeps list and compare requests read-only", async () => {
    await useContentVersionService().list({ collection: "orders", itemId: "row-1" });
    await useContentVersionService().compare({ collection: "orders", itemId: "row-1" }, "v1");

    expect(requests).toEqual([
      { type: "version.list", payload: { collection: "orders", itemId: "row-1" } },
      {
        type: "version.compare",
        payload: { collection: "orders", itemId: "row-1", versionId: "v1" },
      },
    ]);
  });

  it("loads collection-scoped table views without a write operation id", async () => {
    const service = usePresetVersionService();
    const result = await service.listPresets("orders");
    expect(result).toEqual({ collection: "orders", presets: [] });
    expect(requests).toEqual([
      { type: "preset.list", payload: { collection: "orders" } },
    ]);
  });

  it("rejects the product error envelope returned for a stale preset save", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => ({
        error: {
          code: "preset_edit_conflict",
          path: "expectedRevision",
          message: "Preset changed elsewhere.",
          details: null,
          retryable: false,
        },
      })),
    } as unknown as HostBridge);
    const service = usePresetVersionService();

    await expect(service.savePreset("orders", "My view", {
      filters: [], sorts: [], search: "", visibleFields: [], layout: "table",
    }, {
      id: "p1",
      revision: "revision-stale",
    })).rejects.toMatchObject({
      code: "preset_edit_conflict",
      path: "expectedRevision",
      message: "Preset changed elsewhere.",
      retryable: false,
    });
  });

  it("rejects a malformed error envelope instead of returning it as success", async () => {
    respond = () => ({ error: { code: "authority.failed" } });
    const service = usePresetVersionService();

    await expect(service.savePreset("orders", "My view", authoredView, null))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });
    await expect(service.listPresets("orders"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });
  });

  it("rejects non-object and incomplete error envelopes as protocol failures", async () => {
    const service = usePresetVersionService();
    const versions = useContentVersionService();

    respond = () => ({ error: "boom" });
    await expect(versions.list({ collection: "orders", itemId: "row-1" }))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });

    respond = () => ({ error: { code: "preset_edit_conflict", path: "expectedRevision" } });
    await expect(service.deletePreset("p1", "rev-p1"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });

    respond = () => ({ error: null });
    await expect(versions.promote({ collection: "orders", itemId: "row-1" }, {
      collection: "orders", itemId: "row-1", versionId: "v1",
      versionRevision: "rev-v1", mainHash: "hash-1", outdated: false, differences: {},
    })).rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });
  });

  it("rejects a preset.list success payload whose entries miss required fields", async () => {
    respond = (type) => {
      if (type !== "preset.list") throw new Error(`unexpected ${type}`);
      return {
        collection: "orders",
        presets: [{
          id: "p1", collection: "orders", name: "My view", scope: "system",
          view: normalizedView, userId: null, emittedEvents: [],
        }],
      };
    };
    const service = usePresetVersionService();

    await expect(service.listPresets("orders"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("rejects a version.compare reply without versionRevision before it can seed promote", async () => {
    respond = (type, payload) => {
      if (type !== "version.compare") throw new Error(`unexpected ${type}`);
      return {
        collection: String(payload.collection), itemId: String(payload.itemId),
        versionId: String(payload.versionId), outdated: false,
        mainHash: "hash-1", differences: {},
      };
    };

    await expect(useContentVersionService().compare({ collection: "orders", itemId: "row-1" }, "v1"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("rejects responses whose identity does not match the request", async () => {
    const service = usePresetVersionService();
    const versions = useContentVersionService();

    respond = (type, payload) => {
      if (type === "version.list") {
        return { collection: "other", itemId: String(payload.itemId), versions: [] };
      }
      if (type === "version.compare") {
        return {
          collection: String(payload.collection), itemId: String(payload.itemId),
          versionId: "v-other", outdated: false, mainHash: "hash-1",
          versionRevision: "rev-v1", differences: {},
        };
      }
      if (type === "version.save") {
        return {
          saved: "v-other", revisionId: "revision-current", metadataRevision: "rev-v1",
          changeSetId: "metadata-change", emittedEvents: [], status: "applied",
        };
      }
      if (type === "version.promote") {
        return {
          promoted: "v-other", restoredToRevision: "revision-saved",
          result: {
            collection: "orders", itemId: "row-1",
            restoredToRevision: "revision-saved", newRevisionId: "revision-restored",
            item: { id: "row-1" },
          },
        };
      }
      if (type === "preset.save") {
        return {
          id: "p-other", collection: "orders", name: "My view", scope: "system",
          view: normalizedView, userId: null, revision: "rev-p1",
          changeSetId: "change-scripted", emittedEvents: [],
        };
      }
      if (type === "preset.delete" || type === "version.delete") {
        return { deleted: "other", status: "applied", changeSetId: "change", emittedEvents: [] };
      }
      throw new Error(`unexpected ${type}`);
    };

    await expect(versions.list({ collection: "orders", itemId: "row-1" }))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(versions.compare({ collection: "orders", itemId: "row-1" }, "v1"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(versions.save({ collection: "orders", itemId: "row-1" }, versionEntry))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(versions.promote({ collection: "orders", itemId: "row-1" }, {
      collection: "orders", itemId: "row-1", versionId: "v1",
      versionRevision: "rev-v1", mainHash: "hash-1", outdated: false, differences: {},
    })).rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(service.savePreset("orders", "My view", authoredView, { id: "p1", revision: "rev-p1" }))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(service.deletePreset("p1", "rev-p1"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(versions.delete({ collection: "orders", itemId: "row-1" }, versionEntry))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("does not let a void write pass silently on an unverifiable receipt", async () => {
    respond = () => "applied";

    await expect(useContentVersionService().save({ collection: "orders", itemId: "row-1" }, versionEntry))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(usePresetVersionService().deletePreset("p1", "rev-p1"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    await expect(useContentVersionService().promote({ collection: "orders", itemId: "row-1" }, {
      collection: "orders", itemId: "row-1", versionId: "v1",
      versionRevision: "rev-v1", mainHash: "hash-1", outdated: false, differences: {},
    })).rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("rejects a legal-looking envelope whose optional fields have illegal types", async () => {
    respond = () => ({
      error: {
        code: "preset_edit_conflict", path: "expectedRevision",
        message: "Preset changed elsewhere.", details: 42, retryable: "true",
      },
    });

    await expect(usePresetVersionService().savePreset("orders", "My view", authoredView, null))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "malformed-error-envelope" });
  });

  it("rejects preset views whose filter tree is structurally invalid before the store consumes it", async () => {
    const entry = (filters: unknown) => ({
      id: "p1", collection: "orders", name: "My view", scope: "system",
      view: { ...normalizedView, filters }, userId: null, revision: "rev-p1",
      changeSetId: null, emittedEvents: [],
    });
    for (const filters of [
      [{ filters: null }],
      [{ filters: "x" }],
      [{ filters: [] }],
      [{ groupLogic: "XOR", filters: [{ field: "status", operator: "eq", value: "open", logic: "AND" }] }],
      [{ field: "status", operator: "not_a_filter", value: "open", logic: "AND" }],
      [{ field: "", operator: "eq", value: "open", logic: "AND" }],
      [{ field: 3, operator: "eq", value: "open", logic: "AND" }],
      [{ field: "status", operator: "eq", value: "open", logic: "XOR" }],
      "filters",
    ]) {
      respond = (type) => {
        if (type !== "preset.list") throw new Error(`unexpected ${type}`);
        return { collection: "orders", presets: [entry(filters)] };
      };
      await expect(usePresetVersionService().listPresets("orders"))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    }
  });

  it("rejects sorts/groups/summaries that violate required fields or closed sets", async () => {
    const entry = (view: Record<string, unknown>) => ({
      id: "p1", collection: "orders", name: "My view", scope: "system",
      view: { ...normalizedView, ...view }, userId: null, revision: "rev-p1",
      changeSetId: null, emittedEvents: [],
    });
    for (const view of [
      { sorts: [{ field: "created", direction: "up" }] },
      { sorts: [{ direction: "asc" }] },
      { sorts: [{ field: "created", nullsLast: "yes" }] },
      { groups: [{ field: "warehouse", bucket: "epoch" }] },
      { groups: [{ bucket: "month" }] },
      { groups: [{ field: "created", bucket: "month", numberInterval: 0 }] },
      { groups: [{ field: "created", bucket: "month", numberInterval: "10" }] },
      { summaries: [{ field: "amount", function: "median" }] },
      { summaries: [{ field: "amount" }] },
      { summaries: [{ function: "sum" }] },
    ]) {
      respond = (type) => {
        if (type !== "preset.list") throw new Error(`unexpected ${type}`);
        return { collection: "orders", presets: [entry(view)] };
      };
      await expect(usePresetVersionService().listPresets("orders"))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    }
  });

  it("accepts corpus-shaped nested filter groups and Go-normalized query parts", async () => {
    const saved = {
      id: "p1", collection: "orders", name: "My view", scope: "system",
      view: {
        ...normalizedView,
        filters: [{
          groupLogic: "OR",
          filters: [
            { field: "amount", operator: "gt", value: 3, logic: "AND" },
            { groupLogic: "AND", filters: [{ field: "status", operator: "eq", value: "open", logic: "AND" }] },
          ],
        }],
        sorts: [{ field: "title", direction: "asc", nullsLast: true }],
        groups: [{ field: "amount", direction: "asc", bucket: "number", numberInterval: 10 }],
        summaries: [{ field: "amount", function: "sum" }],
      },
      userId: null, revision: "rev-p1", changeSetId: "change-scripted",
      emittedEvents: ["event-scripted"],
    };
    respond = (type) => {
      if (type !== "preset.list") throw new Error(`unexpected ${type}`);
      return { collection: "orders", presets: [saved] };
    };

    await expect(usePresetVersionService().listPresets("orders")).resolves.toMatchObject({
      collection: "orders",
      presets: [{ id: "p1", view: { filters: [{ groupLogic: "OR" }] } }],
    });
  });

  it("rejects truncated delete receipts that miss producer-required fields", async () => {
    const versions = useContentVersionService();
    const service = usePresetVersionService();
    const full = { deleted: "p1", status: "applied", changeSetId: "change-1", emittedEvents: ["event-1"] };
    const cases: Array<[string, unknown]> = [
      ["preset.delete", { deleted: "p1" }],
      ["preset.delete", { ...full, status: "pending" }],
      ["preset.delete", { ...full, changeSetId: "" }],
      ["preset.delete", { ...full, changeSetId: null }],
      ["preset.delete", { ...full, emittedEvents: "event-1" }],
      ["preset.delete", { ...full, emittedEvents: [1] }],
      ["version.delete", { deleted: "v1" }],
      ["version.delete", { deleted: "v1", status: "applied" }],
    ];
    for (const [type, payload] of cases) {
      respond = (requested) => {
        if (requested !== type) throw new Error(`unexpected ${requested}`);
        return payload;
      };
      if (type === "preset.delete") {
        await expect(service.deletePreset("p1", "rev-p1"))
          .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
      } else {
        await expect(versions.delete({ collection: "orders", itemId: "row-1" }, versionEntry))
          .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
      }
    }
  });

  it("requires write receipts from save and create to carry the decorated changeSetId and status", async () => {
    const versions = useContentVersionService();

    respond = () => ({
      id: "p1", collection: "orders", name: "My view", scope: "system",
      view: normalizedView, userId: null, revision: "rev-p1",
      changeSetId: null, emittedEvents: [],
    });
    await expect(usePresetVersionService().savePreset("orders", "My view", authoredView, null))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });

    respond = () => ({
      id: "v1", key: "draft", name: "Draft", outdated: false, mainHash: "hash-1",
      revision: "rev-v1", changeSetId: null, emittedEvents: ["metadata-event"],
    });
    await expect(versions.create({ collection: "orders", itemId: "row-1" }, "Draft"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });

    respond = () => ({
      saved: "v1", revisionId: "revision-current", changeSetId: "metadata-change",
      emittedEvents: ["metadata-event"],
    });
    await expect(versions.save({ collection: "orders", itemId: "row-1" }, versionEntry))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });

    respond = () => ({
      saved: "v1", revisionId: "revision-current", changeSetId: "metadata-change",
      status: "applied", emittedEvents: "metadata-event",
    });
    await expect(versions.save({ collection: "orders", itemId: "row-1" }, versionEntry))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("requires version.save receipts to carry a non-empty metadataRevision", async () => {
    const full = {
      saved: "v1", revisionId: "revision-current", metadataRevision: "rev-v1",
      changeSetId: "metadata-change", status: "applied", emittedEvents: ["metadata-event"],
    };
    for (const metadataRevision of [undefined, "", 42]) {
      respond = () => ({ ...full, metadataRevision });
      await expect(useContentVersionService().save({ collection: "orders", itemId: "row-1" }, versionEntry))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    }
  });

  it("requires version.promote results to be a RestoreResult for the requested record", async () => {
    const promote = () => ({
      collection: "orders", itemId: "row-1", versionId: "v1",
      versionRevision: "rev-v1", mainHash: "hash-1", outdated: false, differences: {},
    });
    const fullResult = {
      collection: "orders", itemId: "row-1", restoredToRevision: "revision-saved",
      newRevisionId: "revision-restored", item: { id: "row-1", title: "命名修订" },
    };
    for (const result of [
      {},
      { ...fullResult, collection: "other" },
      { ...fullResult, itemId: "row-2" },
      { ...fullResult, restoredToRevision: "revision-other" },
      { ...fullResult, newRevisionId: 7 },
      { ...fullResult, item: "restored" },
      { collection: "orders", itemId: "row-1", restoredToRevision: "revision-saved", newRevisionId: null },
      "restored",
    ]) {
      respond = (type) => {
        if (type !== "version.promote") throw new Error(`unexpected ${type}`);
        return { promoted: "v1", restoredToRevision: "revision-saved", result };
      };
      await expect(useContentVersionService().promote({ collection: "orders", itemId: "row-1" }, promote()))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    }
  });

  it("enforces the producer's view and entry limits that later queries rely on", async () => {
    const many = (n: number, make: (i: number) => unknown) => Array.from({ length: n }, (_, i) => make(i));
    const view = (patch: Record<string, unknown>) => ({
      id: "p1", collection: "orders", name: "My view", scope: "system",
      view: { ...normalizedView, ...patch }, userId: null, revision: "rev-p1",
      changeSetId: null, emittedEvents: [],
    });
    const leaf = (field: string) => ({ field, operator: "eq", value: 1, logic: "AND" });
    const over = [
      { sorts: many(17, () => ({ field: "f" })) },
      { groups: many(3, () => ({ field: "f" })) },
      { summaries: many(4, () => ({ field: "f", function: "sum" })) },
      { visibleFields: many(129, i => `f${i}`) },
      { columns: many(129, () => ({ key: "open" })) },
      { collapsedGroupKeys: many(513, i => `k${i}`) },
      { filters: many(51, i => leaf(`f${i}`)) },
      { filters: [{ groupLogic: "AND", filters: many(51, i => leaf(`f${i}`)) }] },
      { filters: [leaf("f".repeat(129))] },
      { sorts: [{ field: "f".repeat(129) }] },
      { search: "搜".repeat(257) },
      { layout: "l".repeat(33) },
    ];
    for (const patch of over) {
      respond = (type) => {
        if (type !== "preset.list") throw new Error(`unexpected ${type}`);
        return { collection: "orders", presets: [view(patch)] };
      };
      await expect(usePresetVersionService().listPresets("orders"))
        .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
    }
    const entryOver = { ...view({}), name: "n".repeat(257) };
    respond = (type) => {
      if (type !== "preset.list") throw new Error(`unexpected ${type}`);
      return { collection: "orders", presets: [entryOver] };
    };
    await expect(usePresetVersionService().listPresets("orders"))
      .rejects.toMatchObject({ name: "ProductRpcProtocolError", reason: "invalid-success-payload" });
  });

  it("accepts producer-shaped values exactly at the limits (rune counting)", async () => {
    const many = (n: number, make: (i: number) => unknown) => Array.from({ length: n }, (_, i) => make(i));
    const at = {
      ...normalizedView,
      sorts: many(16, i => ({ field: `f${i}` })),
      groups: many(2, i => ({ field: `g${i}` })),
      summaries: many(3, i => ({ field: `s${i}`, function: "sum" })),
      visibleFields: many(128, i => `v${i}`),
      columns: many(128, i => ({ key: `c${i}` })),
      collapsedGroupKeys: many(512, i => `k${i}`),
      filters: many(50, i => ({ field: `f${i}`, operator: "eq", value: 1, logic: "AND" })),
      search: "搜".repeat(256),
      layout: "l".repeat(32),
    };
    respond = (type) => {
      if (type !== "preset.list") throw new Error(`unexpected ${type}`);
      return {
        collection: "orders",
        presets: [{
          id: "p1", collection: "orders", name: "名".repeat(256), scope: "system",
          view: at, userId: null, revision: "rev-p1", changeSetId: null, emittedEvents: [],
        }],
      };
    };
    await expect(usePresetVersionService().listPresets("orders")).resolves.toBeTruthy();
  });

  it("returns fully valid corpus-shaped results untouched", async () => {
    respond = (type, payload) => {
      if (type === "preset.list") {
        return {
          collection: "orders",
          presets: [{
            id: "z", collection: "orders", name: "Existing", scope: "role",
            view: normalizedView, userId: null, revision: "revision-scripted-before",
            changeSetId: null, emittedEvents: [],
          }],
        };
      }
      if (type === "version.list") {
        return {
          collection: String(payload.collection), itemId: String(payload.itemId),
          versions: [{
            id: "version-a", key: "alpha", name: "命名 \"修订", outdated: true,
            mainHash: "bbbb", revision: "metadata-old", changeSetId: null,
            emittedEvents: [],
          }],
        };
      }
      throw new Error(`unexpected ${type}`);
    };

    await expect(usePresetVersionService().listPresets("orders")).resolves.toMatchObject({
      collection: "orders",
      presets: [{ id: "z", revision: "revision-scripted-before", scope: "role" }],
    });
    await expect(useContentVersionService().list({ collection: "orders", itemId: "row-1" }))
      .resolves.toMatchObject({
        collection: "orders", itemId: "row-1",
        versions: [{ id: "version-a", revision: "metadata-old", outdated: true }],
      });
  });
});
