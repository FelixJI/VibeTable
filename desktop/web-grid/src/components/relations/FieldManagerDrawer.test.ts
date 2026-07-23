import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import FieldManagerDrawer from "./FieldManagerDrawer.vue";
import type { RelationChangePlan, SchemaSnapshot } from "@/contracts";

const schema: SchemaSnapshot = {
  collection: "orders",
  primaryKey: "id",
  columns: [],
  normalizedRelations: [],
  schemaRevision: "schema-1",
  permissionRevision: "permission-1",
  capabilityHash: "cap-1",
  lookupRevision: "lookup-1",
};

const destructivePlan: RelationChangePlan = {
  planId: "plan-1",
  collection: "orders",
  expectedSchemaRevision: "schema-1",
  action: "delete",
  relationId: "orders.contract",
  steps: [{ resource: "relation", action: "delete", key: "orders.contract", destructive: true }],
  affectedLookupIds: ["orders.contract_price"],
  diagnostics: [],
  canApply: true,
};

describe("FieldManagerDrawer", () => {
  const mounted: ReturnType<typeof mount>[] = [];
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount());
    document.body.innerHTML = "";
  });

  it("keeps destructive relation cascade opt-in and emits the frozen plan identity", async () => {
    const wrapper = mount(FieldManagerDrawer, {
      attachTo: document.body,
      props: {
        show: true,
        collection: "orders",
        collections: ["orders", "contracts"],
        schema,
        schemas: [schema],
        lookups: [],
        lookupCatalog: [],
        busy: false,
        error: null,
        relationPlan: destructivePlan,
        lookupValidation: null,
        lookupPreview: null,
      },
    });
    mounted.push(wrapper);
    await flushPromises();

    const apply = document.body.querySelector('[data-testid="apply-relation-plan"]') as HTMLButtonElement;
    expect(apply).toBeTruthy();
    expect(apply.disabled).toBe(true);

    const cascade = document.body.querySelector('[data-testid="cascade-lookup-orders.contract_price"]') as HTMLElement;
    cascade.click();
    await flushPromises();
    expect(apply.disabled).toBe(false);
    apply.click();
    await flushPromises();

    expect(wrapper.emitted("applyRelation")?.[0]?.[0]).toMatchObject({
      planId: "plan-1",
      expectedSchemaRevision: "schema-1",
      cascadeLookupIds: ["orders.contract_price"],
    });
  });

  it("states that display fields are never inferred", async () => {
    const wrapper = mount(FieldManagerDrawer, {
      attachTo: document.body,
      props: {
        show: true, collection: "orders", collections: ["orders", "contracts"], schema, schemas: [schema],
        lookups: [], lookupCatalog: [], busy: false, error: null, relationPlan: null,
        lookupValidation: null, lookupPreview: null,
      },
    });
    mounted.push(wrapper);
    await flushPromises();
    expect(document.body.textContent).toContain("系统不会猜测显示字段");
  });
});
