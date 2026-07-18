import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableAdminStore } from "./tableAdminStore";

describe("tableAdminStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle with empty form", () => {
    const s = useTableAdminStore();
    expect(s.phase).toBe("idle");
    expect(s.form.name).toBe("");
    expect(s.form.fields).toEqual([]);
    expect(s.canSubmit).toBe(false);
  });

  it("openCreate resets form to one empty field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    expect(s.phase).toBe("creating");
    expect(s.form.fields).toHaveLength(1);
  });

  it("addField appends an empty field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.addField();
    expect(s.form.fields).toHaveLength(2);
  });

  it("updateField patches a single field by index", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.updateField(0, { name: "id", type: "integer" });
    expect(s.form.fields[0]).toMatchObject({ name: "id", type: "integer" });
  });

  it("removeField removes by index", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.addField();
    s.removeField(0);
    expect(s.form.fields).toHaveLength(1);
  });

  it("canSubmit true when name + at least one named field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.form.name = "orders";
    s.updateField(0, { name: "id" });
    expect(s.canSubmit).toBe(true);
  });

  it("requestDelete sets pendingDelete and phase=deleting", () => {
    const s = useTableAdminStore();
    s.requestDelete("users");
    expect(s.phase).toBe("deleting");
    expect(s.pendingDelete).toBe("users");
  });

  it("succeed returns to idle and clears form", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.succeed();
    expect(s.phase).toBe("idle");
    expect(s.form.name).toBe("");
  });

  it("fail records error", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.fail("bad name");
    expect(s.phase).toBe("failed");
    expect(s.error).toBe("bad name");
  });
});
