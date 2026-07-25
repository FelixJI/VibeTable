import { describe, expect, it } from "vitest";
import {
  BridgeOperationError,
  BridgeTimeoutError,
} from "@/bridge/hostBridge";
import {
  createNotificationDeduper,
  relationLookupErrorMessage,
} from "./notificationPolicy";

describe("notificationPolicy", () => {
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
});
