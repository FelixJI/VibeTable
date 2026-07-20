import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DAILY_QUOTE_STORAGE_KEY,
  JINRISHICI_SENTENCE_ENDPOINT,
  JINRISHICI_TOKEN_ENDPOINT,
  JINRISHICI_TOKEN_STORAGE_KEY,
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

  it("requests a fresh Hitokoto on every load and keeps the latest as an offline fallback", async () => {
    let request = 0;
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
      hitokoto: request++ === 0 ? "山高自有客行路。" : "水深自有渡船人。",
      from: "一言",
      from_who: "佚名",
      uuid: "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2",
    }), { status: 200 }));
    const fetchImpl = fetchMock as unknown as typeof fetch;

    const options = { fallback, locale: "zh-CN" as const, source: "hitokoto" as const, style: "poetry" as const };
    const first = await loadDailyQuote({ ...options, fetchImpl });
    const second = await loadDailyQuote({ ...options, fetchImpl });

    expect(first).toMatchObject({ text: "山高自有客行路。", origin: "online" });
    expect(second).toMatchObject({ text: "水深自有渡船人。", origin: "online" });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[0][0])).toContain("c=i");
    expect(localStorage.getItem(DAILY_QUOTE_STORAGE_KEY)).toContain("水深自有渡船人");

    const failed = vi.fn(async () => { throw new Error("offline"); }) as typeof fetch;
    expect(await loadDailyQuote({ ...options, fetchImpl: failed })).toMatchObject({
      text: "水深自有渡船人。",
      origin: "cache",
    });
  });

  it("persists and reuses the Jinrishici client token", async () => {
    const token = "abcdefghijklmnopqrstuvwx123456";
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      if (String(input) === JINRISHICI_TOKEN_ENDPOINT) {
        return new Response(JSON.stringify({ status: "success", data: token }), { status: 200 });
      }
      return new Response(JSON.stringify({
        status: "success",
        data: {
          content: "君问归期未有期，巴山夜雨涨秋池。",
          origin: { dynasty: "唐代", author: "李商隐", title: "夜雨寄北" },
        },
      }), { status: 200 });
    });
    const fetchImpl = fetchMock as unknown as typeof fetch;
    const options = { fallback, locale: "zh-CN" as const, source: "jinrishici" as const, style: "poetry" as const };

    const first = await loadDailyQuote({ ...options, fetchImpl });
    const second = await loadDailyQuote({ ...options, fetchImpl });

    expect(first).toMatchObject({
      text: "君问归期未有期，巴山夜雨涨秋池。",
      attribution: "唐代 · 李商隐 · 夜雨寄北",
    });
    expect(second.origin).toBe("online");
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      JINRISHICI_TOKEN_ENDPOINT,
      JINRISHICI_SENTENCE_ENDPOINT,
      JINRISHICI_SENTENCE_ENDPOINT,
    ]);
    expect(localStorage.getItem(JINRISHICI_TOKEN_STORAGE_KEY)).toBe(token);
    expect(fetchMock.mock.calls[1][1]?.headers).toMatchObject({ "X-User-Token": token });
  });

  it("loads Quotable with style tags for international quotes", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify([{
      _id: "quote-1234",
      content: "The unexamined life is not worth living.",
      author: "Socrates",
    }]), { status: 200 }));
    const fetchImpl = fetchMock as unknown as typeof fetch;

    const quote = await loadDailyQuote({
      fallback,
      locale: "en-US",
      source: "quotable",
      style: "philosophy",
      fetchImpl,
    });

    expect(quote).toMatchObject({ text: "The unexamined life is not worth living.", attribution: "Socrates" });
    expect(String(fetchMock.mock.calls[0][0])).toContain("tags=philosophy%7Cwisdom");
  });

  it("uses built-in content without making a network request", async () => {
    const fetchImpl = vi.fn() as typeof fetch;
    expect(await loadDailyQuote({
      fallback,
      locale: "zh-CN",
      source: "builtin",
      style: "mixed",
      fetchImpl,
    })).toEqual(fallback);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
