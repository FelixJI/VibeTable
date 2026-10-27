import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import ImportPreviewPanel from "./ImportPreviewPanel.vue";
import { setLocale } from "@/i18n";
import type { ImportPreviewSession, RelationImportOption } from "@/services/dataIoService";

/**
 * ImportPreviewPanel renders an NModal preset card that teleports to
 * document.body. We mount with attachTo and query document.body so the real
 * modal shell (mask, focus trap, escape gating) is exercised.
 */
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
      sourceColumns: ["number", "amount", "Partner Code", "Legacy note"],
      unmatchedColumns: ["Legacy note"],
      diagnostics: [],
      token: { token: "token-1", expiresAt: 9999999999, consumed: false },
    },
  };
}

const relationOptions: readonly RelationImportOption[] = [{
  targetField: "partner",
  relationId: "orders.fld_partner",
  targetCollection: "partners",
  targetDisplayName: "合作方",
  sourceDisplayName: "合作方",
  matchFields: [
    { fieldId: "fld_code", displayName: "编码" },
    { fieldId: "fld_tax", displayName: "税号" },
  ],
}];

function baseProps(overrides: Record<string, unknown> = {}) {
  return {
    session: session(),
    applying: false,
    cancellable: false,
    cancelling: false,
    error: null,
    relationOptions,
    relationOptionsLoading: false,
    relationOptionsError: null,
    relationConfig: [],
    mappingDirty: false,
    schemaDrifted: false,
    repreviewing: false,
    ...overrides,
  };
}

type PanelProps = InstanceType<typeof ImportPreviewPanel>["$props"];

function mountPanel(props: PanelProps) {
  return mount(ImportPreviewPanel, { props, attachTo: document.body });
}

function bodyEl(testId: string): DOMWrapper<Element> {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  if (!el) throw new Error(`missing ${testId}`);
  return new DOMWrapper(el);
}

function bodyFind(testId: string): DOMWrapper<Element> | null {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  return el ? new DOMWrapper(el) : null;
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

async function escapePress(): Promise<void> {
  // Real key events originate at the focused element and bubble to window,
  // where evtd's unified listener and the modal focus trap receive them.
  const target = document.activeElement instanceof HTMLElement ? document.activeElement : document.body;
  target.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true }));
  await flushPromises();
}

