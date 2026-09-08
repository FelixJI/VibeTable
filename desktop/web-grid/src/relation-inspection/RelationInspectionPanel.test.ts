import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import RelationInspectionPanel from "./RelationInspectionPanel.vue";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/services/bridgeContext", () => ({ useHostBridge: () => ({ request }) }));
const endpoints = [
  { tableId: "t1", fieldId: "f1", schemaRevision: "s1", dataRevision: 1 },
  { tableId: "t2", fieldId: "f2", schemaRevision: "s2", dataRevision: 2 },
];
const cursor = { pairId: "pair", endpoints, after: ["r1", "r2"], done: [false, true], incomplete: false };
function page(overrides: Record<string, unknown> = {}) {
  return { pairId: "pair", endpoints, counts: {}, samples: [], samplesTruncated: false,
    rowsScanned: [1, 1], pageComplete: true, finished: false, complete: false, next: cursor, ...overrides };
}
let wrapper: VueWrapper | undefined;
afterEach(() => { wrapper?.unmount(); wrapper = undefined; request.mockReset(); });
async function click(text: string) {
  const button = wrapper!.findAll("button").find((item) => item.text() === text);
  expect(button, `button ${text}`).toBeDefined();
  await button!.trigger("click");
  await flushPromises();
}

describe("RelationInspectionPanel", () => {
  it("starts only on request and retains earlier findings when the last page completes coverage", async () => {
    request.mockResolvedValueOnce(page({ counts: { metadata_asymmetric: 1, dangling: 2 },
      samples: [{ code: "dangling", endpoint: 0, recordId: "old-row", targetId: "gone" }] }))
      .mockResolvedValueOnce(page({ counts: { metadata_asymmetric: 1, dangling: 1 },
        finished: true, complete: true, next: undefined }));
    wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
    expect(request).not.toHaveBeenCalled();
    await click("检查关系完整性");
    expect(request).toHaveBeenCalledExactlyOnceWith("relation.inspectPair", { tableId: "t1", fieldId: "f1", limit: 100 });
    await click("继续检查");
    expect(request).toHaveBeenLastCalledWith("relation.inspectPair", { tableId: "t1", fieldId: "f1", limit: 100, cursor });
    expect(wrapper.get('[data-finding="metadata_asymmetric"]').text()).toContain("1");
    expect(wrapper.get('[data-finding="dangling"]').text()).toContain("3");
    expect(wrapper.text()).toContain("old-row");
    expect(wrapper.text()).toContain("扫描覆盖完整");
    expect(wrapper.text()).toContain("不代表关系健康");
    expect(request).toHaveBeenCalledTimes(2);
  });
});

