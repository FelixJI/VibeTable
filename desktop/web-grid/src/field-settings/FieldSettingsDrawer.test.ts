import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import FieldSettingsDrawer from "./FieldSettingsDrawer.vue";
import { useFieldSettingsStore } from "./store";
import FormulaFieldEditor from "./formula/FormulaFieldEditor.vue";
import LookupFieldEditor from "./lookup/LookupFieldEditor.vue";
import type {
  CapabilityV2,
  FieldChangePlanV2,
  FieldDefinitionV2,
  FieldMigrationStatusV2,
  FieldSettingsDescribeResultV2,
  JsonValueV2,
  LogicalTypeV2,
} from "@/contracts";

const mounted: VueWrapper[] = [];
const capabilityFixturePath = resolve(
  import.meta.dirname,
  "../../../../contracts/schema-v2/fixtures/capability.json",
);

function definition(type: LogicalTypeV2 = "number"): FieldDefinitionV2 {
  const field = {
    contract: "vibetable.schema.v2",
    identity: { fieldId: "fld_amount", physicalName: "f_amount", providerFieldId: "pb_amount" },
    displayName: "金额",
    help: "用于测试",
    logicalType: type,
    lifecycle: { state: "active", retiredAt: null },
    value: {
      required: false,
      default: { enabled: false, value: null, source: "recommended", defaultsVersion: 1 },
      presence: { mode: "companion", providerFieldId: "pb_presence", physicalName: "__vt_has_f_amount" },
    },
    constraints: {
      unique: { enabled: false, blankPolicy: "ignoreMissing" },
      range: { min: null, max: null }, length: { min: null, max: null },
      pattern: { enabled: false, value: "" }, domains: { only: ["example.com"], except: ["blocked.example"] },
      selection: { min: 0, max: null },
    },
    storage: { kind: "pocketbase-number", options: { onlyInt: false, maxSize: 2048, convertURLs: true, presentable: false } },
    display: {
      kind: type === "formula" || type === "lookup" || type === "autoDate" ? "readonly" : type,
      preset: "plain", displayScale: 2, scaleMode: "max", trimTrailingZeros: true,
      useGrouping: true, currency: "CNY", percentStorage: "ratio", unit: null,
      precision: "minute", timezone: "UTC", mode: "default", trueLabel: "是", falseLabel: "否",
    },
  } as Record<string, unknown>;
  if (type === "select" || type === "multiSelect") field.select = { options: [{ optionId: "opt_a", label: "选项 A", color: "#0ea5e9", order: 0, state: "active" }] };
  if (type === "relation") field.relation = { targetTableId: "tbl_customers", cardinality: "many", deletePolicy: "cascade", displayFieldId: "fld_name" };
  if (type === "file") field.file = { maxFiles: 3, maxBytesPerFile: 4096, allowedMimeTypes: ["image/png"], thumbs: ["small"], protected: true };
  if (type === "json") field.json = { rootType: "object", maxSize: 8192, schema: {} };
  if (type === "autoDate") field.autoDate = { role: "createdAt" };
  if (type === "formula") field.formula = { language: "cel-v1", source: "record.price * 2", resultType: "number" };
  if (type === "lookup") field.lookup = {
    path: [{ relationFieldId: "fld_customer" }], targetFieldId: "fld_name",
  };
  return field as unknown as FieldDefinitionV2;
}

function capability(type: LogicalTypeV2): CapabilityV2 {
  const field = definition(type);
  return {
    ...(JSON.parse(readFileSync(capabilityFixturePath, "utf8")) as CapabilityV2),
    logicalType: type,
    generalSettings: ["displayName", "required", "default"], advancedSettings: ["unique"], dangerSettings: ["retire", "purge"],
    recommended: {
      defaultsVersion: 1, value: field.value, constraints: field.constraints, storage: field.storage,
      display: field.display, ...(field.file ? { file: field.file } : {}), ...(field.json ? { json: field.json } : {}),
    },
    supportsRequired: true, supportsDefault: true, supportsUnique: true, needsPresence: false,
    displayPresets: ["plain", "currency"], conversionTargets: ["text", "number", "select"],
    conversionRules: ["strict"], compileStrategy: "native", userCreatable: type !== "autoDate",
  };
}

