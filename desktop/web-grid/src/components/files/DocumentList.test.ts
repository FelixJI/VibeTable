import { afterEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import DocumentList from "./DocumentList.vue";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";

function entry(entryHandle: string, capabilities: DocumentEntry["capabilities"] = []): DocumentEntry {
  return {
    documentId: `${entryHandle}-document`,
    entryHandle,
    authority: "workspace",
    availability: "available",
    capabilities,
    displayName: `${entryHandle}.txt`,
    relativePath: `${entryHandle}.txt`,
    extension: "txt",
    mimeType: "text/plain",
    sizeBytes: 12,
    effectiveRevisionCreatedAt: "2026-08-23T00:00:00Z",
    formalVersion: 1,
    status: "active",
  };
}

describe("DocumentList", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("moves the single grid tab stop and selection with vertical arrows without wrapping", async () => {
    const entries = [entry("document-a"), { ...entry("document-b"), sizeBytes: 2048 }];
    const select = vi.fn();
    const wrapper = mount(DocumentList, {
      attachTo: document.body,
      props: {
        entries,
        selectedHandles: [entries[0]!.entryHandle],
        primaryHandle: entries[0]!.entryHandle,
        onSelect: select,
      },
    });
    const first = wrapper.get('[data-testid="document-row-document-a"]');
    const second = wrapper.get('[data-testid="document-row-document-b"]');

    expect(first.attributes("tabindex")).toBe("0");
    expect(second.attributes("tabindex")).toBe("-1");
    expect(second.text()).toContain("2.0 KB");
    (first.element as HTMLElement).focus();
    await first.trigger("keydown", { key: "ArrowDown" });

    expect(document.activeElement).toBe(second.element);
    expect(select).toHaveBeenCalledTimes(1);
    expect(select).toHaveBeenLastCalledWith(1, { toggle: false, range: false });

    await second.trigger("keydown", { key: "ArrowDown" });
    expect(document.activeElement).toBe(second.element);
    expect(select).toHaveBeenCalledTimes(1);

    await second.trigger("keydown", { key: "ArrowUp" });
    expect(document.activeElement).toBe(first.element);
    expect(select).toHaveBeenLastCalledWith(0, { toggle: false, range: false });
    await first.trigger("keydown", { key: "ArrowUp" });
    expect(document.activeElement).toBe(first.element);
    expect(select).toHaveBeenCalledTimes(2);
  });

  it("aligns an eligible drag target before emitting one opaque drag intent", async () => {
    const entries = [entry("document-a"), entry("document-b", ["dragOut"])];
    const select = vi.fn();
    const dragOut = vi.fn();
    const dataTransfer = { setData: vi.fn(), types: [] as string[] };
    const wrapper = mount(DocumentList, {
      props: {
        entries,
        selectedHandles: [entries[0]!.entryHandle],
        primaryHandle: entries[0]!.entryHandle,
        onSelect: select,
        onDragOut: dragOut,
      },
    });
    const second = wrapper.get('[data-testid="document-row-document-b"]');

    await second.trigger("dragstart", { dataTransfer });

    expect(select).toHaveBeenCalledOnce();
    expect(select).toHaveBeenCalledWith(1, { toggle: false, range: false });
    expect(dragOut).toHaveBeenCalledOnce();
    expect(dragOut).toHaveBeenCalledWith(entries[1]);
    expect(select.mock.invocationCallOrder[0]).toBeLessThan(dragOut.mock.invocationCallOrder[0]!);
    expect(dataTransfer.setData).not.toHaveBeenCalled();
  });

  it("fails closed when drag-out capability is revoked", async () => {
    const draggable = entry("document-b", ["dragOut"]);
    const select = vi.fn();
    const dragOut = vi.fn();
    const dataTransfer = { setData: vi.fn(), types: [] as string[] };
    const wrapper = mount(DocumentList, {
      props: {
        entries: [draggable],
        selectedHandles: [draggable.entryHandle],
        primaryHandle: draggable.entryHandle,
        onSelect: select,
        onDragOut: dragOut,
      },
    });
    expect(wrapper.get('[data-testid="document-row-document-b"]').attributes("draggable"))
      .toBe("true");

    await wrapper.setProps({ entries: [{ ...draggable, capabilities: [] }] });
    const row = wrapper.get('[data-testid="document-row-document-b"]');
    const event = new Event("dragstart", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
    row.element.dispatchEvent(event);

    expect(row.attributes("draggable")).toBe("false");
    expect(row.attributes("title")).toBeUndefined();
    expect(event.defaultPrevented).toBe(true);
    expect(select).not.toHaveBeenCalled();
    expect(dragOut).not.toHaveBeenCalled();
    expect(dataTransfer.setData).not.toHaveBeenCalled();
  });

  it("preserves the existing closed keyboard gesture emissions", async () => {
    const document = entry("document-a", ["open", "preview"]);
    const wrapper = mount(DocumentList, {
      props: {
        entries: [document],
        selectedHandles: [document.entryHandle],
        primaryHandle: document.entryHandle,
      },
    });
    const row = wrapper.get('[data-testid="document-row-document-a"]');

    await row.trigger("keydown", { key: "a", ctrlKey: true });
    await row.trigger("keydown", { key: "Enter" });
    await row.trigger("keydown", { key: " " });
    await row.trigger("keydown", { key: "F10", shiftKey: true });

    expect(wrapper.emitted("selectAll")).toHaveLength(1);
    expect(wrapper.emitted("open")).toEqual([[document]]);
    expect(wrapper.emitted("select")).toEqual([[0, { toggle: false, range: false }]]);
    expect(wrapper.emitted("preview")).toEqual([[document]]);
    expect(wrapper.emitted("context")).toEqual([[document, { x: 36, y: 24 }]]);
  });
});
