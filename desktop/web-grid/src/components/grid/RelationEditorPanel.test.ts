import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NModal, NSelect } from "naive-ui";

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
  quickCreateEligible: true,
  state: "valid",
  displayTemplate: "{{name}}",
  diagnostics: [],
};

const target: RelationTargetRef = {
  collection: "customers",
  itemId: "customer-1",
  label: "Acme",
  secondaryLabel: "华东区 · A-001",
  junctionValues: {},
};

describe("RelationEditorPanel accessibility", () => {
  it("closes a controlled modal when Esc requests show=false", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        fieldLabel: "客户",
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
        fieldLabel: "客户",
        selected: [target],
        candidates: [target],
      },
      global: { stubs: { teleport: true } },
    });

    expect(wrapper.get(".relation-editor__candidate").attributes("aria-pressed"))
      .toBe("true");
    expect(wrapper.find('input[aria-label="搜索目标记录"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("多对一 · 客户");
    expect(wrapper.text()).not.toContain("orders.customer");
    expect(wrapper.text()).toContain("华东区 · A-001");
  });

  it("opens a candidate without selecting it", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor,
        selected: [],
        candidates: [target],
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-open-target"]').trigger("click");
    expect(wrapper.emitted("open")).toEqual([[target]]);
    expect(wrapper.emitted("select")).toBeUndefined();
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

  it("uses the complete record form when another required field blocks quick create", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor: {
          ...descriptor,
          quickCreateEligible: false,
          quickCreateReason: "目标表字段“区域”必须填写",
        },
        selected: [],
        candidates: [],
        query: "Grace",
        targetDisplayField: "name",
        targetFields: [
          {
            fieldId: "name_id", physicalName: "name", displayName: "名称",
            kind: "scalar", dataType: "shortText", storageType: "text",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "text", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
          {
            fieldId: "region_id", physicalName: "region", displayName: "区域",
            kind: "scalar", dataType: "shortText", storageType: "text",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "text", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
        ],
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-full-create-target"]').trigger("click");
    await wrapper.get('[data-testid="relation-full-create-region"] input').setValue("华东");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="relation-full-create-submit"]').trigger("click");
    expect(wrapper.emitted("createFull")?.[0]?.[0]).toEqual({
      name: "Grace",
      region: "华东",
    });
  });

  it("renders v1 select options and preserves multi-select values in the complete form", async () => {
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor: { ...descriptor, quickCreateEligible: false },
        selected: [], candidates: [], query: "Grace", targetDisplayField: "name",
        targetFields: [
          {
            fieldId: "name_id", physicalName: "name", displayName: "名称",
            kind: "scalar", dataType: "shortText", storageType: "text",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "text", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
          {
            fieldId: "region_id", physicalName: "region", displayName: "区域",
            kind: "scalar", dataType: "select", storageType: "select",
            nullable: false, defaultValue: null,
            constraints: [{
              kind: "enum", options: [{ value: 7, displayName: "华东" }],
            }],
            editor: { kind: "select", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
          {
            fieldId: "tags_id", physicalName: "tags", displayName: "标签",
            kind: "scalar", dataType: "multiSelect", storageType: "select",
            nullable: false, defaultValue: null,
            constraints: [{
              kind: "enum", options: [{ value: true, displayName: "重点" }],
            }],
            editor: { kind: "multiSelect", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
        ],
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-full-create-target"]').trigger("click");
    const region = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("data-testid") === "relation-full-create-region");
    const tags = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("data-testid") === "relation-full-create-tags");
    expect(region?.props("options")).toEqual([
      { label: "华东", value: "select:region_id:0" },
    ]);
    expect(tags?.props("options")).toEqual([
      { label: "重点", value: "select:tags_id:0" },
    ]);
    expect(tags?.props("multiple")).toBe(true);
    expect(wrapper.get('[data-testid="relation-full-create-submit"]').attributes("disabled"))
      .toBeDefined();
    region?.vm.$emit("update:value", "select:region_id:0");
    tags?.vm.$emit("update:value", ["select:tags_id:0"]);
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="relation-full-create-submit"]').trigger("click");
    expect(wrapper.emitted("createFull")?.[0]?.[0]).toEqual({
      name: "Grace", region: 7, tags: [true],
    });
  });

  it("selects a required relation visually in the complete record form", async () => {
    const regionRelation: NormalizedRelationDescriptor = {
      ...descriptor,
      relationId: "customers.region",
      fieldRef: "region",
      sourceCollection: "customers",
      relatedCollection: "regions",
    };
    const wrapper = mount(RelationEditorPanel, {
      props: {
        show: true,
        descriptor: { ...descriptor, quickCreateEligible: false },
        selected: [], candidates: [], query: "Grace", targetDisplayField: "name",
        targetRelations: [regionRelation],
        targetRelationOptions: {
          region: [{ collection: "regions", itemId: "region-1", label: "华东", junctionValues: {} }],
        },
        targetFields: [
          {
            fieldId: "name_id", physicalName: "name", displayName: "名称",
            kind: "scalar", dataType: "shortText", storageType: "text",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "text", config: {} }, readOnly: false,
            formula: null, relation: null, lookup: null, attachmentPolicy: null,
          },
          {
            fieldId: "region_id", physicalName: "region", displayName: "区域",
            kind: "relation", dataType: "relation", storageType: "relation",
            nullable: false, defaultValue: null, constraints: [],
            editor: { kind: "relation", config: {} }, readOnly: false,
            formula: null, relation: {}, lookup: null, attachmentPolicy: null,
          },
        ],
      },
      global: { stubs: { teleport: true } },
    });

    await wrapper.get('[data-testid="relation-full-create-target"]').trigger("click");
    const region = wrapper.findAllComponents(NSelect)
      .find(select => select.attributes("data-testid") === "relation-full-create-region");
    expect((region?.props("options") ?? []).map(option => option.label)).toEqual(["华东"]);
    expect(region?.props("remote")).toBe(true);
    region?.vm.$emit("search", "华北");
    expect(wrapper.emitted("searchCreateRelation")).toEqual([["region", "华北"]]);
    region?.vm.$emit("update:value", "region-1");
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="relation-full-create-submit"]').trigger("click");
    expect(wrapper.emitted("createFull")?.[0]?.[0]).toEqual({
      name: "Grace", region: "region-1",
    });
  });
});
