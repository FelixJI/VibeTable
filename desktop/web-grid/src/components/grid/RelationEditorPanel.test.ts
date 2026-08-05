import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NModal } from "naive-ui";

import RelationEditorPanel from "./RelationEditorPanel.vue";
import type { NormalizedRelationDescriptor, RelationTargetRef } from "@/contracts";

const descriptor: NormalizedRelationDescriptor = {
  relationId: "orders.customer",
  fieldRef: "customer",
  sourceCollection: "orders",
  kind: "m2o",
  relatedCollection: "customers",
  allowedCollections: [],
  junction: null,
  unique: true,
  nullable: true,
  onDelete: "nullify",
  preset: "standard",
  selfRelation: false,
  managed: true,
  state: "valid",
  displayTemplate: "{{name}}",
  diagnostics: [],
};

const target: RelationTargetRef = {
  collection: "customers",
  itemId: "customer-1",
  label: "Acme",
  junctionValues: {},
};

describe("RelationEditorPanel accessibility", () => {
  it("closes a controlled modal when Esc requests show=false", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        selected: [],
        candidates: [target],
      },
      global: { stubs: { teleport: true } },
    });

    wrapper.findComponent(NModal).vm.$emit("update:show", false);
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("exposes relation choices as pressed toggle buttons", () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        selected: [target],
        candidates: [target],
      },
      global: { stubs: { teleport: true } },
    });

    expect(wrapper.get(".relation-editor__candidate").attributes("aria-pressed"))
      .toBe("true");
    expect(wrapper.find('input[aria-label="搜索目标记录"]').exists()).toBe(true);
  });

  it("offers explicit pagination when a large relation has more records", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        selected: [],
        candidates: [target],
        total: 10_001,
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-load-more"]').trigger("click");
    expect(wrapper.emitted("loadMore")).toHaveLength(1);
    expect(wrapper.text()).toContain("1 / 10001");
  });

  it("offers lightweight target creation from the visible search label", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        selected: [],
        candidates: [],
        query: "Grace",
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-create-target"]').trigger("click");
    expect(wrapper.emitted("create")).toEqual([["Grace"]]);
    expect(wrapper.text()).toContain("Grace");
  });
});
