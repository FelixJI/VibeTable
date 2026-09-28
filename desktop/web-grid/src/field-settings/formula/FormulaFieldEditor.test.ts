import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NSelect } from "naive-ui";
import type { FormulaFunctionInfo } from "@/contracts";
import type { FormulaAuthorDocument } from "@/contracts/generated/workbench";
import FormulaFieldEditor from "./FormulaFieldEditor.vue";

const mounted: VueWrapper[] = [];

const FUNCTIONS: readonly FormulaFunctionInfo[] = [
  {
    name: "IF",
    category: "逻辑",
    signature: "IF(bool, T, T)",
    description: "按条件返回两个分支之一，未计算分支。",
    example: "IF({单价} > 0, {数量}, 0)",
  },
  {
    name: "CONCATENATE",
    category: "文本",
    signature: "CONCATENATE(any, any, ...)",
    description: "连接多个文本。",
    example: 'CONCATENATE({名字}, "-后缀")',
  },
  {
    name: "ROUND",
    category: "数值",
    signature: "ROUND(number, int)",
    description: "四舍五入到指定小数位。",
    example: "ROUND({单价} * {数量}, 2)",
  },
];

function restoredDocument() {
  return {
    displaySource: "{单价} * 2",
    documentRevision: 6,
    tokens: [{
      range: { start: { line: 0, character: 0 }, end: { line: 0, character: 4 } },
      kind: "field" as const,
      fieldId: "fld_price",
      relationFieldId: null,
      targetFieldId: null,
    }],
  };
}

