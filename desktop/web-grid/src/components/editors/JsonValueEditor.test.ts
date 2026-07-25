import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import JsonValueEditor from "./JsonValueEditor.vue";

describe("JsonValueEditor", () => {
  it("pretty prints the current value without changing its JSON shape", () => {
    const wrapper = mount(JsonValueEditor, {
      props: { modelValue: { nested: [1, true, null] } },
    });
    expect(wrapper.get("textarea").element.value).toContain('"nested": [');
  });

  it("keeps invalid text visible and points to the JSON field error", async () => {
    const wrapper = mount(JsonValueEditor, { props: { modelValue: {} } });
    await wrapper.get("textarea").setValue('{"broken":');
    expect(wrapper.get('[data-testid="json-editor-error"]').text()).toContain("JSON");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    expect(wrapper.emitted("validityChanged")?.at(-1)).toEqual([false]);
  });

  it("emits the parsed value rather than a JSON string", async () => {
    const wrapper = mount(JsonValueEditor, { props: { modelValue: null } });
    await wrapper.get("textarea").setValue('{"source":"import","count":2}');
    expect(wrapper.emitted("update:modelValue")?.at(-1)?.[0]).toEqual({
      source: "import",
      count: 2,
    });
    expect(wrapper.emitted("validityChanged")?.at(-1)).toEqual([true]);
  });

  it("renders a server field-path error next to the editor", () => {
    const wrapper = mount(JsonValueEditor, {
      props: {
        modelValue: {},
        serverError: "不符合 JSON Schema",
        errorPath: "fields[2].constraints.jsonSchema",
      },
    });
    expect(wrapper.get('[data-testid="json-editor-server-error"]').text())
      .toContain("fields[2].constraints.jsonSchema");
  });
});
