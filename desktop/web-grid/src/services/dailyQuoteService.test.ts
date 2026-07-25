import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DailyQuoteBridgeClient } from "./dailyQuoteBridgeClient";
import {
  DAILY_QUOTE_STORAGE_KEY,
  loadDailyQuote,
  type DailyQuote,
} from "./dailyQuoteService";

const fallback: DailyQuote = {
  text: "Built-in quote",
  attribution: "",
  url: "",
  origin: "builtin",
};

function clientReturning(
  text: string,
): DailyQuoteBridgeClient {
  return {
    fetch: vi.fn(async () => ({
      text,
      attribution: "VibeTable",
      url: "https://example.invalid/quote",
    })),
  };
}

describe("dailyQuoteService", () => {
  beforeEach(() => localStorage.clear());

  it("requests online sources through the typed bridge client and caches the latest result", async () => {
    const bridgeClient = clientReturning("Keep moving.");
    const options = {
      fallback,
      locale: "en-US" as const,
      source: "hitokoto" as const,
      style: "inspiring" as const,
      bridgeClient,
    };

    await expect(loadDailyQuote(options)).resolves.toMatchObject({
      text: "Keep moving.",
      origin: "online",
    });
    expect(bridgeClient.fetch).toHaveBeenCalledWith({
      provider: "hitokoto",
      style: "inspiring",
      locale: "en-US",
    });
    expect(localStorage.getItem(DAILY_QUOTE_STORAGE_KEY)).toContain("Keep moving.");
  });

  it("uses a provider/style cache when the host request fails", async () => {
    const options = {
      fallback,
      locale: "zh-CN" as const,
      source: "jinrishici" as const,
      style: "poetry" as const,
    };
    await loadDailyQuote({
      ...options,
      bridgeClient: clientReturning("山高水长。"),
    });
    const failed: DailyQuoteBridgeClient = {
      fetch: vi.fn(async () => {
        throw new Error("offline");
      }),
    };

    await expect(loadDailyQuote({
      ...options,
      bridgeClient: failed,
    })).resolves.toMatchObject({
      text: "山高水长。",
      origin: "cache",
    });
  });

  it("falls back to built-in content when no provider cache exists", async () => {
    const failed: DailyQuoteBridgeClient = {
      fetch: vi.fn(async () => {
        throw new Error("provider unavailable");
      }),
    };
    await expect(loadDailyQuote({
      fallback,
      locale: "en-US",
      source: "quotable",
      style: "philosophy",
      bridgeClient: failed,
    })).resolves.toEqual(fallback);
  });

  it("uses built-in content without creating or calling a bridge client", async () => {
    const bridgeClient = clientReturning("must not be used");
    await expect(loadDailyQuote({
      fallback,
      locale: "zh-CN",
      source: "builtin",
      style: "mixed",
      bridgeClient,
    })).resolves.toEqual(fallback);
    expect(bridgeClient.fetch).not.toHaveBeenCalled();
  });
});
