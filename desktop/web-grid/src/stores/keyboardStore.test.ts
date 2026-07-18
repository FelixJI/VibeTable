import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useKeyboardStore } from "./keyboardStore";

describe("keyboardStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts with lastFired null", () => {
    const s = useKeyboardStore();
    expect(s.lastFired).toBeNull();
  });

  it("fire records the action id", () => {
    const s = useKeyboardStore();
    s.fire("copy");
    expect(s.lastFired).toBe("copy");
  });
});
