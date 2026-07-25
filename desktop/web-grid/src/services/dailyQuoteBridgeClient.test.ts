import { describe, expect, it, vi } from "vitest";
import type { HostBridge } from "@/bridge/hostBridge";
import { createDailyQuoteBridgeClient } from "./dailyQuoteBridgeClient";

describe("dailyQuoteBridgeClient", () => {
  it("posts only the fixed typed RPC with provider, style and locale", async () => {
    const request = vi.fn(async () => ({
      text: "A safe quote.",
      attribution: "Author",
      url: "https://quotable.io/quotes/safe",
    }));
    const client = createDailyQuoteBridgeClient({
      request,
    } as unknown as HostBridge);

    await expect(client.fetch({
      provider: "quotable",
      style: "philosophy",
      locale: "en-US",
    })).resolves.toMatchObject({ text: "A safe quote." });
    expect(request).toHaveBeenCalledWith("dailyQuote.fetch", {
      provider: "quotable",
      style: "philosophy",
      locale: "en-US",
    });
  });

  it("rejects malformed host output", async () => {
    const client = createDailyQuoteBridgeClient({
      request: vi.fn(async () => ({
        text: "<script>",
        attribution: "",
        url: "javascript:alert(1)",
      })),
    } as unknown as HostBridge);

    await expect(client.fetch({
      provider: "hitokoto",
      style: "mixed",
      locale: "zh-CN",
    })).rejects.toThrow(/invalid daily quote/i);
  });
});
