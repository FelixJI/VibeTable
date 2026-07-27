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
    expect(s.form.indexes).toEqual([]);
    expect(s.autoDateProducerEnabled).toBe(false);
    expect(s.canSubmit).toBe(false);
  });

  it("openCreate resets form to one empty field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    expect(s.phase).toBe("creating");
    expect(s.form.fields).toHaveLength(1);
    expect(s.form.includeCreatedAt).toBe(true);
    expect(s.form.includeUpdatedAt).toBe(true);
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

  it("owns index drafts and prunes field references when a field is removed", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.updateField(0, { name: "status" });
    s.addField("dateTime");
    s.updateField(1, { name: "created_at" });
    s.addIndex();
    s.form.indexes[0]!.name = "idx_status_created";
    s.form.indexes[0]!.fieldClientIds = s.form.fields.map((field) => field.clientId);

    const removedClientId = s.form.fields[0]!.clientId;
    s.removeField(0);

    expect(s.form.indexes[0]!.fieldClientIds).not.toContain(removedClientId);
    expect(s.form.indexes[0]!.fieldClientIds).toEqual([s.form.fields[0]!.clientId]);
  });

  it("blocks submission until every configured index is locally valid", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.form.name = "订单";
    s.updateField(0, { name: "status" });
    s.addIndex();
    expect(s.canSubmit).toBe(false);
    expect(s.localIndexErrors.map((error) => error.path)).toEqual([
      "indexes[0].name",
      "indexes[0].fieldIds",
    ]);

    s.form.indexes[0]!.name = "idx_status";
    s.form.indexes[0]!.fieldClientIds = [s.form.fields[0]!.clientId];
    expect(s.canSubmit).toBe(true);
  });

  it("canSubmit true when name + at least one named field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.form.name = "订单";
    s.updateField(0, { name: "订单编号" });
    expect(s.canSubmit).toBe(true);
  });

  it("distinguishes editable and in-flight create phases and permits retry after failure", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.form.name = "订单";
    s.updateField(0, { name: "订单编号" });

    s.beginSubmit();
    expect(s.phase).toBe("submitting");
    expect(s.canSubmit).toBe(false);

    s.fail("temporary failure");
    expect(s.phase).toBe("failed");
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