function mountEditor(overrides: Record<string, unknown> = {}): VueWrapper {
  const wrapper = mount(FormulaFieldEditor, {
    props: {
      value: {
        language: "cel-v1",
        source: "f_price * 2",
      },
      resultType: "number",
      authorDocument: restoredDocument(),
      functions: FUNCTIONS,
      localFields: [
        { label: "单价", fieldId: "fld_price", dataType: "number" },
        { label: "备注", fieldId: "fld_note", dataType: "text" },
      ],
      relations: [{
        label: "明细",
        fieldId: "fld_lines",
        many: true,
        targetFields: [
          { label: "金额", fieldId: "fld_amount", dataType: "decimal" },
          { label: "说明", fieldId: "fld_description", dataType: "text" },
        ],
      }, {
        label: "客户",
        fieldId: "fld_customer",
        many: false,
        targetFields: [
          { label: "信用额度", fieldId: "fld_credit", dataType: "decimal" },
        ],
      }],
      ...overrides,
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function sourceTextarea(wrapper: VueWrapper): HTMLTextAreaElement {
  return wrapper.get('[data-testid="formula-source"]').find("textarea")
    .element as HTMLTextAreaElement;
}

function documentRequests(wrapper: VueWrapper): {
  displaySource: string;
  authorDocument: FormulaAuthorDocument;
}[] {
  return (wrapper.emitted("validate") ?? [])
    .map(event => event[0] as { kind?: unknown; displaySource?: unknown; authorDocument?: unknown })
    .filter((request): request is {
      kind: "document";
      displaySource: string;
      authorDocument: FormulaAuthorDocument;
    } => request.kind === "document");
}

async function enterEditing(wrapper: VueWrapper): Promise<HTMLTextAreaElement> {
  await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
  return sourceTextarea(wrapper);
}

afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount());
});

describe("FormulaFieldEditor restore and summary", () => {
  it("shows the restored display text, never the persisted physical source", () => {
    const wrapper = mountEditor();
    expect(wrapper.get('[data-testid="formula-summary-source"]').text())
      .toContain("{单价} * 2");
    expect(wrapper.text()).not.toContain("f_price");
  });

  it("asks Go to restore a persisted source when no document is available", async () => {
    const wrapper = mountEditor({ authorDocument: null });
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    expect(wrapper.emitted("validate")?.at(-1)).toEqual([{
      kind: "restore",
      displaySource: "f_price * 2",
      adoptDocument: true,
    }]);

    await wrapper.setProps({ authorDocument: restoredDocument() });
    expect(sourceTextarea(wrapper).value).toBe("{单价} * 2");
    expect(wrapper.find('[data-testid="formula-restore-loading"]').exists()).toBe(false);
  });

  it("cancel discards the working draft without emitting commit", async () => {
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    await wrapper.get('[data-testid="formula-editor-cancel"]').trigger("click");

    expect(wrapper.emitted("validate")?.slice(-2)).toEqual([
      [{ kind: "invalidate", discardDocument: true }],
      [{ kind: "restore", displaySource: "f_price * 2", adoptDocument: true }],
    ]);
    expect(wrapper.emitted("commit")).toBeUndefined();
    expect(wrapper.text()).toContain("{单价} * 2");
    expect(textarea.value).toBe("{单价} * 2");
  });
});

describe("FormulaFieldEditor editing lifecycle", () => {
  it("invalidates immediately, then sends the adjusted document after the debounce", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    await enterEditing(wrapper);
    const textarea = sourceTextarea(wrapper);
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 3");

    // The first keystroke must kill the old validation at once.
    expect(wrapper.emitted("validate")?.at(-1)).toEqual([{ kind: "invalidate" }]);

    await vi.advanceTimersByTimeAsync(249);
    expect(documentRequests(wrapper)).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    const requests = documentRequests(wrapper);
    expect(requests.at(-1)).toEqual({
      kind: "document",
      displaySource: "{单价} * 3",
      authorDocument: {
        displaySource: "{单价} * 3",
        documentRevision: 7,
        tokens: [{
          range: { start: { line: 0, character: 0 }, end: { line: 0, character: 4 } },
          kind: "field",
          fieldId: "fld_price",
          relationFieldId: null,
          targetFieldId: null,
        }],
      },
    });
    expect(textarea.value).toBe("{单价} * 3");
    vi.useRealTimers();
  });

  it("commits the sidecar canonical source only after a matching validation", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    await enterEditing(wrapper);
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 3");
    await vi.advanceTimersByTimeAsync(250);
    await wrapper.setProps({
      validating: true,
    });
    await wrapper.setProps({
      validating: false,
      validatedSource: "{单价} * 3",
      validatedDocumentRevision: 7,
      validation: {
        canonicalSource: "f_price * 3",
        resultType: "number",
        dependencies: ["f_price"],
        relationAggregatePaths: [],
        authorDocument: {
          displaySource: "{单价} * 3",
          documentRevision: 7,
          tokens: [],
        },
      },
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="formula-editor-commit"]').attributes("disabled"))
      .toBeUndefined();
    await wrapper.get('[data-testid="formula-editor-commit"]').trigger("click");

    expect(wrapper.emitted("commit")).toEqual([[
      { language: "cel-v1", source: "f_price * 3" },
    ]]);
    expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);

    // A stale revision must not enable commit again.
    await wrapper.get('[data-testid="formula-editor-entry"]').trigger("click");
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 4");
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="formula-editor-commit"]').attributes("disabled"))
      .toBeDefined();
    vi.useRealTimers();
  });

  it("stops the debounce timer on unmount", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    await enterEditing(wrapper);
    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 3");
    wrapper.unmount();
    mounted.splice(0);
    await vi.advanceTimersByTimeAsync(250);
    expect(documentRequests(wrapper)).toHaveLength(0);
    vi.useRealTimers();
  });

  it("renders the UTF-16 diagnostic range and selects it on demand", async () => {
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    await wrapper.setProps({
      error: "公式语法错误",
      diagnostic: {
        message: "公式语法错误",
        code: "formula.syntax",
        range: {
          start: { line: 0, character: 7 },
          end: { line: 0, character: 8 },
        },
      },
    });
    const rangeButton = wrapper.get('[data-testid="formula-error-range"]');
    expect(rangeButton.text()).toContain("第1行 第8列");
    await rangeButton.trigger("click");
    expect(textarea.selectionStart).toBe(7);
    expect(textarea.selectionEnd).toBe(8);
  });
});