describe("ImportPreviewPanel", () => {
  beforeEach(() => setLocale("zh-CN"));
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("shows the atomic write scope, unmatched columns, diagnostics, and normalized samples", async () => {
    await mountPanel(baseProps({ session: session({ errors: 1 }) }));
    await flushPromises();

    expect(bodyText()).toContain("orders.xlsx");
    expect(bodyText()).toContain("2");
    expect(bodyText()).toContain("Legacy note");
    expect(bodyText()).toContain("Sheet1 · 第 2 行 · 第 2 列");
    expect(bodyText()).toContain("金额格式无效");
    expect(bodyText()).toContain("A-1");
    expect(bodyEl("import-confirm").attributes("disabled")).toBeDefined();
    expect(document.body.querySelector(".n-modal-mask")).toBeTruthy();
  });

  it("requires acknowledgement for warnings or ignored columns before emitting confirm", async () => {
    const wrapper = await mountPanel(baseProps({ session: session({ warnings: 1 }) }));
    await flushPromises();
    const confirm = bodyEl("import-confirm");
    expect(confirm.attributes("disabled")).toBeDefined();

    await bodyEl("import-ack").trigger("click");
    await flushPromises();
    expect(bodyEl("import-confirm").attributes("disabled")).toBeUndefined();
    await bodyEl("import-confirm").trigger("click");
    expect(wrapper.emitted("confirm")).toHaveLength(1);
  });

  it("offers task cancellation but keeps preview dismissal locked while applying", async () => {
    const wrapper = await mountPanel(baseProps({ applying: true, cancellable: true }));
    await flushPromises();
    const cancel = bodyEl("import-cancel");
    expect(cancel.attributes("disabled")).toBeUndefined();
    expect(cancel.text()).toBe("取消导入任务");
    await cancel.trigger("click");
    expect(wrapper.emitted("cancelTask")).toHaveLength(1);
    expect(bodyEl("import-confirm").attributes("disabled")).toBeDefined();
  });

  it("waits for the task id before enabling cancellation", async () => {
    await mountPanel(baseProps({ applying: true, cancellable: false }));
    await flushPromises();
    expect(bodyEl("import-cancel").attributes("disabled")).toBeDefined();
  });

  it("hides the background view-task entry until a task is admitted", async () => {
    // Preview stage: no task exists yet (and no explicit admission prop).
    const preview = await mountPanel(baseProps());
    await flushPromises();
    expect(bodyFind("import-view-task")).toBeNull();
    preview.unmount();

    // Applying but the create receipt (task id) has not arrived yet.
    const unadmitted = await mountPanel(baseProps({ applying: true, cancellable: false }));
    await flushPromises();
    expect(bodyFind("import-view-task")).toBeNull();
    unadmitted.unmount();

    // Preview stage with a stray admission flag must not show it either.
    const staleAdmission = await mountPanel(baseProps({ taskAdmitted: true }));
    await flushPromises();
    expect(bodyFind("import-view-task")).toBeNull();
    staleAdmission.unmount();
  });

  it("emits openImportManagement from the admitted applying task only", async () => {
    const wrapper = await mountPanel(baseProps({ applying: true, cancellable: true, taskAdmitted: true }));
    await flushPromises();

    const viewTask = bodyEl("import-view-task");
    expect(viewTask.text()).toBe("查看导入任务");
    await viewTask.trigger("click");
    expect(wrapper.emitted("openImportManagement")).toHaveLength(1);

    // Cancellation stays tracked and available on the same admitted task.
    const cancel = bodyEl("import-cancel");
    expect(cancel.attributes("disabled")).toBeUndefined();
    expect(cancel.text()).toBe("取消导入任务");
  });

  it("disables confirm until a changed mapping is re-previewed", async () => {
    const wrapper = await mountPanel(baseProps({
      relationConfig: [{ sourceColumn: "Partner Code", relationId: "orders.fld_partner", matchField: "fld_code" }],
      mappingDirty: true,
    }));
    await flushPromises();

    expect(bodyFind("import-mapping-stale")).not.toBeNull();
    expect(bodyEl("import-confirm").attributes("disabled")).toBeDefined();
    const repreview = bodyEl("import-repreview");
    expect(repreview.attributes("disabled")).toBeUndefined();

    await repreview.trigger("click");
    expect(wrapper.emitted("repreview")).toHaveLength(1);
    expect(wrapper.emitted("confirm")).toBeUndefined();
  });

  it("requires re-preview after a schema drift and surfaces the catalog empty state", async () => {
    const wrapper = await mountPanel(baseProps({
      relationOptions: [],
      schemaDrifted: true,
    }));
    await flushPromises();

    expect(bodyFind("import-schema-drifted")).not.toBeNull();
    expect(bodyFind("relation-mapping-empty")).not.toBeNull();
    expect(bodyEl("import-confirm").attributes("disabled")).toBeDefined();
    await bodyEl("import-repreview").trigger("click");
    expect(wrapper.emitted("repreview")).toHaveLength(1);
  });

  it("forwards mapping edits and blocks re-preview while the catalog is loading", async () => {
    await mountPanel(baseProps({ relationOptionsLoading: true, relationOptions: null }));
    await flushPromises();

    expect(bodyFind("relation-mapping-loading")).not.toBeNull();
    expect(bodyEl("import-repreview").attributes("disabled")).toBeDefined();
    expect(bodyEl("import-confirm").attributes("disabled")).toBeDefined();
  });

  it("only lets Escape leave an idle preview, never a busy one", async () => {
    const idle = await mountPanel(baseProps());
    await flushPromises();
    await escapePress();
    expect(idle.emitted("cancel")).toHaveLength(1);
    idle.unmount();

    const applying = await mountPanel(baseProps({ applying: true, cancellable: true }));
    await flushPromises();
    await escapePress();
    expect(applying.emitted("cancel")).toBeUndefined();
    expect(applying.emitted("cancelTask")).toBeUndefined();
    applying.unmount();

    const repreviewing = await mountPanel(baseProps({ repreviewing: true }));
    await flushPromises();
    await escapePress();
    expect(repreviewing.emitted("cancel")).toBeUndefined();
    repreviewing.unmount();
  });

  it("stays mounted but hidden when the host hides it for another view", async () => {
    const wrapper = await mountPanel(baseProps());
    await flushPromises();
    const panel = document.body.querySelector<HTMLElement>("[data-testid=\"import-preview-panel\"]")!;
    expect(panel.style.display).not.toBe("none");

    await wrapper.setProps({ visible: false });
    await flushPromises();
    // Same element, same acknowledgement state — only the shell hides it.
    expect(document.body.querySelector('[data-testid="import-preview-panel"]')).toBe(panel);
    expect(panel.style.display).toBe("none");
    expect(document.body.querySelector(".n-modal-mask")).toBeNull();
  });
});
