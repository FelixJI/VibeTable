import type { MutationErrorPayload } from "@/contracts";
import { getLocale, setLocale } from "@/i18n";
import { describe, expect, it } from "vitest";
import { WorkspaceV2RequestError } from "./workspaceV2HostAdapter";
import {
  BridgeOperationError,
  BridgeRequestRetiredError,
  BridgeTimeoutError,
} from "@/bridge/hostBridge";
import {
  createNotificationDeduper,
  mutationRejectionMessage,
  relationLookupErrorMessage,
  workspaceV2ErrorMessage,
} from "./notificationPolicy";

describe("notificationPolicy", () => {
  it.each(["mutation_validation", "not_writable"] as const)("review17 preserves the public reason of a definite %s rejection", kind => {
    expect(mutationRejectionMessage({ kind, message: "Referenced records cannot be deleted." })).toBe("Referenced records cannot be deleted.");
  });

  it.each([
    ["zh-CN", "有字段受插件保护，本次更改未保存。"],
    ["en-US", "A field is protected by a plugin. The change was not saved."],
  ] as const)("localizes an authoritative field protection rejection for %s", (locale, expected) => {
    const previous = getLocale();
    try {
      setLocale(locale);
      expect(mutationRejectionMessage({ kind: "not_writable", code: "mutation.field_protected", message: "host locale differs" })).toBe(expected);
    } finally {
      setLocale(previous);
    }
  });

  it("review10 a proven preflight unknown uses safe generic rejection copy", () => {
    const error = { kind: "unknown", notSubmitted: true, message: "private host detail" };
    const message = mutationRejectionMessage(error as MutationErrorPayload);
    expect(message).not.toContain("尚未确认"); expect(message).not.toContain("private host detail");
  });

  it("keeps an unknown write outcome distinct from a confirmed rejection", () => {
    const message = mutationRejectionMessage({ kind: "unknown", message: "private host detail" });
    expect(message).toContain("尚未确认");
    expect(message).toContain("核实");
    expect(message).toContain("不要直接重试");
    expect(message).not.toContain("未保存");
    expect(message).not.toContain("private host detail");
  });

  it("deduplicates the same burst while allowing a later retry", () => {
    let now = 1_000;
    const shouldShow = createNotificationDeduper(3_000, () => now);

    expect(shouldShow("RELATION_LOOKUP_FAILED")).toBe(true);
    now += 200;
    expect(shouldShow("RELATION_LOOKUP_FAILED")).toBe(false);
    now += 3_000;
    expect(shouldShow("RELATION_LOOKUP_FAILED")).toBe(true);
  });

  it("maps relation/Lookup failures without exposing raw host messages", () => {
    const error = new BridgeOperationError({
      code: "RELATION_LOOKUP_FAILED",
      message: "Relation or lookup operation failed.",
    });

    const localized = relationLookupErrorMessage(error);
    expect(localized).not.toContain(error.message);
    expect(localized).toContain("关系");
  });

  it("maps non-fixed direct workspace failures to an actionable mirrored-mode prompt", () => {
    const error = new BridgeOperationError({
      code: "workspace.storage_requires_mirrored",
      message: "raw host storage message",
    });

    const localized = workspaceV2ErrorMessage(error);

    expect(localized).toContain("非固定存储");
    expect(localized).toContain("镜像模式");
    expect(localized).not.toContain(error.message);
  });

  it("explains the shared file-history limits for both request error paths", () => {
    for (const error of [
      new WorkspaceV2RequestError("filehistory.resource_limit", "workspace v2 request failed", false),
      new BridgeOperationError({ code: "filehistory.resource_limit", message: "raw host failure" }),
    ]) {
      const message = workspaceV2ErrorMessage(error);
      expect(message).toContain("10000");
      expect(message).toContain("4096");
      expect(message).toContain("共用");
      expect(message).toContain("不会自动删除");
      expect(message).not.toContain(error.message);
    }
  });
  it("maps timeouts and suppresses cancellations", () => {
    expect(
      relationLookupErrorMessage(
        new BridgeTimeoutError("lookup.query", "request-1", 10_000),
      ),
    ).toContain("超时");
    expect(
      relationLookupErrorMessage(
        new BridgeOperationError({ code: "CANCELLED", message: "cancelled" }),
      ),
    ).toBeNull();
  });

  it("stays silent for explicitly retired lookup requests while real failures stay visible", () => {
    // A locally retired in-flight lookup.query belongs to a workspace the
    // renderer already left; it is not a user-facing failure.
    expect(relationLookupErrorMessage(new BridgeRequestRetiredError())).toBeNull();

    // Genuine current-epoch bridge failures must keep their actionable copy.
    expect(
      relationLookupErrorMessage(
        new BridgeOperationError({
          code: "RELATION_LOOKUP_FAILED",
          message: "private host detail",
        }),
      ),
    ).toContain("关系");
    expect(
      relationLookupErrorMessage(
        new BridgeOperationError({
          code: "WORKSPACE_ERROR",
          message: "private host detail",
        }),
      ),
    ).not.toBeNull();
  });
});
