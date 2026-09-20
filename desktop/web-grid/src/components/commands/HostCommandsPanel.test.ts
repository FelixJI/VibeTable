import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NPopconfirm, NSelect } from "naive-ui";
import HostCommandsPanel from "./HostCommandsPanel.vue";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import type { HostShortcut } from "@/contracts";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/services/bridgeContext", () => ({ useHostBridge: () => ({ request }) }));
const mounted: VueWrapper[] = [];
let saved: HostShortcut[];
const output = { outputDisplayName: "chosen.csv", rowsWritten: 2, bytesWritten: 40 };
const workspaceId = "11111111-1111-4111-8111-111111111111";
function open(epoch = 1) {
  const session = useWorkspaceSessionStore();
  session.configureCapabilities(["workspace.session.v2"]);
  session.setWorkspaces([{
    contractVersion: "2.0", workspaceId, displayName: "Test", selectedRoot: "D:\\Test",
    activityRoot: null, storageKind: "fixed", coordinationStrength: "strong", lastOpenedAt: null,
    lastKnownHealth: "healthy", lastSnapshotAt: null, lastSyncAt: null, pendingSync: false,
  }]);
  session.applySession({ contractVersion: "2.0", workspaceId, sessionEpoch: epoch,
    state: "openedWritable", openMode: "writable", writable: true, provisional: false, phase: "idle", errorCode: null });
  useWorkspaceStore().currentTable = "orders";
  useViewQueryStore().reset("orders", ["title"]);
}
async function panel() {
  const wrapper = mount(HostCommandsPanel, { attachTo: document.body });
  mounted.push(wrapper); await flushPromises(); return wrapper;
}
beforeEach(() => {
  setActivePinia(createPinia()); saved = []; request.mockReset();
  request.mockImplementation(async (method: string, params: { shortcut?: HostShortcut; shortcutId?: string }) => {
    switch (method) {
      case "command.list": return { commands: [{ commandId: "export.query", description: "Export" }] };
      case "shortcut.list": return { shortcuts: [...saved] };
      case "shortcut.save": {
        const entry = params.shortcut!;
        saved = [...saved.filter(item => item.shortcutId !== entry.shortcutId), entry]; return entry;
      }
      case "shortcut.delete": saved = saved.filter(item => item.shortcutId !== params.shortcutId); return { deleted: params.shortcutId };
      case "command.run": return { commandId: "export.query", success: true, output, error: null };
      case "shortcut.launch": return { shortcutId: params.shortcutId, launched: false, blockedReason: "cancelled", output: null };
      default: throw new Error(`Unexpected method ${method}`);
    }
  });
});
afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount(); });
describe("Host commands product panel", () => {
  it("creates, edits, reloads after reopening and deletes a persisted definition", async () => {
    open(); let wrapper = await panel();
    await wrapper.get('[data-testid="shortcut-label"] input').setValue("Query export");
    await wrapper.get('[data-testid="shortcut-save"]').trigger("click"); await flushPromises();
    const id = saved[0]!.shortcutId;
    expect(id).toMatch(/^[0-9a-f-]{36}$/);
    expect(saved[0]).toEqual({ shortcutId: id, label: "Query export", target: "built-in-command", commandId: "export.query", url: null });
    await wrapper.get('[data-testid="shortcut-label"] input').setValue("Renamed");
    await wrapper.get('[data-testid="shortcut-save"]').trigger("click"); await flushPromises();
    expect(saved).toHaveLength(1); expect(saved[0]!.shortcutId).toBe(id);
    wrapper.unmount(); mounted.splice(mounted.indexOf(wrapper), 1); wrapper = await panel();
    expect(wrapper.get('[data-testid="shortcut-row"]').text()).toContain("Renamed");
    await wrapper.get('[data-testid="shortcut-row"]').trigger("click");
    await wrapper.get('[data-testid="shortcut-delete"]').trigger("click");
    expect(saved).toHaveLength(1);
    wrapper.getComponent(NPopconfirm).vm.$emit("positive-click"); await flushPromises();
    expect(saved).toHaveLength(0); expect(wrapper.find('[data-testid="shortcut-row"]').exists()).toBe(false);
  });
  it("runs the current filtered query without accepting or persisting a path/grant", async () => {
    open();
    const view = useViewQueryStore();
    view.filters = [{ field: "title", operator: "eq", value: "selected" }];
    view.search = "selected";
    view.groups = [{ field: "title", direction: "asc", bucket: "value" }];
    view.sorts = [{ field: "title", direction: "desc", nullsLast: true }];
    const wrapper = await panel();
    await wrapper.get('[data-testid="command-run"]').trigger("click"); await flushPromises();
    const call = request.mock.calls.find(call => call[0] === "command.run")!;
    expect(call[1]).toEqual({ commandId: "export.query", params: { collection: "orders", query: {
      keyword: "selected", filters: [{ field: "title", operator: "eq", value: "selected" }],
      sorts: [{ field: "title", direction: "desc", nullsLast: true }], offset: 0, limit: 500,
    }, format: "csv" } });
    expect(wrapper.get('[data-testid="commands-notice"]').text()).toContain("chosen.csv");
    expect(wrapper.get('[data-testid="commands-notice"]').text()).toContain("2");
  });
  it("saves HTTPS targets and reports refusal without claiming the browser opened", async () => {
    open(); const wrapper = await panel();
    wrapper.findAllComponents(NSelect)[1]!.vm.$emit("update:value", "url"); await flushPromises();
    await wrapper.get('[data-testid="shortcut-label"] input').setValue("Website");
    await wrapper.get('[data-testid="shortcut-url"] input').setValue("https://example.com/help");
    await wrapper.get('[data-testid="shortcut-save"]').trigger("click"); await flushPromises();
    await wrapper.get('[data-testid="shortcut-launch"]').trigger("click"); await flushPromises();
    expect(request).toHaveBeenCalledWith("shortcut.launch", { shortcutId: saved[0]!.shortcutId });
    expect(wrapper.get('[data-testid="commands-notice"]').text()).toContain("取消");
    expect(saved[0]!.commandId).toBeNull();
  });
  it("retains a failed draft and drops a late reply after workspace retirement", async () => {
    open(); const wrapper = await panel();
    await wrapper.get('[data-testid="shortcut-label"] input').setValue("Keep me");
    request.mockRejectedValueOnce(new Error("Storage unavailable"));
    await wrapper.get('[data-testid="shortcut-save"]').trigger("click"); await flushPromises();
    expect(wrapper.get('[data-testid="commands-error"]').text()).toContain("Storage unavailable");
    expect((wrapper.get('[data-testid="shortcut-label"] input').element as HTMLInputElement).value).toBe("Keep me");
    let resolve!: (value: unknown) => void;
    request.mockImplementationOnce(() => new Promise(value => { resolve = value; }));
    await wrapper.get('[data-testid="command-run"]').trigger("click");
    useWorkspaceSessionStore().closeSession(); await flushPromises();
    resolve({ success: true, output }); await flushPromises();
    expect(wrapper.find('[data-testid="commands-notice"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="command-run"]').attributes("disabled")).toBeDefined();
    open(2); await flushPromises();
    expect(wrapper.get('[data-testid="command-run"]').attributes("disabled")).toBeUndefined();
  });
});
