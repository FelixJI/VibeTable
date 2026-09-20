import { computed, effectScope, nextTick, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ContentVersionEntry, VersionsResult, VersionCompareResult } from "@/contracts";
import type { NamedRevisionScope } from "@/services/contentVersionService";
import { useNamedRevisions } from "./useNamedRevisions";

import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useContentVersionService } from "@/services/contentVersionService";
import { useRevisionHistoryService } from "@/services/revisionHistoryService";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import { registerWorkspaceEpochReset, useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import type { BridgeMessage } from "@/contracts";

beforeEach(() => setActivePinia(createPinia()));

const entry: ContentVersionEntry = { id: "v1", key: "named", name: "Named", revision: "r1", mainHash: "h1", outdated: false, emittedEvents: [] };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done; }); return { promise, resolve }; }
async function settle() { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); }
function fixture() {
  const scope = ref<NamedRevisionScope | null>({ collection: "table", itemId: "row1" });
  const service = {
    list: vi.fn(async (s: NamedRevisionScope): Promise<VersionsResult> => ({ ...s, versions: [entry] })),
    create: vi.fn(async () => entry), save: vi.fn(async () => {}), delete: vi.fn(async () => {}),
    compare: vi.fn(async (s: NamedRevisionScope, id: string): Promise<VersionCompareResult> => ({ ...s, versionId: id, versionRevision: "r1", mainHash: "h2", outdated: true, differences: { title: { main: "new", version: "old" } } })),
    promote: vi.fn(async () => {}),
  };
  const restored = vi.fn(async () => {});
  const lifetime = effectScope();
  const controller = lifetime.run(() => useNamedRevisions(scope, service, restored))!;
  return { scope, service, restored, lifetime, ...controller };
}
describe("named revision UI intent", () => {
  it("uses the selected revision CAS and explicit comparison before restore", async () => {
    const f = fixture(); await settle(); f.select("v1"); await f.dispatch("compare");
    expect(f.state.comparison?.differences.title?.version).toBe("old");
    await f.dispatch("promote");
    expect(f.service.promote).toHaveBeenCalledWith({ collection: "table", itemId: "row1" }, expect.objectContaining({ versionId: "v1", versionRevision: "r1", mainHash: "h2" }));
    expect(f.restored).toHaveBeenCalledTimes(1);
    await f.dispatch("save"); expect(f.service.save).toHaveBeenCalledWith({ collection: "table", itemId: "row1" }, entry);
    expect(f.state.selectedId).toBe("v1"); f.lifetime.stop();
  });
  it("discards an old list after scope changes and preserves a newer explicit selection", async () => {
    const f = fixture(); await settle(); const old = deferred<VersionsResult>();
    f.service.list.mockReturnValueOnce(old.promise); const loading = f.dispatch("reload");
    f.scope.value = { collection: "table", itemId: "row2" }; await settle(); f.select("v1");
    old.resolve({ collection: "table", itemId: "row1", versions: [{ ...entry, id: "wrong" }] }); await loading;
    expect(f.state.versions.map((v) => v.id)).toEqual(["v1"]); expect(f.state.selectedId).toBe("v1"); f.lifetime.stop();
  });
  it("drops a pending comparison when the drawer closes or the component is disposed", async () => {
    const f = fixture(); await settle(); f.select("v1"); const old = deferred<VersionCompareResult>();
    f.service.compare.mockReturnValueOnce(old.promise); const comparing = f.dispatch("compare");
    f.scope.value = null; f.lifetime.stop();
    old.resolve({ collection: "table", itemId: "row1", versionId: "v1", versionRevision: "r1", mainHash: "h1", outdated: false, differences: {} }); await comparing;
    expect(f.state.comparison).toBeNull(); expect(f.state.versions).toEqual([]); expect(f.restored).not.toHaveBeenCalled();
  });
  it("surfaces conflicts without replacing user selection or draft name", async () => {
    const f = fixture(); await settle(); f.select("v1"); f.state.name = "unfinished";
    f.service.save.mockRejectedValueOnce(new Error("version_edit_conflict")); await f.dispatch("save");
    expect(f.state.error).toBe("version_edit_conflict"); expect(f.state.selectedId).toBe("v1"); expect(f.state.name).toBe("unfinished"); f.lifetime.stop();
  });
});


it("never posts the old epoch while the real history reset retains a row selection", async () => {
  const session = useWorkspaceSessionStore();
  session.configureCapabilities(["workspace.session.v2"]);
  const opened = { contractVersion: "2.0" as const, workspaceId: "11111111-1111-4111-8111-111111111111",
    sessionEpoch: 7, state: "openedWritable" as const, openMode: "writable" as const,
    writable: true, provisional: false, phase: "idle" as const, errorCode: null };
  session.setWorkspaces([{ contractVersion: "2.0", workspaceId: opened.workspaceId,
    displayName: "A", selectedRoot: "D:\\A", activityRoot: null, storageKind: "fixed",
    coordinationStrength: "strong", lastOpenedAt: null, lastKnownHealth: "healthy",
    lastSnapshotAt: null, lastSyncAt: null, pendingSync: false }]);
  session.applySession(opened);
  const posted: BridgeMessage[] = [];
  const bridge = createHostBridge({ webview: {
    postMessage: (message: unknown) => posted.push(message as BridgeMessage),
    addEventListener: () => {}, removeEventListener: () => {},
  } });
  bridge.start(); setHostBridgeForTesting(bridge);
  const lifetime = effectScope();
  const workspace = useWorkspaceStore(); workspace.selectTable("orders");
  const history = useRevisionHistoryStore();
  const service = lifetime.run(() => useRevisionHistoryService())!;
  const scope = computed(() => history.panelOpen && history.scope === "row" && history.itemId && workspace.currentTable
    ? { collection: workspace.currentTable, itemId: history.itemId } : null);
  lifetime.run(() => useNamedRevisions(scope, useContentVersionService(), async () => {}));
  let unregisterClear = () => {};
  try {
    history.open({ scope: "row", itemId: "row1" });
    await nextTick(); await settle();
    expect(posted.filter((m) => m.type === "version.list")).toHaveLength(1);
    posted.length = 0;
    session.applySession({ ...opened, sessionEpoch: 8 });
    expect(posted.filter((m) => m.type === "version.list")).toEqual([]);
    await nextTick(); await settle();
    expect(posted.filter((m) => m.type === "version.list")).toMatchObject([
      { scope: { workspaceId: opened.workspaceId, sessionEpoch: 8 } },
    ]);
    posted.length = 0;
    unregisterClear = registerWorkspaceEpochReset("named-revisions-test-view", () => workspace.clear());
    session.applySession({ ...opened, sessionEpoch: 9, state: "switching", phase: "verifying" });
    await nextTick(); await settle();
    expect(posted.filter((m) => m.type === "version.list")).toEqual([]);
    session.applySession({ ...opened, sessionEpoch: 9 });
    await nextTick(); await settle();
    expect(posted.filter((m) => m.type === "version.list")).toEqual([]);
    workspace.selectTable("orders");
    await nextTick(); await settle();
    expect(posted.filter((m) => m.type === "version.list")).toMatchObject([
      { scope: { workspaceId: opened.workspaceId, sessionEpoch: 9 } },
    ]);
  } finally {
    unregisterClear(); service.dispose(); lifetime.stop(); bridge.stop(); setHostBridgeForTesting(null);
  }
});
