import { describe, expect, it, vi } from "vitest";

import type { HostBridge } from "@/bridge/hostBridge";
import type { ColumnSchema, ManagedAttachmentRef } from "@/contracts";
import {
  createStructuredCellDialogController,
  type StructuredCellBridge,
} from "./structuredCellDialogController";

const attachmentColumn: ColumnSchema = {
  name: "photos",
  title: "Photos",
  fieldId: "photos-id",
  dataType: "text",
  editable: true,
  nullable: true,
  attachmentPolicy: {
    maxFiles: 3,
    maxBytesPerFile: 1024,
    allowedMimeTypes: ["image/png"],
    thumbnailVariants: [],
    protected: false,
  },
};

const file = (storedName: string): ManagedAttachmentRef => ({
  contractVersion: "2.0",
  tableId: "items",
  recordId: "row-1",
  fieldId: "photos-id",
  storedName,
  originalName: `${storedName}.png`,
  mimeType: "image/png",
  size: 12,
  sha256: `sha256:${"b".repeat(64)}`,
  downloadCapability: "download-1",
  thumbnails: [],
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function setup(request: HostBridge["request"]) {
  const notify = vi.fn();
  const commitJson = vi.fn();
  const bridge: StructuredCellBridge = {
    request,
    notify: notify as HostBridge["notify"],
  };
  const controller = createStructuredCellDialogController({
    bridge,
    resolveAttachmentAuthority: () => ({
      tableId: "items",
      schemaRevision: "schema-1",
      expectedDigest: `sha256:${"a".repeat(64)}`,
    }),
    commitJson,
    getGrid: () => null,
    translate: key => key,
    reportError: vi.fn(),
    activeElement: () => null,
  });
  return { controller, notify, commitJson };
}

describe("structured cell dialog controller", () => {
  it("releases the explicit attachment trigger before showing the modal", async () => {
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const request = vi.fn().mockResolvedValue({ attachments: [] });
    const { controller } = setup(request as HostBridge["request"]);

    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
      trigger,
    });

    expect(controller.state.attachment.show).toBe(true);
    expect(document.activeElement).not.toBe(trigger);
    trigger.remove();
  });

  it("releases the active workspace when the attachment trigger is not focused", async () => {
    const workspace = document.createElement("div");
    workspace.tabIndex = 0;
    const trigger = document.createElement("button");
    workspace.append(trigger);
    document.body.append(workspace);
    workspace.focus();
    const request = vi.fn().mockResolvedValue({ attachments: [] });
    const { controller } = setup(request as HostBridge["request"]);

    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
      trigger,
    });

    expect(controller.state.attachment.show).toBe(true);
    expect(document.activeElement).not.toBe(workspace);
    workspace.remove();
  });

  it("invalidates an obsolete attachment response when the dialog is reopened", async () => {
    const first = deferred<{ attachments: ManagedAttachmentRef[] }>();
    const second = deferred<{ attachments: ManagedAttachmentRef[] }>();
    const request = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const { controller } = setup(request as HostBridge["request"]);

    const firstOpen = controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });
    await controller.dispatch({ type: "attachment.close" });
    const secondOpen = controller.dispatch({
      type: "attachment.open",
      rowKey: "row-2",
      column: attachmentColumn,
    });

    first.resolve({ attachments: [file("obsolete")] });
    await firstOpen;
    expect(controller.state.attachment.rowKey).toBe("row-2");
    expect(controller.state.attachment.files).toEqual([]);
    expect(controller.state.attachment.loading).toBe(true);

    second.resolve({ attachments: [file("current")] });
    await secondOpen;
    expect(controller.state.attachment.files.map(item => item.storedName)).toEqual(["current"]);
    expect(controller.state.attachment.loading).toBe(false);
  });

  it("binds mutations and notifications to the current authority revision", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      return { status: "applied" };
    });
    const { controller, notify } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    await controller.dispatch({ type: "attachment.download", storedName: "stored" });
    await controller.dispatch({ type: "attachment.remove", storedName: "stored" });

    expect(notify).toHaveBeenCalledWith("file.previewRequested", expect.objectContaining({
      recordId: "row-1",
      storedName: "stored",
    }));
    expect(notify).toHaveBeenCalledWith("file.downloadRequested", expect.objectContaining({
      originalName: "stored.png",
    }));
    expect(request).toHaveBeenCalledWith("file.removeRequested", expect.objectContaining({
      schemaRevision: "schema-1",
      expectedDigest: `sha256:${"a".repeat(64)}`,
    }));
    expect(controller.state.attachment.files).toEqual([]);
  });

  it("commits valid JSON through the injected mutation seam and keeps invalid edits open", async () => {
    const { controller, commitJson } = setup(vi.fn() as HostBridge["request"]);
    const jsonColumn: ColumnSchema = {
      name: "metadata",
      title: "Metadata",
      dataType: "json",
      editable: true,
      nullable: true,
    };
    await controller.dispatch({
      type: "json.open",
      rowKey: "row-1",
      column: jsonColumn,
      value: { approved: false },
      expectedDigest: "digest-1",
    });
    await controller.dispatch({ type: "json.change", value: { approved: true } });
    await controller.dispatch({ type: "json.validity", valid: false });
    await controller.dispatch({ type: "json.save" });
    expect(commitJson).not.toHaveBeenCalled();
    expect(controller.state.json.show).toBe(true);

    await controller.dispatch({ type: "json.validity", valid: true });
    await controller.dispatch({ type: "json.save" });
    expect(commitJson).toHaveBeenCalledWith({
      rowKey: "row-1",
      field: "metadata",
      originalValue: { approved: false },
      value: { approved: true },
      expectedDigest: "digest-1",
    });
    expect(controller.state.json.show).toBe(false);
  });

  it("restores JSON focus after transition cleanup releases the browser focus owner", async () => {
    const trigger = document.createElement("button");
    document.body.append(trigger);
    const scheduledFrames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      scheduledFrames.push(callback);
      return scheduledFrames.length;
    });
    const nativeFocus = trigger.focus.bind(trigger);
    let focusAttempts = 0;
    vi.spyOn(trigger, "focus").mockImplementation((options?: FocusOptions) => {
      focusAttempts += 1;
      if (focusAttempts > 1) nativeFocus(options);
    });
    const { controller } = setup(vi.fn() as HostBridge["request"]);
    const jsonColumn: ColumnSchema = {
      name: "metadata",
      title: "Metadata",
      dataType: "json",
      editable: true,
      nullable: true,
    };

    try {
      await controller.dispatch({
        type: "json.open",
        rowKey: "row-1",
        column: jsonColumn,
        value: { approved: true },
        expectedDigest: "digest-1",
        trigger,
      });
      await controller.dispatch({ type: "json.close" });
      await controller.dispatch({ type: "json.closed" });
      await vi.waitFor(() => expect(focusAttempts).toBe(1));

      expect(scheduledFrames).toHaveLength(1);
      scheduledFrames[0]?.(0);
      expect(document.activeElement).toBe(trigger);
    } finally {
      trigger.remove();
      vi.unstubAllGlobals();
    }
  });
});
