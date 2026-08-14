import { flushPromises, mount } from "@vue/test-utils";
import { NDrawer, NInput, NSelect, NTag } from "naive-ui";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createHostBridge, type HostBridge, type WebViewLike } from "@/bridge/hostBridge";
import type { FieldDefinitionV2, LogicalTypeV2, SchemaSnapshotV2 } from "@/contracts";
import type {
  ContentProfile,
  ContentProfileSnapshot,
  RecordDocumentLinkSnapshot,
} from "@/contracts/generated/workbench";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";
import ContentRecordPanel from "./ContentRecordPanel.vue";

const field = (
  fieldId: string,
  physicalName: string,
  displayName: string,
  logicalType: LogicalTypeV2,
): FieldDefinitionV2 => ({
  contract: "vibetable.schema.v2",
  identity: { fieldId, physicalName, providerFieldId: `pb_${fieldId}` },
  displayName,
  help: "",
  logicalType,
  lifecycle: { state: "active", retiredAt: null },
  value: {
    required: false,
    default: { enabled: false, value: null, source: "recommended", defaultsVersion: 1 },
    presence: { mode: "native" },
  },
  constraints: {
    unique: { enabled: false, blankPolicy: "ignoreMissing" },
    range: { min: null, max: null },
    length: { min: null, max: null },
    pattern: { enabled: false, value: "" },
    domains: { only: [], except: [] },
    selection: { min: 0, max: null },
  },
  storage: {
    kind: logicalType === "editor" ? "pocketbase-editor" : "pocketbase-text",
    options: { onlyInt: false, maxSize: 0, convertURLs: false, presentable: true },
  },
  display: {
    kind: logicalType === "editor" ? "editor" : logicalType === "number" ? "number" : "text",
    preset: "default", displayScale: 2, scaleMode: "fixed", trimTrailingZeros: true,
    useGrouping: false, currency: "", percentStorage: "ratio", unit: null,
    precision: "exact", timezone: "local", mode: "plain", trueLabel: "是", falseLabel: "否",
  },
});

const definition: SchemaSnapshotV2 = {
  contract: "vibetable.schema.v2",
  tableId: "articles",
  displayName: "文章",
  kind: "base",
  schemaRevision: "schema-1",
  dataRevision: 1,
  archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
  fields: [
    field("title-id", "title", "标题", "text"),
    field("summary-id", "summary", "摘要", "text"),
    field("body-id", "body", "正文", "editor"),
    field("amount-id", "amount", "金额", "number"),
  ],
  capabilities: [],
};

const profile: ContentProfile = {
  contractVersion: "1.0",
  tableId: "articles",
  titleFieldId: "title-id",
  bodyFieldId: "body-id",
  summaryFieldId: "summary-id",
  searchableFieldIds: ["title-id", "body-id"],
};

const activeDocument: DocumentEntry = {
  documentId: "11111111-1111-4111-8111-111111111111",
  entryHandle: "entry-1",
  displayName: "需求.pdf",
  relativePath: "docs/需求.pdf",
  extension: ".pdf",
  authority: "workspace",
  availability: "available",
  mimeType: "application/pdf",
  sizeBytes: 20,
  effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
  formalVersion: 1,
  status: "active",
  capabilities: ["open"],
};
const brokenDocument: DocumentEntry = {
  ...activeDocument,
  documentId: "22222222-2222-4222-8222-222222222222",
  entryHandle: "entry-2",
  displayName: "旧附件.docx",
  relativePath: "missing/旧附件.docx",
  availability: "missing",
  status: "deleted",
};

function link(
  linkId: string,
  documentId: string,
  revision: string,
): RecordDocumentLinkSnapshot {
  return {
    link: {
      contractVersion: "1.0",
      linkId,
      tableId: "articles",
      recordId: "r1",
      documentId,
      role: "reference",
      order: 0,
    },
    revision,
  };
}

