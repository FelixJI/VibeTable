import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SourceImportHistory from "./SourceImportHistory.vue";
import type { SourceImportEntry } from "@/contracts/importManagement";

function migration(overrides: Partial<SourceImportEntry> = {}): SourceImportEntry {
  return {
    jobId: "job-1",
    provider: "feishu",
    containerId: "space-1",
    sourceName: "云端空间",
    state: "interrupted",
    stage: "records",
    created: 400,
    total: 403,
    notSubmitted: 3,
    unknownRecords: 0,
    unknownBatch: null,
    targets: [
      { sourceTableId: "src_orders", tableId: "tbl_orders", name: "Orders", collection: "t_orders9f2a" },
      { sourceTableId: "src_people", tableId: "tbl_people", name: "People" },
    ],
    batches: [
      { jobId: "job-1", batchId: "b-schema", stage: "schema", tableId: "tbl_orders", created: 0, relationWrites: 0, attachmentWrites: 0 },
      { jobId: "job-1", batchId: "b-records", stage: "records", tableId: "tbl_orders", created: 400, relationWrites: 0, attachmentWrites: 0 },
    ],
    diagnostics: [],
    startedAt: "2026-10-01T08:00:00Z",
    finishedAt: null,
    sessionEpoch: 1,
    readWindow: { startedAt: "2026-10-01T08:00:00Z", finishedAt: "2026-10-01T08:05:00Z", consistency: "window" },
    snapshotFieldCount: 0,
    skippedFieldCount: 0,
    ...overrides,
  };
}

function mountHistory(entries: readonly SourceImportEntry[], cancellingJobId: string | null = null) {
  return mount(SourceImportHistory, { props: { entries, cancellingJobId } });
}