describe("FormulaFieldEditor cursor insertion", () => {
  it("replaces the selection when inserting a local field with a stable token", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    textarea.setSelectionRange(8, 9); // past-end selection clamps to append
    await wrapper.vm.$nextTick();
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-local-field")
      ?.vm.$emit("update:value", "fld_note");
    await vi.advanceTimersByTimeAsync(250);

    expect(textarea.value).toBe("{单价} * 2{备注}");
    const requests = documentRequests(wrapper);
    const document = requests.at(-1)?.authorDocument;
    expect(document?.tokens).toHaveLength(2);
    expect(document?.tokens.map(token => token.fieldId)).toEqual(["fld_price", "fld_note"]);
    expect(document?.tokens[1]?.range).toEqual({
      start: { line: 0, character: 8 },
      end: { line: 0, character: 12 },
    });
    expect(textarea.selectionStart).toBe(12);
    expect(textarea.selectionEnd).toBe(12);
    vi.useRealTimers();
  });

  it("inserts relation aggregates as one relationTarget token at the cursor", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    textarea.setSelectionRange(8, 8);
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-relation-field")
      ?.vm.$emit("update:value", "fld_lines");
    await wrapper.vm.$nextTick();
    selects.find(select => select.attributes("data-testid") === "formula-target-field")
      ?.vm.$emit("update:value", "fld_amount");
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find(button => button.text().includes("插入聚合"))
      ?.trigger("click");
    await vi.advanceTimersByTimeAsync(250);

    expect(textarea.value).toBe("{单价} * 2SUM({明细}.{金额})");
    const requests = documentRequests(wrapper);
    const document = requests.at(-1)?.authorDocument;
    expect(document?.tokens.map(token => [token.kind, token.fieldId])).toEqual([
      ["field", "fld_price"],
      ["relationTarget", "fld_amount"],
    ]);
    expect(document?.tokens[1]?.relationFieldId).toBe("fld_lines");
    expect(document?.tokens[1]?.targetFieldId).toBe("fld_amount");
    expect(document?.tokens[1]?.range).toEqual({
      start: { line: 0, character: 12 },
      end: { line: 0, character: 21 },
    });
  });

  it("inserts a direct relation reference with a stable path token", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    textarea.setSelectionRange(8, 8);
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-direct-relation")
      ?.vm.$emit("update:value", "fld_customer");
    await wrapper.vm.$nextTick();
    selects.find(select => select.attributes("data-testid") === "formula-direct-target")
      ?.vm.$emit("update:value", "fld_credit");
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find(button => button.text().includes("插入引用"))
      ?.trigger("click");
    await vi.advanceTimersByTimeAsync(250);

    expect(textarea.value).toBe("{单价} * 2{客户}.{信用额度}");
    const requests = documentRequests(wrapper);
    const document = requests.at(-1)?.authorDocument;
    expect(document?.tokens.map(token => token.kind)).toEqual(["field", "relationTarget"]);
  });

  it("searches the offline catalog and inserts a runnable example at the cursor", async () => {
    vi.useFakeTimers();
    const wrapper = mountEditor();
    const textarea = await enterEditing(wrapper);
    await wrapper.get('[data-testid="formula-function-search"]').find("input")
      .setValue("round");
    const options = wrapper.findAll('[data-testid="formula-function-option"]');
    expect(options).toHaveLength(1);
    await options[0]!.trigger("click");
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="formula-function-detail"]').text())
      .toContain("四舍五入");

    // The call insertion lands at the caret; replace it with the full example.
    const insertedAt = textarea.value.indexOf("ROUND(");
    expect(insertedAt).toBeGreaterThan(0);
    textarea.setSelectionRange(insertedAt, insertedAt + "ROUND(".length);
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="formula-function-insert-example"]').trigger("click");
    await vi.advanceTimersByTimeAsync(250);
    expect(textarea.value).toBe("{单价} * 2ROUND({单价} * {数量}, 2)");
    // Example insertions never fabricate tokens; existing ones survive.
    const requests = documentRequests(wrapper);
    const document = requests.at(-1)?.authorDocument;
    expect(document?.tokens.map(token => token.fieldId)).toEqual(["fld_price"]);
    vi.useRealTimers();
  });

  it("filters the catalog by category as well", async () => {
    const wrapper = mountEditor();
    await enterEditing(wrapper);
    const selects = wrapper.findAllComponents(NSelect);
    selects.find(select => select.attributes("data-testid") === "formula-function-category")
      ?.vm.$emit("update:value", "文本");
    await wrapper.vm.$nextTick();
    const options = wrapper.findAll('[data-testid="formula-function-option"]');
    expect(options.map(option => option.text())).toHaveLength(1);
    expect(options[0]!.text()).toContain("CONCATENATE");
  });
});describe("FormulaFieldEditor preview gating", () => {
  it("hides stale previews as soon as the working source changes", async () => {
    const wrapper = mountEditor();
    await enterEditing(wrapper);
    await wrapper.setProps({
      validatedSource: "{单价} * 2",
      previewReady: true,
      previewValue: 42.5,
    });
    expect(wrapper.get('[data-testid="formula-preview-value"]').text()).toContain("42.5");

    await wrapper.get('[data-testid="formula-source"]').find("textarea")
      .setValue("{单价} * 3");
    expect(wrapper.find('[data-testid="formula-preview-value"]').exists()).toBe(false);
  });
});
