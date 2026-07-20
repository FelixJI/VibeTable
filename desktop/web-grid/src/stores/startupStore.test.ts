import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useStartupStore } from "./startupStore";

describe("startupStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("starts gated and applies only host-authoritative non-sensitive state", () => {
    const store = useStartupStore();
    expect(store.phase).toBe("starting");
    store.applyHostState({
      phase: "login",
      stage: "Authentication required",
      email: "user@example.com",
      rememberPassword: true,
      autoLogin: true,
      canCancel: true,
    });
    expect(store).toMatchObject({
      phase: "login",
      stage: "Authentication required",
      email: "user@example.com",
      rememberPassword: true,
      autoLogin: true,
      canCancel: true,
    });
    expect(Object.keys(store.$state)).not.toContain("password");
    expect(Object.keys(store.$state)).not.toContain("otp");
    expect(JSON.stringify(store.$state)).not.toMatch(/password\s*[:=]\s*["']?secret|otp/i);
    expect(localStorage.length).toBe(0);
  });
});
