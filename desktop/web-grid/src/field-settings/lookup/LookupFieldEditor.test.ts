import { afterEach, describe, expect, it } from "vitest";
import { nextTick } from "vue";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NInput, NInputNumber, NSelect } from "naive-ui";
import LookupFieldEditor from "./LookupFieldEditor.vue";
import type { LookupConditionFieldOption } from "./lookupCondition";
import type { LookupSpecV2 } from "@/contracts";

type LookupValue = LookupSpecV2;

interface EditorExtraProps {
  previewLoading?: boolean;
  previewError?: string | null;
  previewReady?: boolean;
  previewValue?: unknown;
}

const mounted: VueWrapper[] = [];

const pathValue: LookupValue = {
  path: [{ relationFieldId: "fld_customer" }],
  targetFieldId: "fld_name",
};
const relationOptions = [[
  { label: "客户", value: "fld_customer" },
  { label: "账户", value: "fld_account", many: true },
]];
const targetFieldOptions = [
  { label: "名称", value: "fld_name", logicalType: "text" },
  { label: "余额", value: "fld_balance", logicalType: "number" },
  { label: "单价", value: "fld_price", logicalType: "number" },
];
const sourceTableOptions = [
  { label: "订单表", value: "tbl_orders" },
  { label: "商品表", value: "tbl_products" },
];
// 两个同显示名““金额（元）””的字段证明选择只按稳定 fieldId 解析，显示名中的
// 中文引号、括号与重名不影响契约。
const conditionFieldOptions: LookupConditionFieldOption[] = [
  { label: "“金额（元）”", value: "fld_amount", logicalType: "number",
    filterOperators: ["eq", "ne", "gt", "gte", "lt", "lte", "contains", "is_null", "is_not_null"] },
  { label: "“金额（元）”", value: "fld_amount_dup", logicalType: "number",
    filterOperators: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"] },
  { label: "备注", value: "fld_note", logicalType: "text",
    filterOperators: ["eq", "ne", "contains", "gt", "is_null", "is_not_null"] },
  { label: "已付款", value: "fld_paid", logicalType: "bool",
    filterOperators: ["eq", "ne", "is_null", "is_not_null"] },
  { label: "下单日期", value: "fld_order_date", logicalType: "date",
    filterOperators: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"] },
  { label: "更新时间", value: "fld_updated_at", logicalType: "dateTime",
    filterOperators: ["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"] },
  { label: "状态", value: "fld_status", logicalType: "select",
    filterOperators: ["eq", "ne", "is_null", "is_not_null"],
    selectOptions: [
      { optionId: "opt_pending", label: "待付款" },
      { optionId: "opt_paid", label: "已付款" },
    ] },
  { label: "附件", value: "fld_files", logicalType: "file", filterOperators: ["eq"] },
];
const currentFieldOptions: LookupConditionFieldOption[] = [
  { label: "本表金额", value: "fld_local_amount", logicalType: "number", filterOperators: ["eq"] },
  { label: "本表备注", value: "fld_local_note", logicalType: "text", filterOperators: ["eq"] },
  { label: "本表已付款", value: "fld_local_paid", logicalType: "bool", filterOperators: ["eq"] },
  { label: "本表状态", value: "fld_local_status", logicalType: "select", filterOperators: ["eq"] },
];
const conditionValue: LookupValue = {
  path: [],
  targetFieldId: "fld_price",
  condition: {
    sourceTableId: "tbl_orders",
    match: "all",
    distinct: false,
    rules: [
      { sourceFieldId: "fld_amount", operator: "gte", operand: { kind: "constant", value: 100 } },
    ],
  },
};

function mountEditor(value: LookupValue, extra: EditorExtraProps = {}): VueWrapper {
  const wrapper = mount(LookupFieldEditor, {
    props: {
      value,
      relationOptions,
      targetFieldOptions,
      maxDepth: 8,
      sourceTableOptions,
      currentFieldOptions,
      conditionFieldOptions,
      ...extra,
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function findSelect(wrapper: VueWrapper, testid: string): VueWrapper {
  const match = wrapper.findAllComponents(NSelect)
    .find(component => component.attributes("data-testid") === testid);
  if (!match) throw new Error(`NSelect not found: ${testid}`);
  return match;
}

function findComponentByTestId(wrapper: VueWrapper, component: typeof NInput | typeof NInputNumber, testid: string): VueWrapper {
  const match = wrapper.findAllComponents(component)
    .find(item => item.attributes("data-testid") === testid);
  if (!match) throw new Error(`component not found: ${testid}`);
  return match;
}

function optionValues(select: VueWrapper): (string | number)[] {
  const options = componentProp(select, "options") as Array<{ value: string | number }>;
  return options.map(option => option.value);
}

function componentProp(wrapper: VueWrapper, name: string): unknown {
  const props = wrapper.vm.$props as Record<string, unknown>;
  return props[name];
}

async function setSelect(wrapper: VueWrapper, testid: string, value: string | number | null): Promise<void> {
  findSelect(wrapper, testid).vm.$emit("update:value", value);
  await nextTick();
}

async function setInputNumber(wrapper: VueWrapper, testid: string, value: number | null): Promise<void> {
  findComponentByTestId(wrapper, NInputNumber, testid).vm.$emit("update:value", value);
  await nextTick();
}

async function openEditor(wrapper: VueWrapper): Promise<void> {
  await wrapper.get('[data-testid="lookup-editor-entry"]').trigger("click");
}

async function commit(wrapper: VueWrapper): Promise<void> {
  await wrapper.get('[data-testid="lookup-editor-commit"]').trigger("click");
}

function commitDisabled(wrapper: VueWrapper): boolean {
  return wrapper.get('[data-testid="lookup-editor-commit"]').attributes("disabled") !== undefined;
}

function lastDraft(wrapper: VueWrapper): unknown {
  return wrapper.emitted("draftChange")?.at(-1)?.[0];
}

afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount());
});

describe("LookupFieldEditor", () => {
  it("沿用路径模式的提交契约，并在每次编辑后发出完整草稿或 null", async () => {
    const wrapper = mountEditor(pathValue);
    expect(wrapper.text()).toContain("客户");
    expect(wrapper.text()).toContain("名称");
    expect(wrapper.find('[data-testid="lookup-relation-step-0"]').exists()).toBe(false);

    await openEditor(wrapper);
    // 进入编辑即用保存的定义发起预览草稿。
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_name",
    });

    await setSelect(wrapper, "lookup-relation-step-0", "fld_account");
    expect(wrapper.emitted("pathChange")).toEqual([[[{ relationFieldId: "fld_account" }]]]);
    expect(lastDraft(wrapper)).toBeNull();
    await setSelect(wrapper, "lookup-target-field", "fld_balance");
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_account" }],
      targetFieldId: "fld_balance",
    });

    // 路径与条件互斥：切到条件模式后草稿立即失效，切回也需重选返回字段。
    await setSelect(wrapper, "lookup-mode", "condition");
    expect(lastDraft(wrapper)).toBeNull();
    await setSelect(wrapper, "lookup-mode", "path");
    expect(lastDraft(wrapper)).toBeNull();
    await setSelect(wrapper, "lookup-target-field", "fld_balance");

    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[
      { path: [{ relationFieldId: "fld_account" }], targetFieldId: "fld_balance" },
    ]]);
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_account" }],
      targetFieldId: "fld_balance",
    });
  });

  it("路径不完整时禁止确认，取消恢复原路径并清空草稿", async () => {
    const wrapper = mountEditor(pathValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-relation-step-0", "");
    expect(commitDisabled(wrapper)).toBe(true);

    await wrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(lastDraft(wrapper)).toBeNull();
    expect(wrapper.text()).toContain("客户");
  });

  it("条件模式提交完整 LookupSpec：path 为空数组，condition 内字段为稳定 ID", async () => {
    const wrapper = mountEditor(conditionValue);
    expect(wrapper.text()).toContain("订单表");
    expect(wrapper.text()).toContain("全部匹配");
    await openEditor(wrapper);
    expect(componentProp(findSelect(wrapper, "lookup-condition-source-table"), "value")).toBe("tbl_orders");

    // 常量 100 → 0：0 是合法类型化常量，不得被当作未填而丢弃。
    await setInputNumber(wrapper, "lookup-rule-constant-number-0", 0);
    const expected: LookupValue = {
      path: [],
      targetFieldId: "fld_price",
      condition: {
        sourceTableId: "tbl_orders",
        match: "all",
        distinct: false,
        rules: [
          { sourceFieldId: "fld_amount", operator: "gte", operand: { kind: "constant", value: 0 } },
        ],
      },
    };
    expect(lastDraft(wrapper)).toEqual(expected);
    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[expected]]);
  });

  it("常量保留 0 / false / 空字符串，空值运算符不带 operand", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_orders");
    expect(lastDraft(wrapper)).toBeNull();

    // 规则 0：数字常量 0。
    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_amount");
    await setSelect(wrapper, "lookup-rule-operator-0", "gte");
    await setSelect(wrapper, "lookup-rule-operand-kind-0", "constant");
    await setInputNumber(wrapper, "lookup-rule-constant-number-0", 0);

    // 规则 1：布尔常量 false（选择器输入）。
    await wrapper.get('[data-testid="lookup-condition-add-rule"]').trigger("click");
    await setSelect(wrapper, "lookup-rule-source-field-1", "fld_paid");
    await setSelect(wrapper, "lookup-rule-operand-kind-1", "constant");
    await setSelect(wrapper, "lookup-rule-constant-bool-1", "false");

    // 规则 2：文本常量 ""（未输入即空字符串，空串本身是类型化值）。
    await wrapper.get('[data-testid="lookup-condition-add-rule"]').trigger("click");
    await setSelect(wrapper, "lookup-rule-source-field-2", "fld_note");
    await setSelect(wrapper, "lookup-rule-operand-kind-2", "constant");
    expect(componentProp(findComponentByTestId(wrapper, NInput, "lookup-rule-constant-text-2"), "value")).toBe("");

    // 规则 3：select 字段 is_null，规则不得携带 operand 属性。
    await wrapper.get('[data-testid="lookup-condition-add-rule"]').trigger("click");
    await setSelect(wrapper, "lookup-rule-source-field-3", "fld_status");
    await setSelect(wrapper, "lookup-rule-operator-3", "is_null");

    await setSelect(wrapper, "lookup-target-field", "fld_price");
    await setSelect(wrapper, "lookup-condition-match", "any");
    await setSelect(wrapper, "lookup-aggregation", "distinct");

    const payload: LookupValue = {
      path: [],
      targetFieldId: "fld_price",
      // canonical：顶层 aggregation 携带去重，旧 condition.distinct 固定为 false。
      aggregation: "distinct",
      condition: {
        sourceTableId: "tbl_orders",
        match: "any",
        distinct: false,
        rules: [
          { sourceFieldId: "fld_amount", operator: "gte", operand: { kind: "constant", value: 0 } },
          { sourceFieldId: "fld_paid", operator: "eq", operand: { kind: "constant", value: false } },
          { sourceFieldId: "fld_note", operator: "eq", operand: { kind: "constant", value: "" } },
          { sourceFieldId: "fld_status", operator: "is_null" },
        ],
      },
    };
    expect(lastDraft(wrapper)).toEqual(payload);
    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[payload]]);
    expect("operand" in (payload.condition?.rules[3] ?? {})).toBe(false);
  });

  it("日期与日期时间常量输出有效 ISO 字符串，选项常量按 optionId 提交", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_orders");

    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_order_date");
    await setSelect(wrapper, "lookup-rule-operator-0", "gte");
    await setSelect(wrapper, "lookup-rule-operand-kind-0", "constant");
    await setSelect(wrapper, "lookup-target-field", "fld_price");
    expect(commitDisabled(wrapper)).toBe(true); // 日期常量为空（非有效 ISO）时不可提交
    await wrapper.get('[data-testid="lookup-rule-constant-date-0"]').setValue("2026-01-02");

    await wrapper.get('[data-testid="lookup-condition-add-rule"]').trigger("click");
    await setSelect(wrapper, "lookup-rule-source-field-1", "fld_updated_at");
    await setSelect(wrapper, "lookup-rule-operand-kind-1", "constant");
    await wrapper.get('[data-testid="lookup-rule-constant-datetime-1"]').setValue("2026-01-02T03:04");

    await wrapper.get('[data-testid="lookup-condition-add-rule"]').trigger("click");
    await setSelect(wrapper, "lookup-rule-source-field-2", "fld_status");
    await setSelect(wrapper, "lookup-rule-operand-kind-2", "constant");
    await setSelect(wrapper, "lookup-rule-constant-select-2", "opt_paid");

    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[
      {
        path: [],
        targetFieldId: "fld_price",
        condition: {
          sourceTableId: "tbl_orders",
          match: "all",
          distinct: false,
          rules: [
            { sourceFieldId: "fld_order_date", operator: "gte",
              operand: { kind: "constant", value: "2026-01-02" } },
            // datetime-local 无 zone；提交统一转 RFC3339 UTC，保持同一时点。
            { sourceFieldId: "fld_updated_at", operator: "eq",
              operand: { kind: "constant", value: new Date("2026-01-02T03:04").toISOString() } },
            // select 常量提交 optionId，而非显示文本“已付款”。
            { sourceFieldId: "fld_status", operator: "eq",
              operand: { kind: "constant", value: "opt_paid" } },
          ],
        },
      },
    ]]);
  });

  it("运算符取类型闭集与公开 filterOperators 的交集，当前行字段仅列兼容类型", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_orders");

    // 不支持的基础类型（file）不进入来源字段选项。
    expect(optionValues(findSelect(wrapper, "lookup-rule-source-field-0")))
      .not.toContain("fld_files");

    // 重名““金额（元）””：按稳定 fieldId 选择，不做显示名解析。
    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_amount_dup");
    // number ∩ published（含 contains）不含 contains。
    expect(optionValues(findSelect(wrapper, "lookup-rule-operator-0")))
      .toEqual(["eq", "ne", "gt", "gte", "lt", "lte", "is_null", "is_not_null"]);

    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_note");
    // text ∩ published（含 gt）不含 gt。
    expect(optionValues(findSelect(wrapper, "lookup-rule-operator-0")))
      .toEqual(["eq", "ne", "contains", "is_null", "is_not_null"]);

    await setSelect(wrapper, "lookup-rule-operand-kind-0", "field");
    expect(optionValues(findSelect(wrapper, "lookup-rule-operand-field-0")))
      .toEqual(["fld_local_note"]);
    await setSelect(wrapper, "lookup-rule-operand-field-0", "fld_local_note");
    await setSelect(wrapper, "lookup-target-field", "fld_price");

    const payload: LookupValue = {
      path: [],
      targetFieldId: "fld_price",
      condition: {
        sourceTableId: "tbl_orders",
        match: "all",
        distinct: false,
        rules: [
          { sourceFieldId: "fld_note", operator: "eq",
            operand: { kind: "field", fieldId: "fld_local_note" } },
        ],
      },
    };
    expect(lastDraft(wrapper)).toEqual(payload);
    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[payload]]);
  });

  it("切换来源表清空返回字段与条件，旧草稿立即失效", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setInputNumber(wrapper, "lookup-rule-constant-number-0", 5);
    expect(lastDraft(wrapper)).not.toBeNull();

    await setSelect(wrapper, "lookup-condition-source-table", "tbl_products");
    expect(wrapper.emitted("sourceTableChange")).toEqual([["tbl_products"]]);
    expect(componentProp(findSelect(wrapper, "lookup-target-field"), "value")).toBeNull();
    expect(componentProp(findSelect(wrapper, "lookup-rule-source-field-0"), "value")).toBeNull();
    expect(commitDisabled(wrapper)).toBe(true);
    expect(lastDraft(wrapper)).toBeNull();
  });

  it("取消恢复原值并向父层恢复原来源表", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_products");
    await wrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.emitted("sourceTableChange")).toEqual([["tbl_products"], ["tbl_orders"]]);
    expect(lastDraft(wrapper)).toBeNull();
    expect(wrapper.text()).toContain("订单表");
  });

  it("编辑态卸载发 null 草稿，未编辑卸载不产生草稿事件", async () => {
    const editing = mountEditor(conditionValue);
    await openEditor(editing);
    await setInputNumber(editing, "lookup-rule-constant-number-0", 1);
    mounted.splice(mounted.indexOf(editing), 1);
    editing.unmount();
    expect(lastDraft(editing)).toBeNull();

    const idle = mountEditor(pathValue);
    mounted.splice(mounted.indexOf(idle), 1);
    idle.unmount();
    expect(idle.emitted("draftChange")).toBeUndefined();
  });

  it("样例结果只显示权威 preview props，不在本地代算", async () => {
    const wrapper = mountEditor(conditionValue, { previewLoading: true });
    await openEditor(wrapper);
    expect(wrapper.find('[data-testid="lookup-preview-loading"]').exists()).toBe(true);

    await wrapper.setProps({ previewLoading: false, previewError: "后端超时" });
    expect(wrapper.get('[data-testid="lookup-preview-error"]').text()).toContain("后端超时");

    await wrapper.setProps({ previewError: null, previewReady: true, previewValue: ["甲", "乙"] });
    expect(wrapper.get('[data-testid="lookup-preview-value"]').text()).toContain("甲、乙");

    await wrapper.setProps({ previewValue: [] });
    expect(wrapper.get('[data-testid="lookup-preview-value"]').text()).toContain("[]");
  });

  it("dateTime 常量提交 RFC3339 并在重新编辑时无损回显同一时点", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_orders");
    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_updated_at");
    await setSelect(wrapper, "lookup-rule-operand-kind-0", "constant");
    await setSelect(wrapper, "lookup-target-field", "fld_price");
    await wrapper.get('[data-testid="lookup-rule-constant-datetime-0"]').setValue("2026-01-02T03:04");
    await commit(wrapper);

    const saved = (wrapper.emitted("commit")![0]![0] as LookupValue)
      .condition?.rules[0]?.operand?.value as string;
    // Go/PB 只接受带 zone 的布局：必须是 RFC3339 UTC 且与本地输入两同一时点。
    expect(saved).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(new Date(saved).getTime()).toBe(new Date("2026-01-02T03:04").getTime());

    const reopened = mountEditor({
      path: [],
      targetFieldId: "fld_price",
      condition: {
        sourceTableId: "tbl_orders",
        match: "all",
        distinct: false,
        rules: [
          { sourceFieldId: "fld_updated_at", operator: "eq",
            operand: { kind: "constant", value: saved } },
        ],
      },
    });
    await openEditor(reopened);
    const input = reopened.get('[data-testid="lookup-rule-constant-datetime-0"]');
    expect((input.element as HTMLInputElement).value).toBe("2026-01-02T03:04");
    // 未改动即可再次提交，时点仍不变。
    await commit(reopened);
    const resubmitted = (reopened.emitted("commit")![0]![0] as LookupValue)
      .condition?.rules[0]?.operand?.value as string;
    expect(new Date(resubmitted).getTime()).toBe(new Date(saved).getTime());
  });

  it("date 常量只接受真实日历日，不做宽松解析", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-condition-source-table", "tbl_orders");
    await setSelect(wrapper, "lookup-rule-source-field-0", "fld_order_date");
    await setSelect(wrapper, "lookup-rule-operand-kind-0", "constant");
    await setSelect(wrapper, "lookup-target-field", "fld_price");
    const input = () => wrapper.get('[data-testid="lookup-rule-constant-date-0"]');

    await input().setValue("2026-1-2");
    expect(commitDisabled(wrapper)).toBe(true);
    await input().setValue("2026-02-30");
    expect(commitDisabled(wrapper)).toBe(true);
    await input().setValue("2026-02-29");
    expect(commitDisabled(wrapper)).toBe(true);
    await input().setValue("2024-02-29");
    expect(commitDisabled(wrapper)).toBe(false);

    await commit(wrapper);
    const payload = wrapper.emitted("commit")![0]![0] as LookupValue;
    expect(payload.condition?.rules[0]?.operand).toEqual({ kind: "constant", value: "2024-02-29" });
  });

  it("模式切换刷新目录事件，取消时无论是否改过都恢复原模式目录", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);

    // 条件 → 路径：发 pathChange 旧 path（条件值的 path 为空数组）。
    await setSelect(wrapper, "lookup-mode", "path");
    expect(wrapper.emitted("pathChange")?.at(-1)).toEqual([[]]);
    // 路径 → 条件：发 sourceTableChange 当前条件源（保留的条件侧）。
    await setSelect(wrapper, "lookup-mode", "condition");
    expect(wrapper.emitted("sourceTableChange")?.at(-1)).toEqual(["tbl_orders"]);

    // 从路径模式切到空白条件：sourceTableChange 为空串，父层清空条件目录。
    const pathWrapper = mountEditor(pathValue);
    await openEditor(pathWrapper);
    await setSelect(pathWrapper, "lookup-mode", "condition");
    expect(pathWrapper.emitted("sourceTableChange")?.at(-1)).toEqual([""]);

    // 取消：原为路径模式 → 恢复原 path；草稿置 null。
    await pathWrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(pathWrapper.emitted("pathChange")?.at(-1))
      .toEqual([[{ relationFieldId: "fld_customer" }]]);
    expect(lastDraft(pathWrapper)).toBeNull();

    // 取消：原为条件模式 → 恢复原来源表。
    await wrapper.get('[data-testid="lookup-editor-cancel"]').trigger("click");
    expect(wrapper.emitted("sourceTableChange")?.at(-1)).toEqual(["tbl_orders"]);
    expect(lastDraft(wrapper)).toBeNull();
  });

  it("进入编辑即发保存的条件草稿；目录缺失时发 null", async () => {
    const wrapper = mountEditor(conditionValue);
    await openEditor(wrapper);
    expect(wrapper.emitted("draftChange")![0]).toEqual([conditionValue]);

    // 未接线目录（集成前场景）：规则无法解析，只能发 null。
    const unwired = mount(LookupFieldEditor, {
      props: {
        value: conditionValue,
        relationOptions,
        targetFieldOptions,
        maxDepth: 8,
      },
    });
    mounted.push(unwired);
    await openEditor(unwired);
    expect(lastDraft(unwired)).toBeNull();
  });

  it("汇总选项按目标类型禁用；改目标类型不静默保留错误聚合", async () => {
    const wrapper = mountEditor(pathValue);
    await openEditor(wrapper);
    const aggregationOptions = () => componentProp(
      findSelect(wrapper, "lookup-aggregation"),
      "options",
    ) as Array<{ value: string; disabled?: boolean }>;

    // 文本目标：计数全类型可用，数值汇总明确禁用。
    expect(aggregationOptions().find(option => option.value === "countRecords")?.disabled)
      .toBeFalsy();
    expect(aggregationOptions().find(option => option.value === "sum")?.disabled).toBe(true);

    // 数字目标：SUM 可选，草稿携带顶层 aggregation。
    await setSelect(wrapper, "lookup-target-field", "fld_balance");
    expect(aggregationOptions().find(option => option.value === "sum")?.disabled).toBe(false);
    await setSelect(wrapper, "lookup-aggregation", "sum");
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_balance",
      aggregation: "sum",
    });

    // 切回文本目标：sum 复位为原值，草稿回到旧形状（不带 aggregation）。
    await setSelect(wrapper, "lookup-target-field", "fld_name");
    expect(componentProp(findSelect(wrapper, "lookup-aggregation"), "value")).toBe("values");
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_name",
    });

    // 禁用选项即便被触发也不被接受。
    findSelect(wrapper, "lookup-aggregation").vm.$emit("update:value", "average");
    await nextTick();
    expect(componentProp(findSelect(wrapper, "lookup-aggregation"), "value")).toBe("values");
  });

  it("旧 condition.distinct=true 读回为去重，保存迁移顶层 aggregation 并清旧 distinct", async () => {
    const legacy: LookupValue = {
      path: [],
      targetFieldId: "fld_price",
      condition: {
        sourceTableId: "tbl_orders",
        match: "all",
        distinct: true,
        rules: conditionValue.condition!.rules,
      },
    };
    const wrapper = mountEditor(legacy);
    await openEditor(wrapper);
    expect(componentProp(findSelect(wrapper, "lookup-aggregation"), "value")).toBe("distinct");
    const canonical = {
      path: [],
      targetFieldId: "fld_price",
      aggregation: "distinct",
      condition: { ...legacy.condition!, distinct: false },
    };
    // 进入编辑即发出的预览草稿与提交都保存 canonical 形状。
    expect(wrapper.emitted("draftChange")![0]).toEqual([canonical]);
    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[canonical]]);
  });

  it("计数适用于全部类型；路径模式渲染权威样例预览与空值规则提示", async () => {
    const wrapper = mountEditor(pathValue, { previewReady: true, previewValue: 40 });
    await openEditor(wrapper);
    await setSelect(wrapper, "lookup-aggregation", "countNonEmpty");
    expect(lastDraft(wrapper)).toEqual({
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_name",
      aggregation: "countNonEmpty",
    });
    expect(wrapper.get('[data-testid="lookup-aggregation-hint"]').text())
      .toContain("空值与空串不计入");
    // 路径模式同样渲染样例区：汇总结果是数字，来自权威 preview props。
    expect(wrapper.get('[data-testid="lookup-preview-value"]').text()).toContain("40");
    expect(wrapper.get('[data-testid="lookup-preview-value"]').text()).toContain("样例汇总结果");
  });

  it("保存的聚合在重新打开编辑器时保持并可再次提交", async () => {
    const saved: LookupValue = {
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_balance",
      aggregation: "average",
    };
    const wrapper = mountEditor(saved);
    expect(wrapper.text()).toContain("AVERAGE");
    await openEditor(wrapper);
    expect(componentProp(findSelect(wrapper, "lookup-aggregation"), "value")).toBe("average");
    await commit(wrapper);
    expect(wrapper.emitted("commit")).toEqual([[saved]]);
  });

  it("已保存的数值聚合与当前目标类型不匹配时阻止确认", async () => {
    const drift: LookupValue = {
      path: [{ relationFieldId: "fld_customer" }],
      // 目标已变为文本，保存的 sum 不再适用；不得静默接受或提交。
      targetFieldId: "fld_name",
      aggregation: "sum",
    };
    const wrapper = mountEditor(drift);
    await openEditor(wrapper);
    expect(commitDisabled(wrapper)).toBe(true);
    await setSelect(wrapper, "lookup-target-field", "fld_balance");
    expect(commitDisabled(wrapper)).toBe(false);
    await commit(wrapper);
    expect(wrapper.emitted("commit")![0]).toEqual([{
      path: [{ relationFieldId: "fld_customer" }],
      targetFieldId: "fld_balance",
      aggregation: "sum",
    }]);
  });
});