function described(type: LogicalTypeV2 = "number", caps: readonly CapabilityV2[] = [capability(type)]): FieldSettingsDescribeResultV2 {
  const field = definition(type);
  return {
    contract: "vibetable.schema.v2", tableId: "tbl_orders", fieldId: field.identity.fieldId,
    schemaRevision: "schema_7", dataRevision: 12, definition: field,
    capabilities: caps, recommendedDefaultsVersion: 1,
  };
}

function plan(): FieldChangePlanV2 {
  const before = definition();
  return {
    contract: "vibetable.schema.v2", planId: "plan_1", planHash: "hash_1", expiresAt: "2026-07-28T12:00:00Z",
    intent: {
      action: "purge", tableId: "tbl_orders", fieldId: before.identity.fieldId,
      expectedSchemaRevision: "schema_7", expectedDataRevision: 12, draft: null,
      actor: { id: "user_1", kind: "user" }, conversionRule: "", confirmation: "PURGE", backupReceipt: "vbr1_receipt",
    },
    before, after: null, classes: ["danger", "migration"], expectedSchemaRevision: "schema_7", expectedDataRevision: 12,
    impact: { records: 12, missing: 2, ambiguous: 1, failures: [], dependencies: [{ kind: "lookup", id: "lk_1", name: "客户名称" }] },
    steps: [{ kind: "archive", details: {} }], warnings: [{ code: "backup.required", path: "", message: "需要备份", details: {} }], errors: [],
    confirmations: ["PURGE", "DEPENDENCIES"], createsMigration: true, canApply: true,
  };
}

function migration(): FieldMigrationStatusV2 {
  return {
    contract: "vibetable.schema.v2", jobId: "job_1", planId: "plan_1", phase: "copying",
    processed: 4, total: 8, canCancel: true, error: null, updatedAt: "2026-07-28T12:00:00Z",
  };
}

function mountDrawer(): VueWrapper {
  const wrapper = mount(FieldSettingsDrawer, {
    attachTo: document.body,
    // 保留 Naive UI 的真实控件，仅替换 Teleport，避免把抽屉内容移出测试树。
    global: { stubs: { teleport: true } },
  });
  mounted.push(wrapper);
  return wrapper;
}

function buttonWithText(wrapper: VueWrapper, text: string): DOMWrapper<Element> {
  const candidate = wrapper.findAll("button").find(item => item.text().includes(text));
  if (!candidate) throw new Error(`找不到按钮：${text}`);
  return candidate;
}

async function openTab(wrapper: VueWrapper, text: string): Promise<void> {
  const names: Record<string, string> = { "高级": "advanced", "回收站": "recycle" };
  const candidate = wrapper.find(`[data-name="${names[text] ?? text}"]`);
  if (!candidate.exists()) throw new Error(`找不到标签：${text}`);
  await candidate.trigger("click");
  await flushPromises();
}

