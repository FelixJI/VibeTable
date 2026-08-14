import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NModal, NSelect } from "naive-ui";

import RelationEditorPanel from "./RelationEditorPanel.vue";
import type {
  FieldDefinitionV2,
  LogicalTypeV2,
  NormalizedRelationDescriptor,
  RelationTargetRef,
} from "@/contracts";

function v2Field(
  fieldId: string,
  physicalName: string,
  displayName: string,
  logicalType: LogicalTypeV2,
  options: {
    required?: boolean;
    select?: readonly { optionId: string; label: string }[];
    relation?: boolean;
  } = {},
): FieldDefinitionV2 {
  return {
    contract: "vibetable.schema.v2",
    identity: { fieldId, physicalName, providerFieldId: `pb_${fieldId}` },
    displayName,
    help: "",
    logicalType,
    lifecycle: { state: "active", retiredAt: null },
    value: {
      required: options.required ?? false,
      default: { enabled: false, value: null, source: "recommended", defaultsVersion: 1 },
      presence: { mode: "native" },
    },
    constraints: {
      unique: { enabled: false, blankPolicy: "ignoreMissing" },
      range: { min: null, max: null },
      length: { min: null, max: null },
      pattern: { enabled: false, value: "" },
      domains: { only: [], except: [] },
      selection: { min: 0, max: null },
    },
    storage: {
      kind: logicalType === "relation" ? "pocketbase-relation" : "pocketbase-text",
      options: { onlyInt: false, maxSize: 0, convertURLs: false, presentable: true },
    },
    display: {
      kind: logicalType === "relation" ? "relation" : "text",
      preset: "default", displayScale: 0, scaleMode: "fixed", trimTrailingZeros: false,
      useGrouping: false, currency: "", percentStorage: "ratio", unit: null,
      precision: "exact", timezone: "local", mode: "plain", trueLabel: "是", falseLabel: "否",
    },
    ...(options.select ? {
      select: { options: options.select.map((item, order) => ({
        ...item, color: "neutral", order, state: "active" as const,
      })) },
    } : {}),
    ...(options.relation ? {
      relation: {
        targetTableId: "regions", cardinality: "one", deletePolicy: "restrict",
        displayFieldId: "name_id",
      },
    } : {}),
  };
}

const descriptor: NormalizedRelationDescriptor = {
  relationId: "orders.customer",
  fieldRef: "customer",
  sourceCollection: "orders",
  kind: "m2o",
  relatedCollection: "customers",
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
          v2Field("name_id", "name", "名称", "text", { required: true }),
          v2Field("region_id", "region", "区域", "text", { required: true }),
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
          v2Field("name_id", "name", "名称", "text", { required: true }),
          v2Field("region_id", "region", "区域", "select", {
            required: true, select: [{ optionId: "east", label: "华东" }],
          }),
          v2Field("tags_id", "tags", "标签", "multiSelect", {
            required: true, select: [{ optionId: "priority", label: "重点" }],
          }),
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
      name: "Grace", region: "east", tags: ["priority"],
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
          region: [{ collection: "regions", itemId: "region-1", label: "华东" }],
        },
        targetFields: [
          v2Field("name_id", "name", "名称", "text", { required: true }),
          v2Field("region_id", "region", "区域", "relation", {
            required: true, relation: true,
          }),
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
