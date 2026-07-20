import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DAILY_QUOTE_STORAGE_KEY,
  loadDailyQuote,
  type DailyQuote,
} from "./dailyQuoteService";

const fallback: DailyQuote = {
  text: "内置短句",
  attribution: "",
  url: "",
  origin: "builtin",
};

describe("dailyQuoteService", () => {
  beforeEach(() => localStorage.clear());

  it("fetches, validates and caches one quote per local day", async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({
      hitokoto: "山高自有客行路。",
      from: "一言",
      from_who: "佚名",
      uuid: "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2",
    }), { status: 200 })) as typeof fetch;

    const first = await loadDailyQuote({
      fallback, locale: "zh-CN", now: new Date(2026, 6, 20), fetchImpl,
    });
    const second = await loadDailyQuote({
      fallback, locale: "zh-CN", now: new Date(2026, 6, 20), fetchImpl,
    });

    expect(first).toMatchObject({ text: "山高自有客行路。", origin: "online" });
    expect(second.origin).toBe("cache");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(DAILY_QUOTE_STORAGE_KEY)).toContain("山高自有客行路");
  });

  it("falls back on network and payload failures", async () => {
    const failed = vi.fn(async () => { throw new Error("offline"); }) as typeof fetch;
    expect(await loadDailyQuote({ fallback, locale: "zh-CN", fetchImpl: failed })).toEqual(fallback);

    const invalid = vi.fn(async () => new Response(JSON.stringify({ hitokoto: "" }), { status: 200 })) as typeof fetch;
    expect(await loadDailyQuote({ fallback, locale: "zh-CN", fetchImpl: invalid })).toEqual(fallback);
  });

  it("keeps the localized built-in sentence for English UI", async () => {
    const fetchImpl = vi.fn() as typeof fetch;
    expect(await loadDailyQuote({ fallback, locale: "en-US", fetchImpl })).toEqual(fallback);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
