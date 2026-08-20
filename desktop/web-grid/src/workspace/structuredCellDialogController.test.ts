import { nextTick } from "vue";
import { describe, expect, it, vi } from "vitest";

import { BridgeOperationError, type HostBridge } from "@/bridge/hostBridge";
import type {
  ColumnEditSchema,
  ColumnSchema,
  ManagedAttachmentRef,
  TablePage,
} from "@/contracts";
import { createGrid } from "@/grid/createGrid";
import {
  createStructuredDialogFocus,
  type StructuredDialogFocus,
  type StructuredGridLike,
} from "@/services/dialogFocus";
import { createNaiveModalAfterLeaveAdapter } from "@/services/naiveModalAfterLeave";
import {
  createStructuredCellDialogController,
  type StructuredCellBridge,
  type StructuredCellDialogController,
  type StructuredCellDialogKind,
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
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function setup(
  request: HostBridge["request"],
  dialogFocus: StructuredDialogFocus = createStructuredDialogFocus({
    getGrid: () => ({
      getRows: () => ["row-1", "row-2"].map(rowKey => ({
        getIndex: () => rowKey,
      })),
    }),
    getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
    subscribeScope: () => () => undefined,
  }),
) {
  const commitJson = vi.fn();
  const reportError = vi.fn();
  const bridge: StructuredCellBridge = {
    request,
  };
  const controller = createStructuredCellDialogController({
    bridge,
    resolveAttachmentAuthority: () => ({
      tableId: "items",
      schemaRevision: "schema-1",
      expectedDigest: `sha256:${"a".repeat(64)}`,
    }),
    commitJson,
    dialogFocus,
    translate: key => key,
    reportError,
    activeElement: () => null,
  });
  return { controller, commitJson, dialogFocus, reportError };
}

function releaseClose(
  controller: StructuredCellDialogController,
  dialog: StructuredCellDialogKind,
): void {
  const lease = controller.claimCloseLease(dialog);
  expect(lease).not.toBeNull();
  lease?.release();
}

interface RealTabulatorDomFixture {
  readonly host: HTMLDivElement;
  restore(): void;
}

function installRealTabulatorDomFixture(): RealTabulatorDomFixture {
  const host = document.createElement("div");
  host.style.width = "800px";
  host.style.height = "400px";
  document.body.append(host);

  const restorers: Array<() => void> = [];
  const frameTimers = new Map<number, ReturnType<typeof setTimeout>>();
  let nextFrameId = 1;

  function replaceProperty(
    target: object,
    key: PropertyKey,
    descriptor: PropertyDescriptor,
  ): void {
    const previous = Object.getOwnPropertyDescriptor(target, key);
    Object.defineProperty(target, key, { configurable: true, ...descriptor });
    restorers.push(() => {
      if (previous) Object.defineProperty(target, key, previous);
      else Reflect.deleteProperty(target, key);
    });
  }

  const belongsToFixture = (element: HTMLElement): boolean =>
    element === host || host.contains(element);

  for (const property of ["offsetWidth", "clientWidth", "scrollWidth"] as const) {
    replaceProperty(HTMLElement.prototype, property, {
      get(this: HTMLElement): number {
        return belongsToFixture(this) ? 800 : 0;
      },
    });
  }
  for (const property of ["offsetHeight", "clientHeight", "scrollHeight"] as const) {
    replaceProperty(HTMLElement.prototype, property, {
      get(this: HTMLElement): number {
        return belongsToFixture(this) ? 400 : 0;
      },
    });
  }

  const nativeGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
  replaceProperty(HTMLElement.prototype, "getBoundingClientRect", {
    writable: true,
    value(this: HTMLElement): DOMRect {
      if (!belongsToFixture(this)) return nativeGetBoundingClientRect.call(this);
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 800,
        bottom: 400,
        width: 800,
        height: 400,
        toJSON: () => ({}),
      } as DOMRect;
    },
  });

  class FixtureResizeObserver implements ResizeObserver {
    readonly observe = () => undefined;
    readonly unobserve = () => undefined;
    readonly disconnect = () => undefined;
  }

  const requestFrame = (callback: FrameRequestCallback): number => {
    const frameId = nextFrameId++;
    const timer = setTimeout(() => {
      frameTimers.delete(frameId);
      callback(performance.now());
    }, 0);
    frameTimers.set(frameId, timer);
    return frameId;
  };
  const cancelFrame = (frameId: number): void => {
    const timer = frameTimers.get(frameId);
    if (timer === undefined) return;
    clearTimeout(timer);
    frameTimers.delete(frameId);
  };

  replaceProperty(globalThis, "ResizeObserver", { writable: true, value: FixtureResizeObserver });
  replaceProperty(window, "ResizeObserver", { writable: true, value: FixtureResizeObserver });
  replaceProperty(globalThis, "requestAnimationFrame", { writable: true, value: requestFrame });
  replaceProperty(window, "requestAnimationFrame", { writable: true, value: requestFrame });
  replaceProperty(globalThis, "cancelAnimationFrame", { writable: true, value: cancelFrame });
  replaceProperty(window, "cancelAnimationFrame", { writable: true, value: cancelFrame });

  return {
    host,
    restore(): void {
      for (const timer of frameTimers.values()) clearTimeout(timer);
      frameTimers.clear();
      host.remove();
      for (const restore of restorers.reverse()) restore();
    },
  };
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

  it("restores the attachment trigger through the shared focus lease", async () => {
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const request = vi.fn().mockResolvedValue({ attachments: [] });
    const { controller, dialogFocus } = setup(request as HostBridge["request"]);

    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
      trigger,
    });
    await controller.dispatch({ type: "attachment.close" });
    releaseClose(controller, "attachment");

    expect(document.activeElement).toBe(trigger);
    dialogFocus.dispose();
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

  it("ignores a late attachment after-leave from a superseded dialog", async () => {
    const firstTrigger = document.createElement("button");
    const secondTrigger = document.createElement("button");
    document.body.append(firstTrigger, secondTrigger);
    const request = vi.fn().mockResolvedValue({ attachments: [] });
    const { controller, dialogFocus } = setup(request as HostBridge["request"]);

    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
      trigger: firstTrigger,
    });
    await controller.dispatch({ type: "attachment.close" });
    const firstClose = controller.claimCloseLease("attachment");
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-2",
      column: attachmentColumn,
      trigger: secondTrigger,
    });

    firstClose?.release();
    expect(document.activeElement).not.toBe(secondTrigger);

    await controller.dispatch({ type: "attachment.close" });
    releaseClose(controller, "attachment");
    expect(document.activeElement).toBe(secondTrigger);

    dialogFocus.dispose();
    firstTrigger.remove();
    secondTrigger.remove();
  });

  it("binds each queued attachment after-leave to the close that created it", async () => {
    const firstTrigger = document.createElement("button");
    const secondTrigger = document.createElement("button");
    document.body.append(firstTrigger, secondTrigger);
    const secondFocus = vi.spyOn(secondTrigger, "focus");
    const request = vi.fn().mockResolvedValue({ attachments: [] });
    const { controller, dialogFocus, reportError } = setup(request as HostBridge["request"]);
    const modal = createNaiveModalAfterLeaveAdapter({
      claimRelease: () => controller.claimCloseLease("attachment"),
      reportError,
    });

    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
      trigger: firstTrigger,
    });
    await controller.dispatch({ type: "attachment.close" });
    modal.beforeLeave();
    const firstRelease = modal.afterLeave();

    const secondOpen = controller.dispatch({
      type: "attachment.open",
      rowKey: "row-2",
      column: attachmentColumn,
      trigger: secondTrigger,
    });
    const secondClose = controller.dispatch({ type: "attachment.close" });
    modal.beforeLeave();
    await firstRelease;
    expect(secondFocus).not.toHaveBeenCalled();

    await modal.afterLeave();
    expect(secondFocus).toHaveBeenCalledTimes(1);
    expect(reportError).not.toHaveBeenCalled();
    await secondOpen;
    await secondClose;

    dialogFocus.dispose();
    firstTrigger.remove();
    secondTrigger.remove();
  });

  it("binds mutations and native actions to the current authority revision", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      return { status: "applied" };
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    await controller.dispatch({ type: "attachment.download", storedName: "stored" });
    await controller.dispatch({ type: "attachment.remove", storedName: "stored" });

    expect(request).toHaveBeenCalledWith("file.previewRequested", expect.objectContaining({
      recordId: "row-1",
      storedName: "stored",
    }));
    expect(request).toHaveBeenCalledWith("file.downloadRequested", expect.objectContaining({
      originalName: "stored.png",
    }));
    expect(request).toHaveBeenCalledWith("file.removeRequested", expect.objectContaining({
      schemaRevision: "schema-1",
      expectedDigest: `sha256:${"a".repeat(64)}`,
    }));
    expect(controller.state.attachment.files).toEqual([]);
  });

  it("leases a native action so concurrent clicks issue one correlated request", async () => {
    const action = deferred<{ outcome: "opened"; reason: null }>();
    const request = vi.fn((type: string) => {
      if (type === "file.list") return Promise.resolve({ attachments: [file("stored")] });
      return action.promise;
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    const first = controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    const second = controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    expect(request.mock.calls.filter(([type]) => type === "file.previewRequested")).toHaveLength(1);

    action.resolve({ outcome: "opened", reason: null });
    await Promise.all([first, second]);
  });

  it("shows preview unavailability locally without reporting a global error", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      return { outcome: "unavailable", reason: "PREVIEW_HANDLER_UNAVAILABLE" };
    });
    const { controller, reportError } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.preview", storedName: "stored" });

    expect(controller.state.attachment.error).toBe("workspace.attachment.previewUnavailable");
    expect(reportError).not.toHaveBeenCalled();
  });

  it("shows a correlated native action failure only in the attachment dialog", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      throw new BridgeOperationError({
        message: "save failed",
        code: "ATTACHMENT_DOWNLOAD_FAILED",
      });
    });
    const { controller, reportError } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.download", storedName: "stored" });

    expect(controller.state.attachment.error).toBe("workspace.attachment.error.operation");
    expect(reportError).not.toHaveBeenCalled();
  });

  it("rejects a malformed native action terminal payload at the dialog boundary", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      return null;
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.preview", storedName: "stored" });

    expect(controller.state.attachment.error).toBe(
      "workspace.attachment.invalidResponse",
    );
  });

  it("rejects a native action response that exposes an undeclared local path", async () => {
    const request = vi.fn(async (type: string) => {
      if (type === "file.list") return { attachments: [file("stored")] };
      return {
        outcome: "opened",
        reason: null,
        path: "C:\\private\\preview.pdf",
      };
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({
      type: "attachment.open",
      rowKey: "row-1",
      column: attachmentColumn,
    });

    await controller.dispatch({ type: "attachment.preview", storedName: "stored" });

    expect(controller.state.attachment.error).toBe(
      "workspace.attachment.invalidResponse",
    );
  });

  it("suppresses a late native action error after the dialog is reopened", async () => {
    const action = deferred<never>();
    const request = vi.fn((type: string) => {
      if (type === "file.list") return Promise.resolve({ attachments: [file("stored")] });
      return action.promise;
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({ type: "attachment.open", rowKey: "row-1", column: attachmentColumn });
    const pending = controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    await controller.dispatch({ type: "attachment.close" });
    await controller.dispatch({ type: "attachment.open", rowKey: "row-2", column: attachmentColumn });

    action.reject(new BridgeOperationError({
      message: "boom",
      code: "ATTACHMENT_PREVIEW_FAILED",
    }));
    await pending;

    expect(controller.state.attachment.rowKey).toBe("row-2");
    expect(controller.state.attachment.error).toBeNull();
  });

  it("releases old action leases when the dialog closes without clearing a newer lease", async () => {
    const firstAction = deferred<{ outcome: "opened"; reason: null }>();
    const secondAction = deferred<{ outcome: "opened"; reason: null }>();
    let actionCount = 0;
    const request = vi.fn((type: string) => {
      if (type === "file.list") return Promise.resolve({ attachments: [file("stored")] });
      actionCount += 1;
      return actionCount === 1 ? firstAction.promise : secondAction.promise;
    });
    const { controller } = setup(request as HostBridge["request"]);
    await controller.dispatch({ type: "attachment.open", rowKey: "row-1", column: attachmentColumn });
    const first = controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    await controller.dispatch({ type: "attachment.close" });
    await controller.dispatch({ type: "attachment.open", rowKey: "row-2", column: attachmentColumn });
    const second = controller.dispatch({ type: "attachment.preview", storedName: "stored" });
    const duplicate = controller.dispatch({ type: "attachment.preview", storedName: "stored" });

    expect(actionCount).toBe(2);
    firstAction.resolve({ outcome: "opened", reason: null });
    await first;
    expect(actionCount).toBe(2);

    secondAction.resolve({ outcome: "opened", reason: null });
    await Promise.all([second, duplicate]);
    expect(controller.state.attachment.rowKey).toBe("row-2");
    expect(controller.state.attachment.error).toBeNull();
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
    let renderComplete: (() => void) | null = null;
    const grid = {
      getRows: () => [{
        getIndex: () => "row-1",
        getCell: () => ({ getElement: () => trigger }),
      }],
      on: (event: string, handler: () => void) => {
        if (event === "renderComplete") renderComplete = handler;
      },
      off: () => {
        renderComplete = null;
      },
    };
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });
    const nativeFocus = trigger.focus.bind(trigger);
    let focusAttempts = 0;
    vi.spyOn(trigger, "focus").mockImplementation((options?: FocusOptions) => {
      focusAttempts += 1;
      if (focusAttempts > 1) nativeFocus(options);
    });
    const { controller } = setup(vi.fn() as HostBridge["request"], dialogFocus);
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
      releaseClose(controller, "json");
      expect(focusAttempts).toBe(1);

      expect(renderComplete).not.toBeNull();
      (renderComplete as (() => void) | null)?.();
      expect(document.activeElement).toBe(trigger);
    } finally {
      dialogFocus.dispose();
      trigger.remove();
    }
  });

  it("keeps focus on the live structured cell when Tabulator replaces its row after close", async () => {
    const fixture = installRealTabulatorDomFixture();
    const jsonColumn: ColumnSchema = {
      name: "payload",
      title: "Payload",
      dataType: "json",
      editable: true,
      nullable: true,
    };
    const editSchema: readonly ColumnEditSchema[] = [{
      name: "payload",
      storageName: "payload",
      dataType: "json",
      editable: true,
      nullable: true,
      primaryKey: false,
      editor: { kind: "json" },
      validation: [],
    }];
    const page: TablePage = {
      table: "items",
      columns: [jsonColumn],
      rows: [{ rowKey: "row-1", payload: { version: "before" } }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
    };
    const grid = createGrid(fixture.host, page, { editSchema });
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid as unknown as StructuredGridLike,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });
    const currentPayloadCell = (): HTMLElement => {
      const row = (grid.getRows() as unknown as Array<{
        getCell: (field: string) => { getElement: () => HTMLElement } | false;
      }>)[0];
      const cell = row?.getCell("payload");
      if (!cell) throw new Error("real Tabulator payload cell is unavailable");
      return cell.getElement();
    };
    const controller = createStructuredCellDialogController({
      bridge: {
        request: vi.fn() as unknown as HostBridge["request"],
      },
      resolveAttachmentAuthority: () => ({
        tableId: null,
        schemaRevision: null,
        expectedDigest: null,
      }),
      commitJson: vi.fn(),
      dialogFocus,
      translate: key => key,
      reportError: vi.fn(),
    });

    try {
      await vi.waitFor(() => expect(currentPayloadCell().isConnected).toBe(true));
      const trigger = currentPayloadCell();
      trigger.focus();
      expect(document.activeElement).toBe(trigger);

      await controller.dispatch({
        type: "json.open",
        rowKey: "row-1",
        column: jsonColumn,
        value: { version: "before" },
        expectedDigest: null,
        trigger,
      });
      await controller.dispatch({ type: "json.close" });
      releaseClose(controller, "json");
      await nextTick();
      await vi.waitFor(() => expect(document.activeElement).toBe(trigger));

      await grid.setData([{ rowKey: "row-1", payload: { version: "after" } }]);
      const currentCell = currentPayloadCell();
      expect(trigger.isConnected).toBe(false);
      expect(currentCell.isConnected).toBe(true);
      expect(currentCell).not.toBe(trigger);
      expect(document.activeElement).toBe(currentCell);
    } finally {
      dialogFocus.dispose();
      grid.destroy?.();
      fixture.restore();
      document.body.innerHTML = "";
    }
  });
});
