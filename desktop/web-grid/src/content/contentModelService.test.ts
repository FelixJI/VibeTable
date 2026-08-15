import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createHostBridge,
  type HostBridge,
  type WebViewLike,
} from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { useContentModelService } from "./contentModelService";

describe("contentModelService", () => {
  beforeEach(() => vi.stubGlobal("crypto", { randomUUID: () => "operation-1" }));

  it("maps profile not-found to unconfigured and keeps writes closed", async () => {
    const request = vi.fn()
      .mockResolvedValueOnce({
        error: { code: "content_profile.not_found", message: "missing" },
      })
      .mockImplementationOnce(async (_type, payload) => ({
        profile: payload.profile,
        revision: "revision-1",
      }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useContentModelService();
    await expect(service.loadProfile("articles")).resolves.toBeNull();

    await expect(service.commitProfile({
      contractVersion: "1.0",
      tableId: "articles",
      titleFieldId: "title",
      bodyFieldId: "body",
      summaryFieldId: null,
      searchableFieldIds: ["title", "body"],
    }, null)).resolves.toMatchObject({ revision: "revision-1" });
    expect(request).toHaveBeenLastCalledWith("contentProfile.commit", {
      profile: expect.objectContaining({ tableId: "articles" }),
      expectedRevision: null,
      idempotencyKey: "operation-1",
    });
  });

  it("commits all content field changes as one digest-guarded row update", async () => {
    const receipt = {
      contractVersion: "2.0",
      status: "applied",
      changeSetId: "change-1",
      affectedRows: [],
      computedFields: {},
      newRevision: "data_0002",
      emittedEvents: [],
      warnings: [],
    } as const;
    const request = vi.fn().mockResolvedValue(receipt);
    setHostBridgeForTesting({ request } as unknown as HostBridge);

    await expect(useContentModelService().commitRecord({
      tableId: "articles",
      schemaRevision: "schema_0002",
      recordId: "record-1",
      values: { title: "Edited", body: "Durable violet body" },
      expectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    })).resolves.toBe(receipt);

    expect(request).toHaveBeenCalledOnce();
    expect(request).toHaveBeenCalledWith("mutation.apply", {
      contractVersion: "2.0",
      requestId: "operation-1",
      idempotencyKey: "operation-1",
      tableId: "articles",
      schemaRevision: "schema_0002",
      operations: [{
        kind: "update",
        recordId: "record-1",
        values: { title: "Edited", body: "Durable violet body" },
      }],
      actor: { type: "user", id: "local", displayName: null },
      expectedRevision: null,
      expectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    });
  });

  it("serializes a sidecar-valid actor through the real HostBridge request envelope", async () => {
    const posted: unknown[] = [];
    const listeners: Array<(event: { readonly data: unknown }) => void> = [];
    const webview: WebViewLike = {
      postMessage(message) {
        posted.push(message);
        const envelope = message as { type: string; requestId: string };
        queueMicrotask(() => {
          for (const listener of listeners) {
            listener({
              data: {
                type: envelope.type,
                requestId: envelope.requestId,
                payload: {
                  contractVersion: "2.0",
                  status: "applied",
                  changeSetId: "change-1",
                  affectedRows: [],
                  computedFields: {},
                  newRevision: "data_0002",
                  emittedEvents: [],
                  warnings: [],
                },
              },
            });
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
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1_000,
      generateRequestId: () => "bridge-request-1",
    });
    bridge.start();
    setHostBridgeForTesting(bridge);

    await useContentModelService().commitRecord({
      tableId: "articles",
      schemaRevision: "schema_0002",
      recordId: "record-1",
      values: { title: "Edited", body: "Durable violet body" },
      expectedDigest: null,
    });

    expect(posted).toEqual([expect.objectContaining({
      type: "mutation.apply",
      requestId: "bridge-request-1",
      payload: expect.objectContaining({
        actor: { type: "user", id: "local", displayName: null },
      }),
    })]);
    bridge.stop();
  });
});
