import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import ImportRelationMapping from "./ImportRelationMapping.vue";
import { setLocale } from "@/i18n";
import type { RelationImportOption } from "@/services/dataIoService";

const options: readonly RelationImportOption[] = [
  {
    targetField: "partner",
    relationId: "orders.fld_partner",
    targetCollection: "partners",
    targetDisplayName: "合作方",
    sourceDisplayName: "合作方",
    matchFields: [
      { fieldId: "fld_code", displayName: "编码" },
      { fieldId: "fld_tax", displayName: "税号" },
    ],
  },
  {
    targetField: "owner",
    relationId: "orders.fld_owner",
    targetCollection: "users",
    targetDisplayName: "用户",
    sourceDisplayName: "负责人",
    matchFields: [],
  },
];

function baseProps(overrides: Record<string, unknown> = {}) {
  return {
    sourceColumns: ["number", "Partner Code", "Partner Code", " "],
    options,
    loading: false,
    error: null,
    modelValue: [],
    disabled: false,
    ...overrides,
  };
}

describe("ImportRelationMapping", () => {
  it("rejects source headers beyond the public mapping limit without truncating", async () => {
    const wrapper = mount(ImportRelationMapping, { props: baseProps({ sourceColumns: ["长".repeat(129)] }) });
    expect(wrapper.get('[data-testid="relation-mapping-add"]').attributes("disabled")).toBeDefined();
    await wrapper.get('[data-testid="relation-mapping-add"]').trigger("click");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    expect(wrapper.get('[data-testid="relation-mapping-unavailable-sources"]').text()).toContain("128");
    wrapper.unmount();
  });

  beforeEach(() => setLocale("zh-CN"));

  it("marks duplicate and empty source headers as unselectable", () => {
    const wrapper = mount(ImportRelationMapping, {
      props: baseProps({
        modelValue: [{ sourceColumn: "number", relationId: "orders.fld_partner", matchField: "fld_code" }],
      }),
    });
    const section = wrapper.get('[data-testid="relation-mapping-section"]');
    expect(section.text()).toContain("关系列匹配");

    const sourceSelect = wrapper.findAllComponents(NSelect)[0]!;
    const optionStates = sourceSelect.props("options") as {
      value: string; disabled: boolean; label: string;
    }[];
    expect(optionStates).toEqual([
      { value: "number", disabled: true, label: "number", reason: null },
      {
        value: "Partner Code",
        disabled: true,
        label: "Partner Code · 重复表头",
        reason: "重复表头",
      },
      { value: "", disabled: true, label: "空表头 · 空表头", reason: "空表头" },
    ]);
  });

  it("adds a complete mapping row with stable identities and defaults", async () => {
    const wrapper = mount(ImportRelationMapping, {
      props: baseProps({ sourceColumns: ["number", "Partner Code"] }),
    });

    await wrapper.get('[data-testid="relation-mapping-add"]').trigger("click");

    const emitted = wrapper.emitted("update:modelValue");
    expect(emitted).toHaveLength(1);
    expect(emitted![0]).toEqual([[
      { sourceColumn: "number", relationId: "orders.fld_partner", matchField: "fld_code" },
    ]]);
  });

  it("exposes only unique-enabled match fields of the selected target", () => {
    const wrapper = mount(ImportRelationMapping, {
      props: baseProps({
        sourceColumns: ["Partner Code"],
        modelValue: [{ sourceColumn: "Partner Code", relationId: "orders.fld_partner", matchField: "fld_code" }],
      }),
    });

    const selects = wrapper.findAllComponents(NSelect);
    const matchSelect = selects[2]!;
    expect((matchSelect.props("options") as { value: string }[]).map((item) => item.value))
      .toEqual(["fld_code", "fld_tax"]);

    const targetSelect = selects[1]!;
    const targetOptions = targetSelect.props("options") as { value: string; disabled: boolean }[];
    expect(targetOptions.find((item) => item.value === "orders.fld_owner")?.disabled).toBe(true);
    expect(targetOptions.find((item) => item.value === "orders.fld_partner")?.disabled).toBe(true);
  });

  it("removes a mapping row on request", async () => {
    const wrapper = mount(ImportRelationMapping, {
      props: baseProps({
        sourceColumns: ["Partner Code"],
        modelValue: [{ sourceColumn: "Partner Code", relationId: "orders.fld_partner", matchField: "fld_code" }],
      }),
    });

    await wrapper.get('[data-testid="relation-mapping-remove-0"]').trigger("click");
    expect(wrapper.emitted("update:modelValue")![0]).toEqual([[]]);
  });

  it("shows explicit empty states instead of a dead widget", () => {
    const empty = mount(ImportRelationMapping, {
      props: baseProps({ sourceColumns: ["number"], options: [] }),
    });
    expect(empty.find('[data-testid="relation-mapping-empty"]').exists()).toBe(true);

    const noUnique = mount(ImportRelationMapping, {
      props: baseProps({
        sourceColumns: ["number"],
        options: [options[1]],
      }),
    });
    expect(noUnique.find('[data-testid="relation-mapping-empty"]').exists()).toBe(true);
  });
});
