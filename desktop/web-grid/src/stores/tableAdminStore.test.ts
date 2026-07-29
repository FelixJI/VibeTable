import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableAdminStore } from "./tableAdminStore";

describe("tableAdminStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle with an empty display-name intent", () => {
    const store = useTableAdminStore();
    expect(store.phase).toBe("idle");
    expect(store.form.name).toBe("");
    expect(store.autoDateProducerEnabled).toBe(false);
    expect(store.canSubmit).toBe(false);
  });

  it("opens and validates only the display-name intent", () => {
    const store = useTableAdminStore();
    store.form.name = "stale";
    store.openCreate();
    expect(store.phase).toBe("creating");
    expect(store.form.name).toBe("");
    expect(store.error).toBeNull();

    store.form.name = "Orders";
    expect(store.canSubmit).toBe(true);
    store.beginSubmit();
    expect(store.phase).toBe("submitting");
    expect(store.canSubmit).toBe(false);
    store.fail("temporary failure");
    expect(store.canSubmit).toBe(true);
  });

  it("tracks collection capabilities and host feature flags", () => {
    const store = useTableAdminStore();
    store.setCollections([{
      collection: "tbl_orders",
      displayName: "Orders",
      metadata: { capabilityHash: "cap_1" },
    }]);
    store.setAutoDateProducerEnabled(true);
    expect(store.collections).toHaveLength(1);
    expect(store.collections[0]?.collection).toBe("tbl_orders");
    expect(store.autoDateProducerEnabled).toBe(true);
  });

  it("tracks delete, failure, success, and close lifecycle state", () => {
    const store = useTableAdminStore();
    store.requestDelete("users");
    expect(store.phase).toBe("deleting");
    expect(store.pendingDelete).toBe("users");

    store.fail("bad name");
    expect(store.phase).toBe("failed");
    expect(store.error).toBe("bad name");
    store.close();
    expect(store.phase).toBe("idle");
    expect(store.pendingDelete).toBeNull();
    expect(store.error).toBeNull();

    store.openCreate();
    store.form.name = "Orders";
    store.succeed();
    expect(store.phase).toBe("idle");
    expect(store.form.name).toBe("");
  });
});
