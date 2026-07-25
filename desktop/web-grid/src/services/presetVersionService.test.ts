import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { usePresetVersionService } from "./presetVersionService";

describe("presetVersionService", () => {
  const requests: Array<{ type: string; payload: Record<string, unknown> }> = [];

  beforeEach(() => {
    setActivePinia(createPinia());
    requests.length = 0;
    const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
      requests.push({ type, payload });
      if (type === "preset.save") {
        return {
          id: "p1", collection: "orders", name: "My view", scope: "system",
          view: payload.view, userId: null, revision: "rev-p1",
          changeSetId: null, emittedEvents: [],
        };
      }
      if (type === "version.create") {
        return {
          id: "v1", key: "draft", name: "Draft", outdated: false,
          mainHash: "hash-1", revision: "rev-v1", changeSetId: null, emittedEvents: [],
        };
      }
      if (type === "version.list") {
        return { collection: "orders", itemId: "row-1", versions: [] };
      }
      if (type === "version.compare") {
        return {
          collection: "orders", itemId: "row-1", versionId: "v1",
          outdated: false, mainHash: "hash-1", differences: {},
        };
      }
      return { deleted: String(payload.presetId ?? payload.versionId ?? "") };
    });
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  afterEach(() => setHostBridgeForTesting(null));

  it("attaches a fresh operationId to every write command", async () => {
    const service = usePresetVersionService();
    const view = {
      filters: [], sorts: [], search: "", visibleFields: [], layout: "table",
    } as const;

    await service.savePreset("orders", "My view", view);
    await service.deletePreset("p1", "rev-p1");
    await service.createVersion("orders", "row-1", "draft", "Draft");
    await service.saveVersion("orders", "row-1", "v1");
    await service.promoteVersion("orders", "row-1", "v1", "hash-1");
    await service.deleteVersion("orders", "row-1", "v1", "rev-v1");

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
    expect(requests.find(({ type }) => type === "version.delete")?.payload.expectedRevision)
      .toBe("rev-v1");
  });

  it("keeps list and compare requests read-only", async () => {
    const service = usePresetVersionService();
    await service.listVersions("orders", "row-1");
    await service.compareVersion("orders", "row-1", "v1");

    expect(requests).toEqual([
      { type: "version.list", payload: { collection: "orders", itemId: "row-1" } },
      {
        type: "version.compare",
        payload: { collection: "orders", itemId: "row-1", versionId: "v1" },
      },
    ]);
  });
});
