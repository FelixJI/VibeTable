import { describe, expect, it, vi } from "vitest";
import type { PluginSurfaceThemeSnapshot, PluginTaskViewSnapshot } from "@/contracts";
import { createPluginSurfaceUi, projectSurfaceTasks, SURFACE_UI_METHODS } from "./pluginSurfaceUi";
import catalog from "../../../../../contracts/plugin-surface-api.v1.json";
const theme = { contract: "vibetable.plugin-theme.v1", mode: "light", locale: "en-US", density: "compact", variables: {} } as PluginSurfaceThemeSnapshot;
const running = { taskId: "one", actionId: "save", surfaceToken: "token", revision: 3, state: "running", cancelRequested: false,
  progress: { current: 2, total: 0, message: "阶段二", cancellable: true }, result: null, error: null } as PluginTaskViewSnapshot;
function setup(tasks = [running]) {
  const cancel = vi.fn(); const show = vi.fn(); const back = vi.fn(() => true);
  const service = createPluginSurfaceUi({ token: () => "token", theme: () => theme, tasks: () => tasks, cancel, show, back });
  const call = (method: string, params: object = {}) => service.handle({ service: "ui", requestId: "request", method, params });
  return { service, call, cancel, show, back };
}
describe("surface presentation services", () => {
  it("matches the public closed catalog and supplies current presentation without data context", () => {
    expect(SURFACE_UI_METHODS).toEqual(catalog.ui.methods);
    const { call } = setup();
    expect(call("context.current")?.result).toEqual(theme);
    expect(call("context.current", { locale: "forged" })?.error?.code).toBe("plugin_capability_invalid");
    for (const method of catalog.ui.unsupported) expect(call(method)?.error?.code).toBe("plugin_api_unsupported");
  });
  it("projects only owned authority tasks, real unknown total and safe receipts without confirmation/input", () => {
    const tasks = [running, { ...running, taskId: "foreign", surfaceToken: "other" },
      { ...running, taskId: "unknown", state: "aborted", error: { code: "plugin_commit_unknown", message: "核对实际数据", recoverability: "none", details: { secret: true } },
        confirmation: { title: "secret preview" }, result: { summary: "receipt", table: { secret: true } } } as unknown as PluginTaskViewSnapshot];
    const projected = projectSurfaceTasks(tasks, "token");
    expect(projected.map(task => task.taskId)).toEqual(["one", "unknown"]);
    expect(projected[0]?.progress).toEqual({ current: 2, total: null, message: "阶段二", cancellable: true });
    expect(projected[1]?.error?.code).toBe("plugin_commit_unknown");
    expect(JSON.stringify(projected)).not.toContain("secret");
    expect(projectSurfaceTasks(Array.from({ length: 300 }, (_, i) => ({ ...running, taskId: String(i) })), "token")).toHaveLength(256);
  });
  it("controls one currently owned task and never approves or invents a terminal", () => {
    const { call, cancel } = setup([running, { ...running, taskId: "saved", state: "succeeded" },
      { ...running, taskId: "locked", progress: { ...running.progress!, cancellable: false } }, { ...running, taskId: "requested", cancelRequested: true }]);
    expect(call("tasks.cancel", { taskId: "missing" })?.error?.code).toBe("plugin_task_not_owned");
    expect(call("tasks.cancel", { taskId: "one", approved: true })?.error?.code).toBe("plugin_capability_invalid");
    expect(call("tasks.cancel", { taskId: "saved" })?.result).toEqual({ cancelRequested: false });
    expect(call("tasks.cancel", { taskId: "locked" })?.error?.code).toBe("plugin_task_not_cancellable");
    expect(call("tasks.cancel", { taskId: "requested" })?.result).toEqual({ cancelRequested: true });
    expect(call("tasks.cancel", { taskId: "one" })?.result).toEqual({ cancelRequested: true });
    expect(cancel.mock.calls).toEqual([["one"]]);
    expect(running.state).toBe("running");
    expect(call("navigation.back")?.result).toEqual({ navigated: true });
    expect(call("navigation.back", { url: "https://evil.test" })?.error?.code).toBe("plugin_capability_invalid");
  });
  it("bounds text notifications for the surface lifetime and resets only on a new token", () => {
    const { call, service, show } = setup();
    expect(call("notification.show", { message: " " })?.error?.code).toBe("plugin_capability_invalid");
    expect(call("notification.show", { message: "x".repeat(501) })?.error?.code).toBe("plugin_capability_invalid");
    for (let i = 0; i < 8; i++) expect(call("notification.show", { message: String(i) })?.result).toEqual({ shown: true });
    expect(call("notification.show", { message: "overflow" })?.error?.code).toBe("plugin_notification_limit");
    expect(show).toHaveBeenCalledTimes(8);
    service.reset();
    expect(call("notification.show", { message: "new frame" })?.result).toEqual({ shown: true });
    expect(service.handle(null)).toBeNull();
    expect(service.handle({ service: "other" })).toBeNull();
    expect(service.handle({ service: "ui", requestId: "x".repeat(129) })?.error?.code).toBe("plugin_capability_invalid");
    const cycle: Record<string, unknown> = {}; cycle.self = cycle;
    expect(call("notification.show", cycle)?.error?.code).toBe("plugin_capability_invalid");
  });
});
