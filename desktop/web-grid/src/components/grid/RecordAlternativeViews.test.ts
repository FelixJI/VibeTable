import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import type { ColumnSchema, PresetView } from "@/contracts";
import RecordGalleryView from "./RecordGalleryView.vue";
import RecordKanbanView from "./RecordKanbanView.vue";
import RecordTimelineView from "./RecordTimelineView.vue";

const schema = [
  { name: "title", title: "标题", dataType: "text", editable: true, nullable: false },
  { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
  { name: "start", title: "开始", dataType: "date", editable: true, nullable: true },
  { name: "end", title: "结束", dataType: "date", editable: true, nullable: true },
  { name: "cover", title: "封面", dataType: "text", editable: true, nullable: true },
] as ColumnSchema[];

function view(input: Partial<PresetView>): PresetView {
  return {
    filters: [],
    sorts: [],
    search: "",
    visibleFields: [],
    layout: input.kind ?? "table",
    ...input,
  };
}

describe("alternative record views", () => {
  it("formats configured title fields on gallery and kanban cards", () => {
    const numeric: ColumnSchema = { name: "title", title: "标题", dataType: "decimal", editable: true, nullable: true,
      display: { kind: "number", preset: "progress", displayScale: 2, scaleMode: "max", trimTrailingZeros: true,
        useGrouping: false, currency: "CNY", percentStorage: "ratio", unit: null, precision: "exact",
        timezone: "system", mode: "default", trueLabel: "是", falseLabel: "否" } };
    const select: ColumnSchema = { ...numeric, dataType: "text", display: { ...numeric.display!, kind: "select", preset: "" },
      enumOptions: [{ optionId: "a", label: "标签", color: "blue", order: 0, state: "active" }] };
    const rich: ColumnSchema = { ...select, display: { ...select.display!, kind: "editor" } };
    for (const component of [RecordGalleryView, RecordKanbanView]) {
      for (const [column, value, expected] of [[numeric, 1.5, "150%"], [select, "a", "标签"],
        [rich, "<b>摘要</b><script>alert(1)</script>", "摘要"]] as const) {
        const card = mount(component, { props: { rows: [{ rowKey: "one", title: value, status: "open" }], schema: [column, schema[1]],
          view: view({ kind: component === RecordGalleryView ? "gallery" : "kanban", titleField: "title", groupField: "status" }) } });
        expect(card.find("article strong").text()).toBe(expected);
        expect(card.find("script").exists()).toBe(false); card.unmount();
      }
    }
  });

  it("renders authoritative Formula numeric lists on cards from bare query values", () => {
    const column = {
      name: "numbers", title: "数值", kind: "formula", dataType: "json", resultElementType: "number",
      editable: false, nullable: true,
      display: { kind: "readonly", preset: "currency", displayScale: 2, scaleMode: "fixed",
        trimTrailingZeros: false, useGrouping: true, currency: "CNY" as const, percentStorage: "ratio" as const,
        unit: null, precision: "exact", timezone: "system", mode: "default", indent: 0,
        trueLabel: "是", falseLabel: "否" },
    } as ColumnSchema;
    const values = Object.freeze([1234.56789, null, 0]);
    const gallery = mount(RecordGalleryView, { props: {
      rows: [
        { rowKey: "1", title: "A", numbers: values },
        { rowKey: "2", title: "B", numbers: { state: "ready", value: values } },
        { rowKey: "3", title: "C", numbers: { state: "failed", value: values } },
      ], schema: [...schema, column],
      view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "numbers"] }),
    } });
    const cards = gallery.findAll('[data-testid="gallery-card"]');
    expect(cards[0].text()).toContain("¥1,234.57 ·  · ¥0.00");
    expect(cards[1].text()).toContain("¥1,234.57 ·  · ¥0.00");
    expect(cards[2].text()).toContain("计算失败");
    expect(cards[2].text()).not.toContain("1,234");
    expect(values).toEqual([1234.56789, null, 0]);
  });

  it("renders relation card fields through the shared typed label projection with fallback spec", async () => {
    const relationColumn = {
      name: "contract", title: "合同", kind: "relation" as const, dataType: "text" as const,
      relationId: "lines.contract", editable: true, nullable: true,
    } as ColumnSchema;
    const relations = [{
      relationId: "lines.contract",
      fieldRef: "contract",
      sourceCollection: "lines",
      kind: "m2m" as const,
      relatedCollection: "contracts",
      unique: false,
      nullable: true,
      onDelete: "nullify" as const,
      preset: "standard" as const,
      selfRelation: false,
      managed: true,
      state: "valid" as const,
      diagnostics: [],
      displayFieldInfo: {
        fieldId: "fld_rate", dataType: "decimal" as const,
        display: { kind: "number" as const, preset: "percent" as const, displayScale: 1, scaleMode: "fixed" as const,
          trimTrailingZeros: false, useGrouping: true, currency: "" as const, percentStorage: "ratio" as const,
          unit: null, precision: "exact" as const, timezone: "system" as const,
          mode: "default" as const, indent: 0 as const, trueLabel: "是", falseLabel: "否" },
      },
      fallbackDisplayFieldInfo: {
        fieldId: "fld_amount", dataType: "decimal" as const,
        display: { kind: "number" as const, preset: "currency" as const, displayScale: 2, scaleMode: "fixed" as const,
          trimTrailingZeros: false, useGrouping: true, currency: "CNY" as const, percentStorage: "ratio" as const,
          unit: null, precision: "exact" as const, timezone: "system" as const,
          mode: "default" as const, indent: 0 as const, trueLabel: "是", falseLabel: "否" },
      },
    }];
    const ids = ["t1", "t2", "t3", "t4"];
    const gallery = mount(RecordGalleryView, { props: {
      rows: [{
        rowKey: "1", title: "A", contract: ids,
        __vibetableRelationLabels: {
          contract: {
            t1: { value: 0.125, source: "display" },
            t2: { value: 1982, source: "primary" },
            t3: { value: false, source: "display" },
          },
        },
      }], schema: [...schema, relationColumn], relations,
      view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "contract"] }),
    } });
    const card = gallery.get('[data-testid="gallery-card"]').text();
    expect(card).toContain("12.5% · ¥1,982.00 · ✕ +1");
    await gallery.setProps({ view: view({ kind: "gallery", titleField: "contract" }) });
    expect(gallery.get(".gallery-copy > strong").text()).toBe("12.5% · ¥1,982.00 · ✕ +1");
  });

  it("renders formula envelopes on cards by authoritative result type, never stale values (AC4)", () => {
    const formulaColumn = {
      name: "total", title: "合计", kind: "formula" as const, dataType: "decimal" as const,
      editable: false, nullable: true,
      display: {
        kind: "readonly", preset: "currency", displayScale: 2, scaleMode: "fixed",
        trimTrailingZeros: false, useGrouping: true, currency: "CNY",
        percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
        mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
      },
    } as ColumnSchema;
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [
          { rowKey: "1", title: "A", total: { state: "ready", value: 1234.56789 } },
          { rowKey: "2", title: "B", total: { state: "updating", value: 1234.56789 } },
        ],
        schema: [...schema, formulaColumn],
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "total"] }),
      },
    });
    const cards = gallery.findAll('[data-testid="gallery-card"]');
    expect(cards[0].text()).toContain("¥1,234.57");
    // 非 fresh 状态显示真实状态，不把旧值当新结果显示。
    expect(cards[1].text()).toContain("计算中");
    expect(cards[1].text()).not.toContain("1,234");
  });

  it("renders lookup envelopes on cards by authoritative output type and state (AC4)", () => {
    const lookupColumn = {
      name: "prices", title: "价格", kind: "lookup" as const, dataType: "json" as const,
      editable: false, nullable: true, lookupId: "orders.prices",
      display: {
        kind: "readonly", preset: "", displayScale: 2, scaleMode: "max",
        trimTrailingZeros: true, useGrouping: true, currency: "CNY",
        percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
        mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
      },
    } as ColumnSchema;
    const definitions = [{
      lookupId: "orders.prices", collection: "orders", fieldKey: "prices", displayName: "价格",
      path: [], source: { kind: "target_field" as const, fieldRef: "price" },
      outputType: "decimal" as const, resultCardinality: "many" as const,
      revision: 1, state: "valid" as const, diagnostics: [], dependencies: [],
    }];
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [
          // 真实 wire 形状：sidecar CellValue 恒定序列化 provenance 数组与分页元数据。
          { rowKey: "1", title: "A", prices: { state: "ok", value: [1.5, 1234.56789], provenance: [],
            provenanceTotal: 2, provenanceTotalKnown: true, provenanceOffset: 0, provenanceLimit: 100, provenanceHasMore: false } },
          { rowKey: "2", title: "B", prices: { state: "restricted", value: [42], provenance: [],
            provenanceTotal: 1, provenanceTotalKnown: true, provenanceOffset: 0, provenanceLimit: 100, provenanceHasMore: false } },
          { rowKey: "3", title: "C", prices: [1.5, 1234.56789] },
          { rowKey: "4", title: "D", prices: { state: "updating", value: null, provenance: [],
            diagnostic: { code: "calculation.pending", message: "computed value is waiting for recalculation" } } },
        ],
        schema: [...schema, lookupColumn],
        lookupDefinitions: definitions,
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "prices"] }),
      },
    });
    const cards = gallery.findAll('[data-testid="gallery-card"]');
    expect(cards[0].text()).toContain("1.5 · 1,234.57");
    expect(cards[1].text()).toContain("受限");
    expect(cards[1].text()).not.toContain("42");
    // 查询 wire 的 ready 结果是裸值：仍按权威定义格式化，而不是 JSON 数组文本。
    expect(cards[2].text()).toContain("1.5 · 1,234.57");
    expect(cards[2].text()).not.toContain("[1.5");
    // 重算窗口内 wire 返回 updating envelope：显示真实状态而不是空文本。
    expect(cards[3].text()).toContain("计算中");
    expect(cards[3].text()).not.toContain("1,234");
  });

  it("renders legitimate JSON lookup state objects as data; only real pending envelopes show 计算中", () => {
    const lookupColumn = {
      name: "payload", title: "负载", kind: "lookup" as const, dataType: "json" as const,
      editable: false, nullable: true, lookupId: "orders.payload",
    } as ColumnSchema;
    const definitions = [{
      lookupId: "orders.payload", collection: "orders", fieldKey: "payload", displayName: "负载",
      path: [{ relationId: "orders.contract" }], source: { kind: "target_field" as const, fieldRef: "payload" },
      outputType: "json" as const,
      revision: 1, state: "valid" as const, diagnostics: [], dependencies: [],
    }];
    const pending = { state: "updating", value: null, provenance: [],
      diagnostic: { code: "calculation.pending", message: "computed value is waiting for recalculation" } };
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [
          // 合法裸 JSON：源字段本身就是形如 envelope 的对象，wire 以裸值交付。
          { rowKey: "1", title: "A", payload: { state: "updating", value: 42 } },
          { rowKey: "2", title: "B", payload: { state: "updating", value: { depth: 3 } } },
          // 诊断码不是 calculation.pending：不是 sidecar 的 pending envelope，是数据。
          { rowKey: "3", title: "C", payload: { state: "updating", value: null,
            diagnostic: { code: "workflow.pending" } } },
          // 诊断码正确但 value 非空：真实生产者恒为 null，仍按数据渲染。
          { rowKey: "4", title: "D", payload: { state: "updating", value: 7,
            diagnostic: { code: "calculation.pending" } } },
          // 真实 pending envelope：sidecar 恒定携带 calculation.pending 诊断与空值。
          { rowKey: "5", title: "E", payload: pending },
        ],
        schema: [...schema, lookupColumn],
        lookupDefinitions: definitions,
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "payload"] }),
      },
    });
    const cards = gallery.findAll('[data-testid="gallery-card"]');
    // 与 grid 的裸值归一一致：按权威定义渲染数据本身，而不是永久显示“计算中”。
    expect(cards[0].text()).toContain('{"state":"updating","value":42}');
    expect(cards[0].text()).not.toContain("计算中");
    expect(cards[1].text()).toContain('{"state":"updating","value":{"depth":3}}');
    expect(cards[1].text()).not.toContain("计算中");
    expect(cards[2].text()).toContain('{"state":"updating","value":null,"diagnostic":{"code":"workflow.pending"}}');
    expect(cards[2].text()).not.toContain("计算中");
    expect(cards[3].text()).toContain('{"state":"updating","value":7,"diagnostic":{"code":"calculation.pending"}}');
    expect(cards[3].text()).not.toContain("计算中");
    // 真实 pending 仍显示状态，不用空值占据卡片。
    expect(cards[4].text()).toContain("计算中");
    expect(cards[4].text()).not.toContain("depth");
    // Kanban 车道标签走同一个 displayValue：合法 JSON 是数据，pending 才是状态。
    const kanbanData = mount(RecordKanbanView, {
      props: {
        rows: [{ rowKey: "1", title: "A", payload: { state: "updating", value: 42 } }],
        schema: [...schema, lookupColumn],
        lookupDefinitions: definitions,
        view: view({ kind: "kanban", groupField: "payload", titleField: "title" }),
      },
    });
    expect(kanbanData.text()).toContain('{"state":"updating","value":42}');
    expect(kanbanData.text()).not.toContain("计算中");
    const kanbanPending = mount(RecordKanbanView, {
      props: {
        rows: [{ rowKey: "2", title: "B", payload: pending }],
        schema: [...schema, lookupColumn],
        lookupDefinitions: definitions,
        view: view({ kind: "kanban", groupField: "payload", titleField: "title" }),
      },
    });
    expect(kanbanPending.text()).toContain("计算中");
    expect(kanbanPending.text()).not.toContain("depth");
    expect(kanbanPending.findAll('[data-testid="kanban-card"]')).toHaveLength(1);
  });

  it("recognizes only real LookupCellValue envelopes across states; bare JSON state objects stay data", () => {
    const lookupColumn = {
      name: "payload", title: "负载", kind: "lookup" as const, dataType: "json" as const,
      editable: false, nullable: true, lookupId: "orders.payload",
    } as ColumnSchema;
    const definitions = [{
      lookupId: "orders.payload", collection: "orders", fieldKey: "payload", displayName: "负载",
      path: [{ relationId: "orders.contract" }], source: { kind: "target_field" as const, fieldRef: "payload" },
      outputType: "json" as const,
      revision: 1, state: "valid" as const, diagnostics: [], dependencies: [],
    }];
    // sidecar CellValue 的完整 wire 形状：状态 + 分页元数据恒定序列化。
    const cell = (state: string, extra: Record<string, unknown>) => ({
      state, value: null, provenance: [], provenanceTotal: 0, provenanceTotalKnown: true,
      provenanceOffset: 0, provenanceLimit: 100, provenanceHasMore: false, ...extra,
    });
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [
          // 裸 JSON 可碰撞部分分页字段；缺完整 LookupCellValue 形状仍是数据。
          { rowKey: "1", title: "A", payload: { state: "ok", value: 42, provenance: [], provenanceTotal: 0 } },
          { rowKey: "2", title: "B", payload: { state: "restricted", value: 42, provenance: [], provenanceTotal: 0 } },
          { rowKey: "3", title: "C", payload: { state: "invalid", value: 42, provenance: [], provenanceTotal: 0 } },
          { rowKey: "4", title: "D", payload: { state: "too_expensive", value: 42, provenance: [], provenanceTotal: 0 } },
          { rowKey: "5", title: "E", payload: { state: "ready", value: 42 } },
          // 真实 envelope：按状态渲染，不泄露 stale 值。
          { rowKey: "6", title: "F", payload: cell("ok", { value: [1.5, 1234.56789], provenanceTotal: 2 }) },
          { rowKey: "7", title: "G", payload: cell("restricted", { value: [42], provenanceTotal: 1 }) },
          { rowKey: "8", title: "H", payload: cell("invalid", {
            diagnostic: { code: "lookup.value.source_missing", message: "关联的来源记录已不存在" } }) },
          // 无 Lookup 分页元数据的业务 JSON 仍是数据，即使携带 diagnostic。
          { rowKey: "9", title: "I", payload: { state: "invalid", value: null,
            diagnostic: { code: "lookup.value.source_missing", message: "关联的来源记录已不存在" } } },
          { rowKey: "10", title: "J", payload: { state: "too_expensive", value: null,
            diagnostic: { code: "workflow.pending", message: "等待处理" } } },
        ],
        schema: [...schema, lookupColumn],
        lookupDefinitions: definitions,
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "payload"] }),
      },
    });
    const cards = gallery.findAll('[data-testid="gallery-card"]');
    expect(cards[0].text()).toContain('{"state":"ok","value":42,"provenance":[],"provenanceTotal":0}');
    expect(cards[1].text()).toContain('{"state":"restricted","value":42,"provenance":[],"provenanceTotal":0}');
    expect(cards[1].text()).not.toContain("受限");
    expect(cards[2].text()).toContain('{"state":"invalid","value":42,"provenance":[],"provenanceTotal":0}');
    expect(cards[2].text()).not.toContain("无效");
    expect(cards[3].text()).toContain('{"state":"too_expensive","value":42,"provenance":[],"provenanceTotal":0}');
    expect(cards[3].text()).not.toContain("超出预算");
    expect(cards[4].text()).toContain('{"state":"ready","value":42}');
    // 真实 envelope 按权威定义格式化（json 输出类型不加数值格式化，decimal 场景由 AC4 覆盖）。
    expect(cards[5].text()).toContain("1.5 · 1234.56789");
    expect(cards[6].text()).toContain("受限");
    expect(cards[6].text()).not.toContain("42");
    expect(cards[7].text()).toContain("#REF!");
    expect(cards[8].text()).toContain('"lookup.value.source_missing"');
    expect(cards[8].text()).not.toContain("#REF!");
    expect(cards[9].text()).toContain('"workflow.pending"');
    expect(cards[9].text()).not.toContain("超出预算");
  });

  it("keeps runtime number-looking values unformatted for non-numeric declared columns", () => {
    const textColumn = { name: "code", title: "代码", dataType: "text", editable: true, nullable: true } as ColumnSchema;
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [{ rowKey: "1", title: "A", code: 1234.56789 }],
        schema: [...schema, textColumn],
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "code"] }),
      },
    });
    expect(gallery.get('[data-testid="gallery-card"]').text()).toContain("1234.56789");
  });

  it("renders numeric fields through the authoritative column display in cards and lanes (AC4)", () => {
    const amountColumn = {
      name: "amount", title: "金额", dataType: "decimal" as const, editable: true, nullable: true,
      display: {
        kind: "number", preset: "currency", displayScale: 2, scaleMode: "fixed",
        trimTrailingZeros: false, useGrouping: true, currency: "CNY",
        percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
        mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
      },
    } as ColumnSchema;
    const kanban = mount(RecordKanbanView, {
      props: {
        rows: [
          { rowKey: "1", title: "A", amount: 1234.56789 },
          { rowKey: "2", title: "B", amount: 12 },
        ],
        schema: [...schema, amountColumn],
        view: view({ kind: "kanban", groupField: "amount", titleField: "title" }),
      },
    });
    // Lane labels share the grid's numeric display contract.
    expect(kanban.text()).toContain("¥1,234.57");
    expect(kanban.text()).toContain("¥12.00");
    const gallery = mount(RecordGalleryView, {
      props: {
        rows: [{ rowKey: "1", title: "A", amount: 1234.56789 }],
        schema: [...schema, amountColumn],
        view: view({ kind: "gallery", titleField: "title", visibleFields: ["title", "amount"] }),
      },
    });
    expect(gallery.get('[data-testid="gallery-card"]').text()).toContain("¥1,234.57");
  });

  it("groups records into kanban lanes and keeps blank values visible", () => {
    const wrapper = mount(RecordKanbanView, {
      props: {
        rows: [
          { rowKey: "1", title: "准备合同", status: "进行中" },
          { rowKey: "2", title: "等待确认", status: null },
          { rowKey: "3", title: "发送归档", status: "已完成" },
        ],
        schema,
        view: view({ kind: "kanban", groupField: "status", titleField: "title" }),
      },
    });
    expect(wrapper.get('[data-testid="record-kanban-view"]').text()).toContain("进行中");
    expect(wrapper.text()).toContain("已完成");
    expect(wrapper.text()).toContain("未分组");
    expect(wrapper.findAll('[data-testid="kanban-card"]')).toHaveLength(3);
    expect(wrapper.get('[data-testid="kanban-card"]').attributes("draggable")).toBe("false");
  });

  it("renders active select labels and emits an option-id move without mutating rows", async () => {
    const rowDigest = `sha256:${"a".repeat(64)}`;
    const rows = [
      { rowKey: "1", title: "准备合同", status: "opt_todo", __vibetableDigest: rowDigest },
      { rowKey: "2", title: "等待确认", status: null, __vibetableDigest: `sha256:${"b".repeat(64)}` },
    ];
    const wrapper = mount(RecordKanbanView, {
      props: {
        rows,
        schema,
        view: view({ kind: "kanban", groupField: "status", titleField: "title" }),
        interactionEnabled: true,
        laneOptions: [
          { optionId: "opt_todo", label: "待处理" },
          { optionId: "opt_doing", label: "进行中" },
          { optionId: "opt_done", label: "已完成" },
        ],
      },
    });

    expect(wrapper.findAll('[data-testid="kanban-lane"]')).toHaveLength(4);
    expect(wrapper.text()).toContain("待处理");
    expect(wrapper.text()).toContain("进行中");
    expect(wrapper.text()).toContain("已完成");
    expect(wrapper.text()).toContain("未分组");
    expect(wrapper.text()).not.toContain("opt_todo");
    expect(wrapper.get('[data-option-id="opt_done"]').findAll('[data-testid="kanban-card"]'))
      .toHaveLength(0);

    await wrapper.get('[data-row-key="1"]').trigger("dragstart");
    await wrapper.get('[data-option-id="opt_done"]').trigger("drop");

    expect(wrapper.emitted("cardMove")).toEqual([[
      { rowKey: "1", targetOptionId: "opt_done", expectedDigest: rowDigest },
    ]]);
    expect(rows[0]?.status).toBe("opt_todo");
  });

  it("renders local gallery covers and falls back for missing or failed covers", async () => {
    const wrapper = mount(RecordGalleryView, {
      props: {
        rows: [
          { rowKey: "1", title: "蓝图", cover: "/covers/cover.png", status: "草稿" },
          { rowKey: "2", title: "无封面", cover: null, status: "完成" },
        ],
        schema,
        view: view({ kind: "gallery", coverField: "cover", titleField: "title" }),
      },
    });
    expect(wrapper.get('img[src="/covers/cover.png"]')).toBeTruthy();
    expect(wrapper.findAll('[data-testid="gallery-card"]')).toHaveLength(2);
    expect(wrapper.get('[data-testid="gallery-cover-placeholder"]')).toBeTruthy();
    await wrapper.get('img[src="/covers/cover.png"]').trigger("error");
    expect(wrapper.findAll('[data-testid="gallery-cover-placeholder"]')).toHaveLength(2);
  });

  it("renders timeline records as proportional horizontal ranges", () => {
    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [createPinia()] },
      props: {
        rows: [
          { rowKey: "1", title: "设计", start: "2026-08-01", end: "2026-08-04" },
          { rowKey: "2", title: "开发", start: "2026-08-05", end: "2026-08-10" },
        ],
        schema,
        view: view({ kind: "timeline", dateField: "start", endDateField: "end", titleField: "title" }),
      },
    });
    expect(wrapper.findAll('[data-testid="timeline-record"]')).toHaveLength(2);
    expect(wrapper.get('[data-testid="timeline-scale"]').text()).toContain("8月");
  });

  it("renders authorized point-date labels in UTC logical-day space", () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const value = "2026-08-12 00:00:00.000Z";
    const instant = new Date("2026-08-12T00:00:00.000Z");
    const locale = "en-US";
    const formatOptions = { month: "short", day: "numeric" } as const;
    const logicalDateLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "UTC",
    }).format(instant);
    const negativeOffsetLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "America/Los_Angeles",
    }).format(instant);
    const viewportStart = new Date("2026-08-05T00:00:00.000Z");
    const logicalViewportLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "UTC",
    }).format(viewportStart);
    const negativeOffsetViewportLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "America/Los_Angeles",
    }).format(viewportStart);
    expect(negativeOffsetLabel).not.toBe(logicalDateLabel);
    const NativeDateTimeFormat = Intl.DateTimeFormat;
    function DateTimeFormatMock(
      locales?: Intl.LocalesArgument,
      options?: Intl.DateTimeFormatOptions,
    ): Intl.DateTimeFormat {
      return new NativeDateTimeFormat(locales, options);
    }
    const formatter = vi.spyOn(Intl, "DateTimeFormat").mockImplementation(
      DateTimeFormatMock as typeof Intl.DateTimeFormat,
    );
    const pinia = createPinia();
    pinia.state.value.ui = { locale };

    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [pinia] },
      props: {
        rows: [{ rowKey: "row-1", title: "逻辑日期", start: value, __vibetableDigest: digest }],
        schema,
        view: view({
          kind: "timeline",
          dateField: "start",
          endDateField: null,
          titleField: "title",
        }),
        interactionEnabled: true,
        movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
      },
    });

    expect(formatter).toHaveBeenCalledWith(locale, {
      ...formatOptions,
      timeZone: "UTC",
    });
    expect(wrapper.get(".timeline-title small").text()).toBe(logicalDateLabel);
    expect(wrapper.get('[data-testid="timeline-scale"]').text()).toContain(logicalViewportLabel);
    expect(wrapper.get('[data-testid="timeline-scale"]').text())
      .not.toContain(negativeOffsetViewportLabel);
    expect(wrapper.get(".timeline-title small").text()).not.toBe(negativeOffsetLabel);
    expect(wrapper.get('[data-testid="timeline-track"]').attributes("data-start-date"))
      .toBe("2026-08-05");
    expect(wrapper.get('[data-testid="timeline-track"]').attributes("data-end-date"))
      .toBe("2026-08-19");
    formatter.mockRestore();
  });

  it("emits an opaque point-date move only for a controller-authorized Timeline record", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const rows = [
      { rowKey: "row-1", title: "可移动", start: "2026-08-12", __vibetableDigest: digest },
      { rowKey: "row-2", title: "范围锚点", start: "2026-08-20", __vibetableDigest: digest },
    ];
    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [createPinia()] },
      props: {
        rows,
        schema,
        view: view({
          kind: "timeline",
          dateField: "start",
          endDateField: null,
          titleField: "title",
        }),
        interactionEnabled: true,
        movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
      },
    });

    const records = wrapper.findAll('[data-testid="timeline-record"]');
    expect(records).toHaveLength(2);
    expect(records[0]!.attributes("draggable")).toBe("true");
    expect(records[0]!.attributes("data-date")).toBe("2026-08-12");
    expect(records[1]!.attributes("draggable")).toBe("false");
    await records[0]!.trigger("click");
    expect(wrapper.emitted("openRecord")?.[0]?.[0]).toMatchObject({ rowKey: "row-1" });
    await records[1]!.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("openRecord")?.[1]?.[0]).toMatchObject({ rowKey: "row-2" });
    expect(wrapper.text()).not.toContain("row-1");
    expect(wrapper.text()).not.toContain(digest);

    const setData = vi.fn();
    const dataTransfer = { setData, effectAllowed: "none", dropEffect: "none" };
    await records[0]!.trigger("dragstart", { dataTransfer });
    expect(setData).toHaveBeenCalledWith("text/plain", "row-1");

    const track = wrapper.findAll('[data-testid="timeline-track"]')[0]!;
    vi.spyOn(track.element, "getBoundingClientRect").mockReturnValue({
      left: 100,
      right: 900,
      top: 0,
      bottom: 64,
      width: 800,
      height: 64,
      x: 100,
      y: 0,
      toJSON: () => ({}),
    });
    expect(track.attributes("data-start-date")).toBe("2026-08-05");
    expect(track.attributes("data-end-date")).toBe("2026-08-27");
    await track.trigger("drop", { clientX: 500, dataTransfer });

    expect(wrapper.emitted("intent")).toEqual([[
      {
        type: "timeline.record.move",
        rowKey: "row-1",
        targetDate: "2026-08-16",
        expectedDigest: digest,
      },
    ]]);
    expect(rows[0]?.start).toBe("2026-08-12");
  });
});