function host(options: {
  profile?: ContentProfileSnapshot | null;
  links?: readonly RecordDocumentLinkSnapshot[];
  failProfile?: string;
  failLink?: string;
  failMutation?: string;
} = {}) {
  let currentProfile = options.profile === undefined
    ? { profile, revision: "profile-1" }
    : options.profile;
  let links = options.links
    ? [...options.links]
    : options.profile
      ? [link("link-broken", brokenDocument.documentId, "link-1")]
      : [];
  const request = vi.fn(async (type: string, payload: Record<string, unknown>) => {
    if (type === "schema.getTable") return definition;
    if (type === "contentProfile.load") {
      if (options.failProfile) throw new Error(options.failProfile);
      return currentProfile ?? { error: { code: "content_profile.not_found", message: "missing" } };
    }
    if (type === "contentProfile.commit") {
      if (options.failProfile) throw new Error(options.failProfile);
      currentProfile = { profile: payload.profile as ContentProfile, revision: "profile-2" };
      return currentProfile;
    }
    if (type === "recordDocumentLink.list") return { items: links };
    if (type === "recordDocumentLink.commit") {
      if (options.failLink) throw new Error(options.failLink);
      links = [...links, { link: payload.link, revision: "link-2" } as RecordDocumentLinkSnapshot];
      return links.at(-1);
    }
    if (type === "recordDocumentLink.repair") {
      links = links.map((item) => item.link.linkId === payload.linkId
        ? { ...item, link: { ...item.link, documentId: payload.documentId as string }, revision: "link-3" }
        : item);
      return links[0];
    }
    if (type === "recordDocumentLink.delete") {
      links = links.filter((item) => item.link.linkId !== payload.linkId);
      return { linkId: payload.linkId };
    }
    if (type === "mutation.apply") {
      if (options.failMutation) throw new Error(options.failMutation);
      return {
        contractVersion: "2.0",
        status: "applied",
        changeSetId: "change-1",
        affectedRows: [{
          recordId: "r1",
          operation: "update",
          revision: "row_0002",
          digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        }],
        computedFields: {},
        newRevision: "data_0002",
        emittedEvents: [],
        warnings: [],
      };
    }
    throw new Error(`unexpected request: ${type}`);
  });
  setHostBridgeForTesting({ request } as unknown as HostBridge);
  return request;
}

function mountPanel(props: Record<string, unknown> = {}) {
  return mount(ContentRecordPanel, {
    props: {
      show: true,
      tableId: "articles",
      row: {
        rowKey: "r1",
        __vibetableDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        title: "初始标题",
        summary: "初始摘要",
        body: "初始正文",
        amount: 10,
      },
      columns: [],
      documents: [activeDocument, brokenDocument],
      documentLabels: {
        [activeDocument.documentId]: activeDocument.displayName,
        [brokenDocument.documentId]: brokenDocument.displayName,
      },
      ...props,
    },
    global: { stubs: { teleport: true } },
  });
}