function deferred() {
  let resolve!: (value: unknown) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<unknown>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

it("stops an in-flight request and ignores its response after restarting", async () => {
  const old = deferred();
  request.mockReturnValueOnce(old.promise).mockResolvedValueOnce(page({ counts: { duplicate: 1 } }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  await click("停止检查");
  expect(wrapper.text()).toContain("已停止检查");
  await click("重新检查");
  old.resolve(page({ counts: { dangling: 99 } }));
  await flushPromises();
  expect(wrapper.find('[data-finding="dangling"]').exists()).toBe(false);
  expect(wrapper.get('[data-finding="duplicate"]').text()).toContain("1");
});

it("clears previous results on target change and ignores late failures", async () => {
  const old = deferred();
  request.mockResolvedValueOnce(page()).mockReturnValueOnce(old.promise)
    .mockResolvedValueOnce(page({ endpoints: [{ ...endpoints[0], tableId: "new", fieldId: "field" }, endpoints[1]],
      finished: true, complete: true, next: undefined }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  await click("继续检查");
  await wrapper.setProps({ tableId: "new", fieldId: "field" });
  expect(wrapper.text()).not.toContain("关系对：");
  await click("检查关系完整性");
  old.reject(new Error("stale failure"));
  await flushPromises();
  expect(wrapper.text()).not.toContain("stale failure");
  expect(wrapper.text()).toContain("new / field");
});

it("ignores a response after unmount without starting another request", async () => {
  const pending = deferred();
  request.mockReturnValueOnce(pending.promise);
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  wrapper.unmount(); wrapper = undefined;
  pending.resolve({ malformed: true });
  await flushPromises();
  expect(request).toHaveBeenCalledTimes(1);
});

it("requires a restart on revision conflict while keeping earlier findings", async () => {
  request.mockResolvedValueOnce(page({ counts: { dangling: 2 } }))
    .mockRejectedValueOnce({ code: "relation.inspect.revision_changed", message: "conflict" })
    .mockResolvedValueOnce(page({ finished: true, complete: true, next: undefined }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性"); await click("继续检查");
  expect(wrapper.get('[role="alert"]').text()).toContain("请重新检查");
  expect(wrapper.get('[data-finding="dangling"]').text()).toContain("2");
  expect(wrapper.findAll("button").some((item) => item.text() === "继续检查")).toBe(false);
  await click("重新检查");
  expect(request.mock.calls[2][1]).not.toHaveProperty("cursor");
  expect(wrapper.find('[data-finding="dangling"]').exists()).toBe(false);
  expect(wrapper.text()).toContain("已检查范围内暂无发现");
});

it("bounds displayed samples across pages and explains incomplete coverage", async () => {
  const samples = Array.from({ length: 50 }, (_, index) => ({ code: "invalid_value", endpoint: 1, recordId: `r${index}` }));
  request.mockResolvedValueOnce(page({ counts: { invalid_value: 50 }, samples }))
    .mockResolvedValueOnce(page({ counts: { invalid_value: 50 }, samples }))
    .mockResolvedValueOnce(page({ counts: { invalid_value: 50 }, samples, pageComplete: false,
      finished: true, complete: false, next: undefined }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性"); await click("继续检查"); await click("继续检查");
  expect(wrapper.get('[data-finding="invalid_value"]').text()).toContain("150");
  expect(wrapper.findAll('ol[aria-label="问题样例"] li')).toHaveLength(100);
  expect(wrapper.text()).toContain("问题样例已截断");
  expect(wrapper.text()).toContain("检查覆盖不完整");
  expect(wrapper.text()).toContain("本页有无法检查");
});

it.each([
  ["invalid result", { unexpected: true }],
  ["wrong target", page({ endpoints: [{ ...endpoints[0], fieldId: "wrong" }, endpoints[1]], finished: true, complete: true, next: undefined })],
])("rejects %s rather than displaying a healthy scan", async (_label, result) => {
  request.mockResolvedValueOnce(result);
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  expect(wrapper.get('[role="alert"]').text()).toContain("重新检查");
  expect(wrapper.text()).not.toContain("暂无发现");
});

it("does not merge a continuation from changed endpoint revisions", async () => {
  request.mockResolvedValueOnce(page({ counts: { dangling: 2 } }))
    .mockResolvedValueOnce(page({ endpoints: [{ ...endpoints[0], dataRevision: 2 }, endpoints[1]],
      counts: { dangling: 10 }, finished: true, complete: true, next: undefined }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性"); await click("继续检查");
  expect(wrapper.get('[role="alert"]').text()).toContain("已变化");
  expect(wrapper.get('[data-finding="dangling"]').text()).toContain("2");
});

it.each([new Error("读取失败"), "unknown failure"])("shows read failure and permits a new explicit scan", async (failure) => {
  request.mockRejectedValueOnce(failure);
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  expect(wrapper.find('[role="alert"]').exists()).toBe(true);
  expect(wrapper.findAll("button").some((item) => item.text() === "重新检查")).toBe(true);
});

it("reports malformed metadata even when the reverse endpoint cannot be identified", async () => {
  request.mockResolvedValueOnce(page({ pairId: "", endpoints: [endpoints[0], { tableId: "", fieldId: "", schemaRevision: "", dataRevision: 0 }],
    counts: { metadata_invalid: 1 }, samples: [{ code: "metadata_invalid", endpoint: 0, detail: "missing definition" }],
    samplesTruncated: true, pageComplete: false, finished: true, complete: false, next: undefined }));
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性");
  expect(wrapper.text()).toContain("无法识别");
  expect(wrapper.text()).toContain("missing definition");
  expect(wrapper.text()).toContain("检查覆盖不完整");
});

it.each([
  ["relation.inspect.revision_changed", "检查期间数据或字段已变化，请重新检查。", "此前发现仅供参考"],
  ["relation.inspect.storage_failed", "无法读取关系数据，请重试检查。", "无法读取关系数据"],
])("shows mapped Product HTTP error %s from a resolved bridge response", async (code, message, expected) => {
  request.mockResolvedValueOnce(page({ counts: { dangling: 2 } }))
    .mockResolvedValueOnce({ error: { code, path: "", message, details: {}, retryable: false } });
  wrapper = mount(RelationInspectionPanel, { props: { tableId: "t1", fieldId: "f1" } });
  await click("检查关系完整性"); await click("继续检查");
  expect(wrapper.get('[role="alert"]').text()).toContain(expected);
  expect(wrapper.get('[data-finding="dangling"]').text()).toContain("2");
  expect(wrapper.text()).not.toContain("无效结果");
  expect(wrapper.findAll("button").some((button) => button.text() === "继续检查")).toBe(false);
});
