import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useStartupStore } from "./startupStore";

describe("startupStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("starts gated and applies only host-authoritative readiness state", () => {
    const store = useStartupStore();
    expect(store.phase).toBe("starting");
    store.applyHostState({
      phase: "faulted",
      stage: "Local runtime unavailable",
      canCancel: true,
      logs: [
        { time: "08:30:01", source: "复用", message: "依赖校验缓存有效。" },
      ],
    });
    expect(store).toMatchObject({
      phase: "faulted",
      stage: "Local runtime unavailable",
      canCancel: true,
      logs: [
        { time: "08:30:01", source: "复用", message: "依赖校验缓存有效。" },
      ],
    });
    expect(Object.keys(store.$state)).not.toContain("email");
    expect(Object.keys(store.$state)).not.toContain("rememberPassword");
    expect(Object.keys(store.$state)).not.toContain("autoLogin");
    expect(localStorage.length).toBe(0);
  });
});