describe("ContentRecordPanel", () => {
  beforeEach(() => {
    vi.spyOn(crypto, "randomUUID").mockReturnValue("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa");
  });

  afterEach(() => {
    setHostBridgeForTesting(null);
    vi.restoreAllMocks();
  });

  it("configures a content profile from SchemaCore field identities", async () => {
    const request = host({ profile: null });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.get('[data-testid="content-profile-config"]').text()).toContain("SchemaCore");
    const selects = wrapper.findAllComponents(NSelect);
    expect(selects[0]!.props("options")).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: "标题", value: "title-id" }),
      expect.objectContaining({ label: "正文", value: "body-id" }),
    ]));
    expect(selects[1]!.props("options")).toEqual([
      { label: "正文", value: "body-id" },
    ]);

    selects[0]!.vm.$emit("update:value", "title-id");
    selects[1]!.vm.$emit("update:value", "body-id");
    selects[2]!.vm.$emit("update:value", "summary-id");
    selects[3]!.vm.$emit("update:value", ["title-id", "body-id"]);
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find((button) => button.text().includes("保存内容配置"))!.trigger("click");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("contentProfile.commit", expect.objectContaining({
      profile: profile,
      expectedRevision: null,
    }));
    expect(wrapper.get('[data-testid="content-record-panel"]').text()).toContain("初始标题");
    wrapper.unmount();
  });

  it("loads fresh V1 schema options through the production HostBridge whitelist", async () => {
    const listeners: Array<(event: { readonly data: unknown }) => void> = [];
    const webview: WebViewLike = {
      postMessage(message) {
        const envelope = message as { type: string; requestId: string };
        const payload = envelope.type === "schema.getTable"
          ? {
              ...definition,
              fields: [
                field("title-id", "title", "Title", "text"),
                field("body-id", "body", "Body", "editor"),
              ],
            }
          : envelope.type === "contentProfile.load"
            ? { error: { code: "content_profile.not_found", message: "missing" } }
            : envelope.type === "recordDocumentLink.list"
              ? { items: [] }
              : null;
        queueMicrotask(() => {
          for (const listener of listeners) {
            listener({ data: { type: envelope.type, requestId: envelope.requestId, payload } });
          }
        });
      },
      addEventListener(_type, listener) {
        listeners.push(listener);
      },
      removeEventListener(_type, listener) {
        const index = listeners.indexOf(listener);
        if (index >= 0) listeners.splice(index, 1);
      },
    };
    const bridge = createHostBridge({ webview, timeoutMs: 1_000 });
    bridge.start();
    setHostBridgeForTesting(bridge);

    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.find('[data-testid="content-profile-config"]').exists()).toBe(true);
    expect(wrapper.findAllComponents(NSelect)[0]!.props("options")).toEqual([
      { label: "Title", value: "title-id" },
      { label: "Body", value: "body-id" },
    ]);

    wrapper.unmount();
    bridge.stop();
  });

  it("keeps the drawer open while Escape dismisses a field select menu", async () => {
    host({ profile: null });
    const wrapper = mountPanel();
    await flushPromises();

    const drawer = wrapper.getComponent(NDrawer);
    const searchable = wrapper.findAllComponents(NSelect)[3]!;
    expect(drawer.props("closeOnEsc")).toBe(true);

    searchable.vm.$emit("update:show", true);
    await wrapper.vm.$nextTick();
    expect(drawer.props("closeOnEsc")).toBe(false);

    wrapper.findAllComponents(NSelect)[3]!.vm.$emit("update:show", false);
    await wrapper.vm.$nextTick();
    expect(drawer.props("closeOnEsc")).toBe(true);

    wrapper.findAllComponents(NSelect)[3]!.vm.$emit("update:show", true);
    await wrapper.setProps({ show: false });
    expect(drawer.props("closeOnEsc")).toBe(true);
    wrapper.unmount();
  });

  it("edits mapped record fields and creates, repairs, and removes document links", async () => {
    const request = host({ profile: { profile, revision: "profile-1" } });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.text()).toContain("关联已断开");

    await wrapper.findAll("button").find((button) => button.text().includes("编辑内容"))!.trigger("click");
    const inputs = wrapper.findAllComponents(NInput);
    inputs[0]!.vm.$emit("update:value", "新标题");
    inputs[1]!.vm.$emit("update:value", "新摘要");
    inputs[2]!.vm.$emit("update:value", "新正文");
    await wrapper.findAll("button").find((button) => button.text().includes("保存记录"))!.trigger("click");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("mutation.apply", expect.objectContaining({
      contractVersion: "2.0",
      tableId: "articles",
      schemaRevision: "schema-1",
      expectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      operations: [{
        kind: "update",
        recordId: "r1",
        values: {
          title: "新标题",
          body: "新正文",
          summary: "新摘要",
        },
      }],
    }));
    expect(wrapper.emitted("saved")).toHaveLength(1);
    expect(wrapper.get('[data-testid="content-record-panel"]').text()).toContain("新标题");
    expect(wrapper.get('[data-testid="content-record-panel"]').text()).toContain("新正文");
    await wrapper.setProps({ row: null });
    expect(wrapper.get('[data-testid="content-record-panel"]').text()).toContain("新正文");
    await wrapper.setProps({
      row: {
        rowKey: "r1",
        __vibetableDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        title: "新标题",
        summary: "新摘要",
        body: "新正文",
        amount: 10,
      },
    });
    expect(wrapper.get('[data-testid="content-record-panel"]').text()).toContain("新正文");

    let selects = wrapper.findAllComponents(NSelect);
    selects[0]!.vm.$emit("update:value", activeDocument.documentId);
    selects[1]!.vm.$emit("update:value", "source");
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find((button) => button.text().includes("建立关联"))!.trigger("click");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("recordDocumentLink.commit", expect.objectContaining({
      link: expect.objectContaining({ documentId: activeDocument.documentId, role: "source", recordId: "r1" }),
    }));

    selects = wrapper.findAllComponents(NSelect);
    selects[0]!.vm.$emit("update:value", activeDocument.documentId);
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find((button) => button.text().includes("重新绑定"))!.trigger("click");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("recordDocumentLink.repair", expect.objectContaining({
      linkId: "link-broken",
      documentId: activeDocument.documentId,
      expectedRevision: "link-1",
    }));

    await wrapper.findAll("button").find((button) => button.text().includes("移除关联"))!.trigger("click");
    await flushPromises();
    expect(request).toHaveBeenCalledWith("recordDocumentLink.delete", expect.objectContaining({
      expectedRevision: "link-3",
    }));
    wrapper.unmount();
  });

  it("uses the parent document-label projection after the panel is unmounted and reopened", async () => {
    host({
      profile: { profile, revision: "profile-1" },
      links: [link("link-active", activeDocument.documentId, "link-1")],
    });
    const firstPanel = mountPanel({ documents: [activeDocument] });
    await flushPromises();

    const activeCard = firstPanel.get('[data-testid="content-link-link-active"]');
    expect(activeCard.text()).toContain(activeDocument.displayName);
    expect(activeCard.text()).toContain("正常");
    firstPanel.unmount();

    const reopenedPanel = mountPanel({ documents: [] });
    await flushPromises();

    const brokenCard = reopenedPanel.get('[data-testid="content-link-link-active"]');
    expect(brokenCard.text()).toContain(activeDocument.displayName);
    expect(brokenCard.findComponent(NTag).text()).toBe("关联已断开");
    reopenedPanel.unmount();
  });

  it("handles no selection, close, reload, and stable profile/link failures", async () => {
    host({ profile: { profile, revision: "profile-1" }, failLink: "link.conflict" });
    const wrapper = mountPanel({ row: null });
    await flushPromises();
    expect(wrapper.text()).toContain("请先在表格中选择一条记录");
    wrapper.findComponent(NDrawer).vm.$emit("update:show", false);
    expect(wrapper.emitted("close")).toHaveLength(1);

    await wrapper.setProps({ row: { id: 8, title: null, summary: null, body: null } });
    await flushPromises();
    expect(wrapper.text()).toContain("记录 8");
    const selects = wrapper.findAllComponents(NSelect);
    selects[0]!.vm.$emit("update:value", activeDocument.documentId);
    await wrapper.vm.$nextTick();
    await wrapper.findAll("button").find((button) => button.text().includes("建立关联"))!.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("link.conflict");
    wrapper.unmount();

    host({ profile: null, failProfile: "schema.offline" });
    const failed = mountPanel();
    await flushPromises();
    expect(failed.text()).toContain("schema.offline");
    failed.unmount();
  });

  it("keeps all content drafts editable when the atomic record mutation conflicts", async () => {
    host({
      profile: { profile, revision: "profile-1" },
      failMutation: "mutation.digest_conflict",
    });
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get('[data-testid="content-edit"]').trigger("click");
    const inputs = wrapper.findAllComponents(NInput);
    inputs[0]!.vm.$emit("update:value", "冲突标题");
    inputs[2]!.vm.$emit("update:value", "冲突正文");
    await wrapper.get('[data-testid="content-record-save"]').trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("mutation.digest_conflict");
    expect(wrapper.find('[data-testid="content-record-save"]').exists()).toBe(true);
    expect(wrapper.emitted("saved")).toBeUndefined();
    wrapper.unmount();
  });
});
