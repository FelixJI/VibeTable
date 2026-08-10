import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ImportPreviewPanel from "./ImportPreviewPanel.vue";
import { setLocale } from "@/i18n";
import type { ImportPreviewSession } from "@/services/dataIoService";

function session(options: { errors?: number; warnings?: number } = {}): ImportPreviewSession {
  const errors = options.errors ?? 0;
  const warnings = options.warnings ?? 0;
  return {
    grant: {
      grantId: "grant-1",
      purpose: "import_source",
      direction: "read",
      displayName: "orders.xlsx",
      sizeBytes: 2048,
      mimeType: null,
      expiresAt: 9999999999,
    },
    mode: "create_only",
    plan: {
      collection: "orders",
      schemaRevision: "schema-1",
      capabilityHash: "cap-1",
      sourceHash: "hash-1",
      summary: {
        totalRows: 2,
        validRows: errors ? 1 : 2,
        errorRows: errors ? 1 : 0,
        warningRows: warnings ? 1 : 0,
        errorCount: errors,
        warningCount: warnings,
      },
      rows: [
        {
          sourceRow: 2,
          values: { number: "A-1", amount: 12.5 },
          diagnostics: errors ? [{
            sheet: "Sheet1",
            row: 2,
            column: 2,
            severity: "error",
            code: "field.value.invalid",
            message: "金额格式无效",
            originalValue: "oops",
          }] : [],
          relationResolutions: [],
        },
      ],
      unmatchedColumns: ["Legacy note"],
      diagnostics: [],
      token: { token: "token-1", expiresAt: 9999999999, consumed: false },
    },
  };
}

describe("ImportPreviewPanel", () => {
  beforeEach(() => setLocale("zh-CN"));

  it("shows the atomic write scope, unmatched columns, diagnostics, and normalized samples", () => {
    const wrapper = mount(ImportPreviewPanel, {
      props: {
        session: session({ errors: 1 }), applying: false, cancellable: false,
        cancelling: false, error: null,
      },
    });

    expect(wrapper.text()).toContain("orders.xlsx");
    expect(wrapper.text()).toContain("2");
    expect(wrapper.text()).toContain("Legacy note");
    expect(wrapper.text()).toContain("Sheet1 · 第 2 行 · 第 2 列");
    expect(wrapper.text()).toContain("金额格式无效");
    expect(wrapper.text()).toContain("A-1");
    expect(wrapper.get('[data-testid="import-confirm"]').attributes("disabled")).toBeDefined();
  });

  it("requires acknowledgement for warnings or ignored columns before emitting confirm", async () => {
    const wrapper = mount(ImportPreviewPanel, {
      props: {
        session: session({ warnings: 1 }), applying: false, cancellable: false,
        cancelling: false, error: null,
      },
    });
    const confirm = wrapper.get('[data-testid="import-confirm"]');
    expect(confirm.attributes("disabled")).toBeDefined();

    await wrapper.get('[data-testid="import-ack"]').trigger("click");
    expect(wrapper.get('[data-testid="import-confirm"]').attributes("disabled")).toBeUndefined();
    await wrapper.get('[data-testid="import-confirm"]').trigger("click");
    expect(wrapper.emitted("confirm")).toHaveLength(1);
  });

  it("offers task cancellation but keeps preview dismissal locked while applying", async () => {
    const wrapper = mount(ImportPreviewPanel, {
      props: {
        session: session(), applying: true, cancellable: true, cancelling: false, error: null,
      },
    });
    const cancel = wrapper.get('[data-testid="import-cancel"]');
    expect(cancel.attributes("disabled")).toBeUndefined();
    expect(cancel.text()).toBe("取消导入任务");
    await cancel.trigger("click");
    expect(wrapper.emitted("cancelTask")).toHaveLength(1);
    expect(wrapper.get('[data-testid="import-confirm"]').attributes("disabled")).toBeDefined();
  });

  it("waits for the task id before enabling cancellation", () => {
    const wrapper = mount(ImportPreviewPanel, {
      props: {
        session: session(), applying: true, cancellable: false,
        cancelling: false, error: null,
      },
    });
    expect(wrapper.get('[data-testid="import-cancel"]').attributes("disabled")).toBeDefined();
  });
});
