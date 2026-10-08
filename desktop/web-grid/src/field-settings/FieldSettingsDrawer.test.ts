import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import FieldSettingsDrawer from "./FieldSettingsDrawer.vue";
import { useFieldSettingsStore } from "./store";
import FormulaFieldEditor from "./formula/FormulaFieldEditor.vue";
import LookupFieldEditor from "./lookup/LookupFieldEditor.vue";
import { NInputNumber, NSelect, NSwitch } from "naive-ui";
import type {
  CapabilityV2,
  FieldChangePlanV2,
  FieldDefinitionV2,
  FieldMigrationStatusV2,
  FieldSettingsDescribeResultV2,
  JsonValueV2,
  LogicalTypeV2,
  SchemaSnapshot,
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

function lookupSchemaSnapshot(
  collection: string,
  columns: ReadonlyArray<Record<string, unknown>>,
): SchemaSnapshot {
  return {
    collection,
    primaryKey: "id",
    columns: columns.map(column => ({
      editable: true, nullable: true, ...column,
    })),
    normalizedRelations: [], schemaRevision: "schema_7", permissionRevision: "schema_7",
    capabilityHash: "cap", lookupRevision: "lk_1",
  } as unknown as SchemaSnapshot;
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

  it("允许编辑已有双向关联的另一端并保留不可变目标", async () => {
    const store = useFieldSettingsStore();
    const describedPair = described("relation");
    store.beginOpen();
    store.load({
      ...describedPair, definition: {
        ...describedPair.definition!, relation: {
          ...describedPair.definition!.relation!, deletePolicy: "restrict",
          pairId: "pair_1", reciprocalFieldId: "fld_orders",
        },
      },
    });
    store.loadRelationPair({
      reciprocalDisplayName: "订单", reciprocalCardinality: "many", sourceDisplayFieldId: "fld_number",
    });
    const wrapper = mountDrawer();
    await flushPromises();
    expect(store.dirty).toBe(false);
    await wrapper.get('[data-testid="relation-reciprocal-name"]').find("input").setValue("客户订单");
    const select = (id: string) => wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === id)!;
    expect(select("relation-target-table").props("disabled")).toBe(true);
    select("relation-reciprocal-cardinality").vm.$emit("update:value", "one");
    select("relation-source-display-field").vm.$emit("update:value", "fld_title");
    select("relation-source-cardinality").vm.$emit("update:value", "one");
    select("relation-target-display-field").vm.$emit("update:value", "fld_alias");
    select("relation-delete-policy").vm.$emit("update:value", "setNull");
    await flushPromises();
    expect(store.relationPair).toEqual({
      reciprocalDisplayName: "客户订单", reciprocalCardinality: "one", sourceDisplayFieldId: "fld_title",
    });
    expect(store.draft?.relation).toMatchObject({
      targetTableId: "tbl_customers", pairId: "pair_1", reciprocalFieldId: "fld_orders",
      cardinality: "one", displayFieldId: "fld_alias", deletePolicy: "setNull",
    });
    expect(store.canPlan).toBe(true);
    expect(select("relation-delete-policy").props("options")).toEqual([
      { label: "置空", value: "setNull" }, { label: "阻止删除", value: "restrict" },
    ]);
    await openTab(wrapper, "高级");
    expect(wrapper.text()).not.toContain("目标删除时级联删除本表记录");
  });

  it("展示冻结计划的两端名称、基数、显示字段和共享删除策略", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("relation"));
    for (const [kind, collection, fieldId, title] of [
      ["source", "tbl_orders", "fld_number", "订单编号"],
      ["target", "tbl_customers", "fld_name", "客户名称"],
    ] as const) {
      store.setRelationSchema(kind, {
        collection, primaryKey: "id", primaryDisplayFieldId: fieldId,
        columns: [{ name: `f_${fieldId}`, fieldId, title, kind: "scalar", dataType: "text", editable: true, nullable: false }],
        normalizedRelations: [], schemaRevision: "schema_7", permissionRevision: "schema_7",
        capabilityHash: "cap", lookupRevision: "lookup",
      });
    }
    const before: FieldDefinitionV2 = {
      ...definition("relation"), displayName: "客户",
      relation: { ...definition("relation").relation!, deletePolicy: "setNull", pairId: "pair_1", reciprocalFieldId: "fld_orders" },
    };
    const reciprocalBefore: FieldDefinitionV2 = {
      ...before, displayName: "订单",
      relation: { ...before.relation!, targetTableId: "tbl_orders", displayFieldId: "fld_number" },
    };
    const frozen = plan();
    store.setPlan({
      ...frozen, intent: { ...frozen.intent, action: "update", relationPairPatch: { sourceCardinality: "one" } },
      before, after: { ...before, displayName: "所属客户", relation: { ...before.relation!, cardinality: "one", deletePolicy: "restrict" } },
      confirmations: ["relationPair"],
      relatedChanges: [{
        tableId: "tbl_customers", fieldId: "fld_orders", expectedSchemaRevision: "schema_7",
        before: reciprocalBefore,
        after: { ...reciprocalBefore, displayName: "客户订单", relation: { ...reciprocalBefore.relation!, deletePolicy: "restrict" } },
      }],
    });
    const wrapper = mountDrawer();
    await flushPromises();
    const source = wrapper.get('[data-testid="field-plan-source-change"]').text();
    const reciprocal = wrapper.get('[data-testid="field-plan-reciprocal-change"]').text();
    expect(source).toContain("变更前：客户 · 多条");
    expect(source).toContain("变更后：所属客户 · 单条");
    expect(source).toContain("显示字段：客户名称");
    expect(source).toContain("共享删除策略：阻止删除");
    expect(reciprocal).toContain("变更后：客户订单 · 多条");
    expect(reciprocal).toContain("显示字段：订单编号");
    expect(source + reciprocal).not.toMatch(/fld_|tbl_|pair_1/);
    expect(wrapper.text()).toContain("同时应用预览中的两端关联设置与共享删除策略");
    expect(wrapper.text()).not.toContain("同时停用或删除");
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

  it("被阻止的计划渲染不兼容样本并保持保存禁用", () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described());
    const base = plan();
    const blocked: FieldChangePlanV2 = {
      ...base,
      canApply: false,
      impact: {
        ...base.impact,
        failures: [
          { recordId: "rec_15", reason: "field.value.invalid at value: value must be an integer" },
          { recordId: "rec_23", reason: "field.value.invalid at value: value must be an integer" },
        ],
      },
      errors: [{
        code: "field.constraint.existing_data_invalid", path: "draft.constraints",
        message: "existing records do not satisfy the requested field settings",
        details: { failed: 2, scanned: 6 },
      }],
    };
    store.setPlan(blocked);
    const wrapper = mountDrawer();

    const card = wrapper.get('[data-testid="field-change-plan"]');
    expect(card.text()).toContain("已阻止");
    expect(card.text()).toContain("不兼容样本");
    expect(card.text()).toContain("rec_15 · field.value.invalid at value: value must be an integer");
    expect(card.text()).toContain("rec_23 · field.value.invalid at value: value must be an integer");
    expect(wrapper.get('[data-testid="field-apply-button"]').attributes("disabled")).toBeDefined();
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

  it("lookup 目标选项从真实 schema 派生精确类型（路径列 dataType / 条件 FieldDefinitionV2）", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("lookup"));
    store.setLookupSchemas([lookupSchemaSnapshot("tbl_customers", [
      { name: "f_name", fieldId: "fld_name", title: "名称", kind: "scalar", dataType: "text" },
      // 数值公式列复用 schema dataType：呈现为 number，可被数值聚合使用。
      { name: "f_balance", fieldId: "fld_balance", title: "余额", kind: "formula", dataType: "decimal" },
    ])]);
    let wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.findComponent(LookupFieldEditor).props("targetFieldOptions")).toEqual([
      { label: "名称", value: "fld_name", logicalType: "text" },
      { label: "余额", value: "fld_balance", logicalType: "number" },
    ]);
    mounted.splice(mounted.indexOf(wrapper), 1);
    wrapper.unmount();
    document.body.innerHTML = "";

    // 条件模式：类型取来源表 FieldDefinitionV2 的 logicalType。
    store.lookupConditionSchema = lookupSchemaSnapshot("tbl_orders", [
      { name: "f_amount", fieldId: "fld_amount", title: "金额", kind: "scalar", dataType: "decimal" },
      { name: "f_note", fieldId: "fld_note", title: "备注", kind: "scalar", dataType: "text" },
    ]);
    store.lookupConditionFields = [
      definition("number"),
      { ...definition("text"), displayName: "备注", identity: { ...definition("text").identity, fieldId: "fld_note" } },
    ];
    wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.findComponent(LookupFieldEditor).props("targetFieldOptions")).toEqual([
      { label: "金额", value: "fld_amount", logicalType: "number" },
      { label: "备注", value: "fld_note", logicalType: "text" },
    ]);
  });

  it("高级币种入口复用受限下拉并保留已有合法自定义币种", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    const current = described("number");
    store.load({ ...current, definition: {
      ...current.definition!, display: { ...current.definition!.display, preset: "currency", currency: "cHf" },
    } });
    const wrapper = mountDrawer();
    await flushPromises();
    await openTab(wrapper, "高级");
    const currency = wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === "advanced-display-currency");
    expect(currency).toBeDefined();
    expect(currency!.props("options")).toContainEqual({ label: "cHf", value: "cHf" });
    expect(currency!.props("tag")).not.toBe(true);
    currency!.vm.$emit("update:value", "USD");
    await flushPromises();
    expect(store.draft?.display.currency).toBe("USD");
  });

  it("数字显示预设/位数/尾零/币种写入草稿并实时预览（AC1/AC2）", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("number"));
    const wrapper = mountDrawer();
    await flushPromises();
    const select = (id: string) => wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === id)!;
    const previews = () => wrapper.findAll('[data-testid="number-display-preview"] code')
      .map(item => item.text());

    // 预设选项来自能力声明；未知预设原样透出，已知预设给中文标签。
    expect(select("number-display-preset").props("options")).toEqual([
      { label: "plain", value: "plain" }, { label: "货币", value: "currency" },
    ]);
    // 默认（max + trim）预览：1234.56789 -> 1,234.57；12 -> 12。
    expect(previews()).toEqual(["1,234.57", "12"]);

    // 切换“固定”规范化尾零规则，并预览尾零保留。
    select("number-display-scale-mode").vm.$emit("update:value", "fixed");
    await flushPromises();
    expect(store.draft?.display.scaleMode).toBe("fixed");
    expect(store.draft?.display.trimTrailingZeros).toBe(false);
    expect(previews()).toEqual(["1,234.57", "12.00"]);

    // 货币预设附带币种并预览币符。
    select("number-display-preset").vm.$emit("update:value", "currency");
    await flushPromises();
    expect(store.draft?.display.preset).toBe("currency");
    expect(store.draft?.display.currency).toBe("CNY");
    expect(previews()).toEqual(["¥1,234.57", "¥12.00"]);

    // 整数显示只改呈现，不动存储 onlyInt。
    select("number-display-preset").vm.$emit("update:value", "integer");
    await flushPromises();
    expect(store.draft?.display.displayScale).toBe(0);
    expect(store.draft?.storage.options.onlyInt).toBe(false);
    expect(previews()).toEqual(["1,235", "12"]);

    // 百分比预设解释两种存储语义并预览同一显示。
    select("number-display-preset").vm.$emit("update:value", "percent");
    await flushPromises();
    expect(store.draft?.display.percentStorage).toBe("ratio");
    expect(wrapper.get('[data-testid="number-display-preview"]').text()).toContain("存储 0.125 显示 12.5%");
    select("number-display-percent-storage").vm.$emit("update:value", "percent");
    await flushPromises();
    expect(store.draft?.display.percentStorage).toBe("percent");
    expect(wrapper.get('[data-testid="number-display-preview"]').text()).toContain("存储 12.5 显示 12.5%");

    // 单位预设附着单位字符串；先把小数位调回 2 再验证附着规则。
    select("number-display-preset").vm.$emit("update:value", "unit");
    await flushPromises();
    wrapper.findAllComponents(NInputNumber)
      .find(item => item.attributes("data-testid") === "number-display-scale")!
      .vm.$emit("update:value", 2);
    await flushPromises();
    wrapper.get('[data-testid="number-display-unit"]').find("input").setValue("kg");
    await flushPromises();
    expect(store.draft?.display.unit).toBe("kg");
    expect(previews()[0]).toBe("1,234.57kg");

    // 关闭千分位后预览不再有分隔符。
    wrapper.findAllComponents(NSwitch)
      .find(item => item.attributes("data-testid") === "number-display-grouping")!
      .vm.$emit("update:value", false);
    await flushPromises();
    expect(store.draft?.display.useGrouping).toBe(false);
    expect(previews()[0]).toBe("1234.57kg");
    expect(store.dirty).toBe(true);
  });

  it("切回数字/整数显示预设会清掉残留单位，且不改存储 onlyInt", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("number"));
    const wrapper = mountDrawer();
    await flushPromises();
    const select = (id: string) => wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === id)!;
    select("number-display-preset").vm.$emit("update:value", "unit");
    await flushPromises();
    wrapper.get('[data-testid="number-display-unit"]').find("input").setValue("kg");
    await flushPromises();
    expect(store.draft?.display.unit).toBe("kg");
    // 切回“数字”：残留单位被清除，预览不再附着 kg（控件隐藏但值不残留）。
    select("number-display-preset").vm.$emit("update:value", "number");
    await flushPromises();
    expect(store.draft?.display.unit).toBeNull();
    expect(wrapper.findAll('[data-testid="number-display-preview"] code')[0].text())
      .toBe("1,234.57");
    // 整数显示同样清单位，且不触碰存储 onlyInt。
    select("number-display-preset").vm.$emit("update:value", "unit");
    await flushPromises();
    wrapper.get('[data-testid="number-display-unit"]').find("input").setValue("kg");
    await flushPromises();
    select("number-display-preset").vm.$emit("update:value", "integer");
    await flushPromises();
    expect(store.draft?.display.unit).toBeNull();
    expect(store.draft?.display.displayScale).toBe(0);
    expect(store.draft?.storage.options.onlyInt).toBe(false);
  });

  it("进度只写显示参数，评分显式开启整数范围，电话号码保持文本", async () => {
    const store = useFieldSettingsStore(); store.beginOpen();
    const numeric = { ...capability("number"), displayPresets: ["number", "progress", "rating"] };
    const text = { ...capability("text"), displayPresets: ["phone"] };
    store.load(described("number", [numeric, text]));
    const wrapper = mountDrawer(); await flushPromises();
    const preset = () => wrapper.findAllComponents(NSelect).find(item => item.attributes("data-testid") === "number-display-preset")!;
    preset().vm.$emit("update:value", "progress"); await flushPromises();
    expect(store.draft?.display.preset).toBe("progress"); expect(store.draft?.display.progressTarget).toBe(1);
    expect(store.draft?.constraints.range).toEqual({ min: null, max: null }); expect(store.draft?.storage.options.onlyInt).toBe(false);
    preset().vm.$emit("update:value", "rating"); await flushPromises();
    expect(store.draft?.storage.options.onlyInt).toBe(true); expect(store.draft?.constraints.range).toEqual({ min: 0, max: 5 });
    expect(store.draft?.display.progressTarget).toBeUndefined();
    wrapper.findAllComponents(NInputNumber).find(item => item.attributes("data-testid") === "rating-max")!.vm.$emit("update:value", 10);
    await flushPromises(); expect(store.draft?.constraints.range.max).toBe(10); expect(store.draft?.display.ratingMax).toBe(10);
    wrapper.findAllComponents(NSelect).find(item => item.attributes("data-testid") === "field-logical-type")!.vm.$emit("update:value", "phone");
    await flushPromises(); expect(store.draft?.logicalType).toBe("text"); expect(store.draft?.display.preset).toBe("phone");
    expect(wrapper.get('[data-testid="common-display-preview"]').text()).toContain("+86 010-0012 ext.03");
    wrapper.findAllComponents(NSelect).find(item => item.attributes("data-testid") === "field-logical-type")!.vm.$emit("update:value", "text");
    await flushPromises(); expect(store.draft?.logicalType).toBe("text"); expect(store.draft?.display.preset).toBe("plain");
  });

  it("小数位控件限 0..15 并稳定钳制非法输入（AC1）", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("number"));
    const wrapper = mountDrawer();
    await flushPromises();
    const scaleInput = wrapper.findAllComponents(NInputNumber)
      .find(item => item.attributes("data-testid") === "number-display-scale")!;
    expect(scaleInput.props("min")).toBe(0);
    expect(scaleInput.props("max")).toBe(15);
    scaleInput.vm.$emit("update:value", 17);
    await flushPromises();
    expect(store.draft?.display.displayScale).toBe(15);
    scaleInput.vm.$emit("update:value", null);
    await flushPromises();
    expect(store.draft?.display.displayScale).toBe(0);
  });

  it("尾零规则由位数模式唯一决定，不再提供独立开关", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("number"));
    const wrapper = mountDrawer();
    await flushPromises();
    // 只读说明，不提供无效开关；wire 字段在切换模式时规范化。
    expect(wrapper.find('[data-testid="number-display-trim"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="number-display-trailing-zeros-hint"]').text())
      .toContain("最多模式按最多位数显示并自动去掉尾零");
    const select = (id: string) => wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === id)!;
    select("number-display-scale-mode").vm.$emit("update:value", "fixed");
    await flushPromises();
    expect(store.draft?.display.trimTrailingZeros).toBe(false);
    expect(wrapper.get('[data-testid="number-display-trailing-zeros-hint"]').text())
      .toContain("固定模式始终保留指定位尾零");
    select("number-display-scale-mode").vm.$emit("update:value", "max");
    await flushPromises();
    expect(store.draft?.display.trimTrailingZeros).toBe(true);
  });

  it("Formula 数值列表按已提交或Go验证的元素类型开放数字设置", async () => {
    const store = useFieldSettingsStore();
    const current = described("formula");
    store.beginOpen();
    store.load({ ...current, definition: { ...current.definition!, formula: {
      language: "cel-v2", source: "UNIQUE([1.0, 2.0, 1.0])", resultType: "json", resultElementType: "number",
    } } });
    const wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(true);
    store.beginOpen();
    store.load({ ...current, definition: { ...current.definition!, formula: {
      language: "cel-v2", source: "UNIQUE([true, false])", resultType: "json", resultElementType: "bool",
    } } });
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(false);
    store.setFormulaValidation("UNIQUE([1.0, 2.0])", {
      canonicalSource: "UNIQUE([1.0, 2.0])", resultType: "json", resultElementType: "number",
      dependencies: [], relationAggregatePaths: [],
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(true);
  });

  it("数字显示按权威结果类型对 Formula 开放，文本结果不开放（AC4）", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    store.load(described("formula"));
    let wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("结果数字显示");
    // 货币预设同样作用于公式结果展示。
    wrapper.findAllComponents(NSelect)
      .find(item => item.attributes("data-testid") === "number-display-preset")!
      .vm.$emit("update:value", "currency");
    await flushPromises();
    expect(store.draft?.display.preset).toBe("currency");
    expect(wrapper.findAll('[data-testid="number-display-preview"] code')[0].text())
      .toBe("¥1,234.57");
    mounted.splice(mounted.indexOf(wrapper), 1);
    wrapper.unmount();
    document.body.innerHTML = "";

    // 文本结果的公式不开放数字显示入口。
    const textFormula = described("formula");
    const definitionWithTextResult = {
      ...textFormula.definition!,
      formula: { ...textFormula.definition!.formula!, resultType: "text" as const },
    } as FieldDefinitionV2;
    store.beginOpen();
    store.load({ ...textFormula, definition: definitionWithTextResult });
    wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(false);
  });

  it("数字显示按权威输出类型对 Lookup 开放：SUM 汇总与数值目标（AC4）", async () => {
    const store = useFieldSettingsStore();
    store.beginOpen();
    const lookupDescribed = described("lookup");
    store.load(lookupDescribed);
    store.setLookupSchemas([lookupSchemaSnapshot("tbl_customers", [
      { name: "f_name", fieldId: "fld_name", title: "名称", kind: "scalar", dataType: "text" },
      { name: "f_balance", fieldId: "fld_balance", title: "余额", kind: "scalar", dataType: "decimal" },
    ])]);
    let wrapper = mountDrawer();
    await flushPromises();
    // 文本目标且无聚合：不开放。
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(false);
    // 数值目标：数值列表元素适用数字显示。
    store.patchDraft({
      lookup: { ...store.draft!.lookup!, targetFieldId: "fld_balance" },
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(true);
    // 文本目标 + SUM 数值聚合：结果为 decimal，开放。
    store.patchDraft({
      lookup: {
        ...store.draft!.lookup!,
        targetFieldId: "fld_name",
        aggregation: "sum",
      },
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("结果数字显示");
    mounted.splice(mounted.indexOf(wrapper), 1);
    wrapper.unmount();
    document.body.innerHTML = "";

    // 文本聚合（如 values 保持文本元素）不开放。
    store.patchDraft({
      lookup: {
        ...store.draft!.lookup!,
        aggregation: "values",
        targetFieldId: "fld_name",
      },
    });
    wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.find('[data-testid="number-display-preset"]').exists()).toBe(false);
  });
});