describe("SourceImportHistory", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("renders a strict committed/not-submitted/pending split for a partial migration", () => {
    // 400 committed of 403: never completed, never rounded, stage visible.
    const wrapper = mountHistory([migration()]);
    const row = wrapper.get('[data-testid="source-import-row"]');
    expect(row.attributes("data-state")).toBe("interrupted");
    expect(row.attributes("data-stage")).toBe("records");
    expect(wrapper.get('[data-testid="source-count-created"]').text()).toContain("已提交 400 行");
    expect(wrapper.get('[data-testid="source-count-not-submitted"]').text()).toContain("未提交 3 行");
    expect(wrapper.get('[data-testid="source-count-unknown"]').text()).toContain("待确认 0 行");
    expect(wrapper.get('[data-testid="source-count-total"]').text()).toContain("共 403 行");
    expect(row.text()).toContain("阶段：记录写入");
    expect(row.text()).not.toContain("已完成");
  });

  it("marks completion only for terminal succeeded receipts with full counts", () => {
    const wrapper = mountHistory([migration({
      state: "succeeded",
      stage: "settled",
      created: 403,
      notSubmitted: 0,
      unknownRecords: 0,
      finishedAt: "2026-10-01T08:06:00Z",
    })]);
    const row = wrapper.get('[data-testid="source-import-row"]');
    expect(row.attributes("data-state")).toBe("succeeded");
    expect(row.text()).toContain("已完成");
    // No stage chip once settled: the migration is closed, not in progress.
    expect(row.text()).not.toContain("阶段：");
  });

  it("explains an unknown batch even when unknownRecords is zero", () => {
    const wrapper = mountHistory([migration({ unknownBatch: "b-relations" })]);
    const hint = wrapper.get('[data-testid="source-hint-unknown-batch"]');
    expect(hint.text()).toContain("b-relations");
    expect(hint.text()).toContain("关系");
    expect(hint.text()).toContain("不会自动重放");
  });

  it("discloses paged window reads and never claims point-in-time consistency", () => {
    const windowed = mountHistory([migration()]);
    expect(windowed.get('[data-testid="source-hint-window"]').text()).toContain("分页窗口");

    const snapshot = mountHistory([migration({
      readWindow: { startedAt: "2026-10-01T08:00:00Z", finishedAt: "2026-10-01T08:05:00Z", consistency: "snapshot" },
    })]);
    expect(snapshot.find('[data-testid="source-hint-window"]').exists()).toBe(false);
  });

  it("labels snapshot/skip policies as degradation, not native computation copies", () => {
    const wrapper = mountHistory([migration({ snapshotFieldCount: 2, skippedFieldCount: 1 })]);
    const hint = wrapper.get('[data-testid="source-hint-snapshot"]');
    expect(hint.text()).toContain("2");
    expect(hint.text()).toContain("1");
    expect(hint.text()).toContain("未按原生计算复制");
  });

  it("never claims a failed migration rolled back committed work", () => {
    const wrapper = mountHistory([migration({ state: "failed", finishedAt: "2026-10-01T08:02:00Z" })]);
    const hint = wrapper.get('[data-testid="source-hint-failed"]');
    expect(hint.text()).toContain("失败不等于回滚");
    expect(hint.text()).toContain("不会被自动删除");
  });

  it("offers per-table open buttons only for committed targets", async () => {
    const wrapper = mountHistory([migration({ unknownBatch: "b-records" })]);
    await wrapper.get('[data-testid="source-import-detail-job-1"]').trigger("click");
    // Only the schema batch committed; its target stays openable even though
    // the later records batch is pending confirmation.
    const buttons = wrapper.findAll('[data-testid="source-target-open"]');
    expect(buttons).toHaveLength(1);
    expect(buttons[0]!.attributes("data-collection")).toBe("t_orders9f2a");
    // The button emits the committed physical identity, never the logical id.
    await buttons[0]!.trigger("click");
    expect(wrapper.emitted("openTarget")).toEqual([["t_orders9f2a"]]);

    // Legacy receipts without a committed collection fall back to the receipt
    // identity; nothing is derived or guessed in the frontend.
    const legacy = mountHistory([migration({ unknownBatch: null, notSubmitted: 0, created: 403, targets: [
      { sourceTableId: "src_people", tableId: "tbl_people", name: "People" },
    ], batches: [
      { jobId: "job-1", batchId: "b-schema-people", stage: "schema", tableId: "tbl_people", created: 0, relationWrites: 0, attachmentWrites: 0 },
    ] })]);
    await legacy.get('[data-testid="source-import-detail-job-1"]').trigger("click");
    const legacyButton = legacy.get('[data-testid="source-target-open"]');
    expect(legacyButton.attributes("data-collection")).toBe("tbl_people");
    await legacyButton.trigger("click");
    expect(legacy.emitted("openTarget")).toEqual([["tbl_people"]]);

    // A target whose only batch IS the unknown one has nothing committed.
    const onlyUnknown = mountHistory([migration({ unknownBatch: "b-records", batches: [
      { jobId: "job-1", batchId: "b-records", stage: "records", tableId: "tbl_orders", created: 400, relationWrites: 0, attachmentWrites: 0 },
    ] })]);
    await onlyUnknown.get('[data-testid="source-import-detail-job-1"]').trigger("click");
    expect(onlyUnknown.findAll('[data-testid="source-target-open"]')).toHaveLength(0);

    const none = mountHistory([migration({ batches: [] })]);
    await none.get('[data-testid="source-import-detail-job-1"]').trigger("click");
    expect(none.findAll('[data-testid="source-target-open"]')).toHaveLength(0);
    expect(none.text()).toContain("尚无已提交的目标表");
  });

  it("offers cancellation only for current queued/running source jobs", async () => {
    const wrapper = mountHistory([
      migration({ jobId: "run-1", state: "running", notSubmitted: 403, created: 0 }),
      migration({ jobId: "queue-1", state: "queued", notSubmitted: 403, created: 0 }),
      migration({ jobId: "done-1", state: "succeeded", stage: "settled", created: 403, notSubmitted: 0, finishedAt: "2026-10-01T08:06:00Z" }),
      migration({ jobId: "stalled-1", state: "unknown" }),
    ]);
    expect(wrapper.get('[data-job-id="run-1"]').find('[data-testid="source-import-cancel"]').exists()).toBe(true);
    expect(wrapper.get('[data-job-id="queue-1"]').find('[data-testid="source-import-cancel"]').exists()).toBe(true);
    expect(wrapper.get('[data-job-id="done-1"]').find('[data-testid="source-import-cancel"]').exists()).toBe(false);
    expect(wrapper.get('[data-job-id="stalled-1"]').find('[data-testid="source-import-cancel"]').exists()).toBe(false);

    await wrapper.get('[data-job-id="run-1"]').get('[data-testid="source-import-cancel"]').trigger("click");
    expect(wrapper.emitted("cancelTask")).toEqual([["run-1"]]);
  });

  it("maps diagnostics through the fixed localized code map with a safe generic fallback", async () => {
    const wrapper = mountHistory([migration({
      diagnostics: [
        { code: "source_import.relation_edges", blocking: true },
        { code: "totally_unknown_code", blocking: false },
      ],
    })]);
    await wrapper.get('[data-testid="source-import-detail-job-1"]').trigger("click");

    const diagnostics = wrapper.findAll('[data-testid="source-diagnostic"]');
    expect(diagnostics).toHaveLength(2);
    expect(diagnostics[0]!.text()).toContain("双向关系边集不一致");
    expect(diagnostics[1]!.text()).toContain("totally_unknown_code");
    expect(diagnostics[1]!.text()).toContain("任务报告");
  });

  it("explains unknown outcomes without replay or rollback claims", () => {
    const wrapper = mountHistory([migration({ state: "unknown" })]);
    const hint = wrapper.get('[data-testid="source-hint-unknown"]');
    expect(hint.text()).toContain("权威回执");
    expect(hint.text()).toContain("不会自动重放");
  });
});