describe("FieldSettingsDrawer", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
  });
  afterEach(() => {
    mounted.splice(0).forEach(wrapper => wrapper.unmount());
    document.body.innerHTML = "";
  });

  it("展示加载和带错误码的失败状态", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    const wrapper = mountDrawer();
    expect(wrapper.find(".loading-card").exists()).toBe(true);

    const failure = Object.assign(new Error("revision 已过期"), { code: "field.conflict.stale_revision" });
    store.fail(failure);
    await flushPromises();
    expect(wrapper.get('[data-testid="field-settings-error"]').text()).toContain("field.conflict.stale_revision");
    expect(wrapper.text()).toContain("revision 已过期");
  });

  it("通过真实控件更新字段名、默认值，并按能力切换类型", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("number", [capability("number"), capability("text")]));
    const wrapper = mountDrawer();

    await wrapper.get('[data-testid="field-display-name"]').find("input").setValue("应收金额");
    await wrapper.get('[data-testid="field-default-enabled"]').trigger("click");
    await wrapper.get('[data-testid="field-default-number"]').find("input").setValue("0");
    expect(store.draft?.displayName).toBe("应收金额");
    expect(store.draft?.value.default).toMatchObject({ enabled: true, value: 0, source: "user" });

    store.changeType("text");
    expect(store.draft?.logicalType).toBe("text");
    expect(store.action).toBe("convert");
    expect(store.conversionRule).toBe("");
  });

  it.each([
    "text", "editor", "number", "bool", "date", "dateTime", "time", "autoDate", "email", "url",
    "select", "multiSelect", "relation", "file", "geoPoint", "json", "formula", "lookup",
  ] as const)("挂载 %s 的专属设置分支", async (type) => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described(type));
    if (type !== "formula" && type !== "lookup") {
      const defaultValues: Partial<Record<LogicalTypeV2, JsonValueV2>> = {
        bool: false,
        number: 0,
        select: "opt_a",
        date: "2026-08-12",
        dateTime: "2026-08-12T12:00:00Z",
        time: "12:00:00",
        geoPoint: { lat: 31.23, lon: 121.47 },
        json: { enabled: true },
      };
      store.patchDraft({
        value: {
          ...store.draft!.value,
          default: {
            ...store.draft!.value.default,
            enabled: true,
            value: defaultValues[type] ?? "示例值",
          },
        },
      });
    }
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.find(".identity-card").exists()).toBe(true);
    expect(wrapper.findAll(".settings-section").length).toBeGreaterThan(0);

    if (type === "select" || type === "multiSelect") {
      expect(wrapper.find(".option-row input:not([type='color'])").attributes("value")).toBe("选项 A");
    }
    if (type === "relation") expect(wrapper.html()).toContain("tbl_customers");
    if (type === "autoDate") expect(wrapper.html()).toContain("createdAt");
    if (type === "formula") {
      expect(wrapper.get('[data-testid="formula-editor-entry"]').text())
        .toContain("公式工作台");
      expect(wrapper.find('[data-testid="formula-source"]').exists()).toBe(false);
      expect(wrapper.find('[data-testid="field-default-enabled"]').exists()).toBe(false);
      expect(wrapper.text()).not.toContain("恢复当前类型推荐值");
      wrapper.findComponent(FormulaFieldEditor).vm.$emit("commit", {
        language: "cel-v1",
        source: "record.price * 3",
        resultType: "number",
      });
      await wrapper.vm.$nextTick();
      expect(store.draft?.formula?.source).toBe("record.price * 3");
    }
    if (type === "lookup") {
      expect(wrapper.get('[data-testid="lookup-editor-entry"]').text())
        .toContain("查找引用编辑器");
      expect(wrapper.find('[data-testid="lookup-relation-step-0"]').exists()).toBe(false);
      expect(wrapper.find('[data-testid="field-default-enabled"]').exists()).toBe(false);
      expect(wrapper.text()).not.toContain("恢复当前类型推荐值");
      wrapper.findComponent(LookupFieldEditor).vm.$emit("commit", {
        path: [{ relationFieldId: "fld_account" }],
        targetFieldId: "fld_balance",
      });
      await wrapper.vm.$nextTick();
      expect(store.draft?.lookup).toMatchObject({
        path: [{ relationFieldId: "fld_account" }],
        targetFieldId: "fld_balance",
      });
    }

    await openTab(wrapper, "高级");
    expect(wrapper.text()).toContain("数据源字段标识（只读）");
  });

  it("编辑选择项并发出关闭与预览事件", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("select"));
    const wrapper = mountDrawer();

    await buttonWithText(wrapper, "添加").trigger("click");
    expect(store.draft?.select?.options).toHaveLength(2);
    await wrapper.get('button[aria-label="停用选项"]').trigger("click");
    expect(store.draft?.select?.options[0]?.state).toBe("retired");
    await wrapper.get('[data-testid="field-plan-button"]').trigger("click");
    await buttonWithText(wrapper, "关闭").trigger("click");
    expect(wrapper.emitted("plan")).toHaveLength(1);
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("删除已存在选项前要求替换或清空规则，取消时不改变草稿", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("select"));
    store.patchDraft({
      select: {
        options: [
          ...(store.draft?.select?.options ?? []),
          { optionId: "opt_b", label: "选项 B", color: "#22c55e", order: 1, state: "active" },
        ],
      },
    });
    const wrapper = mountDrawer();
    const deleteButton = wrapper.get('button[aria-label="永久删除选项"]');

    expect(deleteButton.attributes("disabled")).toBeDefined();
    expect(store.draft?.select?.options).toHaveLength(2);
    store.setConversionRule("selectOption:opt_a:replace:opt_b");
    await wrapper.vm.$nextTick();
    await wrapper.get('button[aria-label="永久删除选项"]').trigger("click");
    expect(store.draft?.select?.options).toHaveLength(1);
    expect(store.draft?.select?.options[0]?.optionId).toBe("opt_b");
    expect(store.conversionRule).toBe("selectOption:opt_a:replace:opt_b");
  });

  it("渲染冻结计划、确认后允许应用，并覆盖迁移与回收站交互", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described());
    store.setPlan(plan());
    store.setRecycled([{ ...definition("text"), displayName: "已停用标题", lifecycle: { state: "retired", retiredAt: "2026-07-28T10:00:00Z" } }]);
    const wrapper = mountDrawer();

    expect(wrapper.get('[data-testid="field-change-plan"]').text()).toContain("purge");
    expect(wrapper.get('[data-testid="field-apply-button"]').attributes("disabled")).toBeDefined();
    const confirmations = wrapper.findAll('[role="checkbox"]');
    await confirmations.at(-2)!.trigger("click");
    await confirmations.at(-1)!.trigger("click");
    expect(store.confirmations).toEqual(["PURGE", "DEPENDENCIES"]);
    expect(wrapper.get('[data-testid="field-apply-button"]').attributes("disabled")).toBeUndefined();
    await wrapper.get('[data-testid="field-apply-button"]').trigger("click");
    store.setMigration(migration());
    await flushPromises();
    await buttonWithText(wrapper, "取消迁移").trigger("click");
    await openTab(wrapper, "回收站");
    await buttonWithText(wrapper, "刷新").trigger("click");
    await buttonWithText(wrapper, "恢复").trigger("click");

    expect(wrapper.emitted("apply")).toHaveLength(1);
    expect(wrapper.emitted("cancelMigration")).toHaveLength(1);
    expect(wrapper.emitted("loadRecycleBin")).toHaveLength(2);
    expect(wrapper.emitted("restore")?.[0]).toEqual(["fld_amount"]);
  });

  it("用单人场景解释字段元数据，并在计划生成后滚动到预览", async () => {
    const scrollIntoView = vi.fn();
    vi.stubGlobal("HTMLElement", HTMLElement);
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    });
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described());
    const wrapper = mountDrawer();

    expect(wrapper.text()).not.toContain("协作者");
    expect(wrapper.get("textarea").attributes("placeholder")).toContain("字段用途");
    await openTab(wrapper, "高级");
    expect(wrapper.text()).toContain("数据源字段标识（只读）");
    expect(wrapper.text()).toContain("普通使用无需修改");

    store.setPlan(plan());
    await flushPromises();
    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "nearest" });
    expect(wrapper.get('[data-testid="field-apply-button"]').text()).toContain("保存字段变更");
  });

  it("危险操作在发出计划请求前冻结对应动作", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described());
    const wrapper = mountDrawer();

    await openTab(wrapper, "高级");
    await buttonWithText(wrapper, "停用字段").trigger("click");
    expect(store.action).toBe("retire");
    await buttonWithText(wrapper, "永久清除").trigger("click");
    expect(store.action).toBe("purge");
    expect(wrapper.emitted("plan")).toHaveLength(2);
  });
});
