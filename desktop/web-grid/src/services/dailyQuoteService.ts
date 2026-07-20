import type { DailyQuoteSource, DailyQuoteStyle } from "@/stores/uiStore";

export type DailyQuoteOrigin = "online" | "cache" | "builtin";

export interface DailyQuote {
  readonly text: string;
  readonly attribution: string;
  readonly url: string;
  readonly origin: DailyQuoteOrigin;
}

interface HitokotoPayload {
  readonly hitokoto?: unknown;
  readonly from?: unknown;
  readonly from_who?: unknown;
  readonly uuid?: unknown;
}

interface JinrishiciPayload {
  readonly status?: unknown;
  readonly data?: {
    readonly content?: unknown;
    readonly origin?: {
      readonly title?: unknown;
      readonly dynasty?: unknown;
      readonly author?: unknown;
    };
  };
}

interface QuotablePayload {
  readonly _id?: unknown;
  readonly content?: unknown;
  readonly author?: unknown;
}

interface DailyQuoteCache {
  readonly source: DailyQuoteSource;
  readonly style: DailyQuoteStyle;
  readonly quote: Omit<DailyQuote, "origin">;
}

export const DAILY_QUOTE_STORAGE_KEY = "vt:daily-quote:v2";
export const JINRISHICI_TOKEN_STORAGE_KEY = "vt:jinrishici-token:v1";
export const HITOKOTO_ENDPOINT = "https://v1.hitokoto.cn/";
export const JINRISHICI_TOKEN_ENDPOINT = "https://v2.jinrishici.com/token";
export const JINRISHICI_SENTENCE_ENDPOINT = "https://v2.jinrishici.com/sentence";
export const QUOTABLE_ENDPOINT = "https://api.quotable.io/quotes/random";

const HITOKOTO_CATEGORIES: Readonly<Record<DailyQuoteStyle, readonly string[]>> = {
  mixed: ["d", "e", "i", "k"],
  inspiring: ["e", "f", "k"],
  literary: ["d"],
  philosophy: ["k"],
  poetry: ["i"],
  lighthearted: ["l"],
};

const QUOTABLE_TAGS: Partial<Record<DailyQuoteStyle, string>> = {
  inspiring: "inspirational|success",
  philosophy: "philosophy|wisdom",
};

function cleanText(value: unknown, maxLength: number): string {
  if (typeof value !== "string") return "";
  return value.replace(/\s+/g, " ").trim().slice(0, maxLength);
}

function hitokotoUrl(style: DailyQuoteStyle): string {
  const params = new URLSearchParams({ encode: "json", charset: "utf-8", max_length: "64" });
  for (const category of HITOKOTO_CATEGORIES[style]) params.append("c", category);
  return `${HITOKOTO_ENDPOINT}?${params.toString()}`;
}

function quotableUrl(style: DailyQuoteStyle): string {
  const params = new URLSearchParams({ limit: "1", maxLength: "96" });
  const tags = QUOTABLE_TAGS[style];
  if (tags) params.set("tags", tags);
  return `${QUOTABLE_ENDPOINT}?${params.toString()}`;
}

function readCache(storage: Storage, source: DailyQuoteSource, style: DailyQuoteStyle): DailyQuote | null {
  try {
    const parsed = JSON.parse(storage.getItem(DAILY_QUOTE_STORAGE_KEY) ?? "null") as DailyQuoteCache | null;
    const candidate = parsed?.quote;
    if (
      parsed?.source !== source
      || parsed?.style !== style
      || !candidate
      || typeof candidate.text !== "string"
      || candidate.text.length < 2
      || candidate.text.length > 140
      || typeof candidate.attribution !== "string"
      || typeof candidate.url !== "string"
    ) return null;
    return { ...candidate, origin: "cache" };
  } catch {
    return null;
  }
}

function writeCache(storage: Storage, source: DailyQuoteSource, style: DailyQuoteStyle, quote: DailyQuote): void {
  try {
    const { text, attribution, url } = quote;
    storage.setItem(DAILY_QUOTE_STORAGE_KEY, JSON.stringify({
      source,
      style,
      quote: { text, attribution, url },
    } satisfies DailyQuoteCache));
  } catch {
    // An unavailable cache must never prevent the quote from rendering.
  }
}

function fromHitokoto(payload: HitokotoPayload): DailyQuote | null {
  const text = cleanText(payload.hitokoto, 96);
  if (text.length < 2) return null;
  const author = cleanText(payload.from_who, 40);
  const source = cleanText(payload.from, 60);
  const attribution = [author, source].filter(Boolean).join(" · ");
  const uuid = typeof payload.uuid === "string" && /^[0-9a-f-]{8,40}$/i.test(payload.uuid)
    ? payload.uuid
    : "";
  return {
    text,
    attribution,
    url: uuid ? `https://hitokoto.cn/?uuid=${encodeURIComponent(uuid)}` : "https://hitokoto.cn",
    origin: "online",
  };
}

function fromJinrishici(payload: JinrishiciPayload): DailyQuote | null {
  if (payload.status !== "success") return null;
  const text = cleanText(payload.data?.content, 96);
  if (text.length < 2) return null;
  const origin = payload.data?.origin;
  const attribution = [
    cleanText(origin?.dynasty, 16),
    cleanText(origin?.author, 32),
    cleanText(origin?.title, 48),
  ].filter(Boolean).join(" · ");
  return {
    text,
    attribution,
    url: "https://www.jinrishici.com/",
    origin: "online",
  };
}

function fromQuotable(payload: QuotablePayload | readonly QuotablePayload[]): DailyQuote | null {
  const item = Array.isArray(payload) ? payload[0] : payload;
  const text = cleanText(item?.content, 140);
  if (text.length < 2) return null;
  const id = typeof item?._id === "string" && /^[\w-]{4,64}$/.test(item._id) ? item._id : "";
  return {
    text,
    attribution: cleanText(item?.author, 60),
    url: id ? `https://quotable.io/quotes/${encodeURIComponent(id)}` : "https://quotable.io/",
    origin: "online",
  };
}

function readToken(storage: Storage): string {
  try {
    const token = storage.getItem(JINRISHICI_TOKEN_STORAGE_KEY) ?? "";
    return /^[\w/+\-=]{20,200}$/.test(token) ? token : "";
  } catch {
    return "";
  }
}

function writeToken(storage: Storage, token: string): void {
  try {
    storage.setItem(JINRISHICI_TOKEN_STORAGE_KEY, token);
  } catch {
    // The request can still continue for this session when storage is unavailable.
  }
}

export interface LoadDailyQuoteOptions {
  readonly fallback: DailyQuote;
  readonly locale: "zh-CN" | "en-US";
  readonly source: DailyQuoteSource;
  readonly style: DailyQuoteStyle;
  readonly timeoutMs?: number;
  readonly fetchImpl?: typeof fetch;
  readonly storage?: Storage;
}

export async function loadDailyQuote(options: LoadDailyQuoteOptions): Promise<DailyQuote> {
  if (options.source === "builtin") return options.fallback;
  const storage = options.storage ?? localStorage;
  const cached = readCache(storage, options.source, options.style);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), options.timeoutMs ?? 3000);

  try {
    const fetchImpl = options.fetchImpl ?? fetch;
    let quote: DailyQuote | null = null;

    if (options.source === "jinrishici") {
      let token = readToken(storage);
      if (!token) {
        const tokenResponse = await fetchImpl(JINRISHICI_TOKEN_ENDPOINT, {
          headers: { Accept: "application/json" },
          signal: controller.signal,
          cache: "no-store",
        });
        if (!tokenResponse.ok) return cached ?? options.fallback;
        const tokenPayload = await tokenResponse.json() as { data?: unknown };
        token = cleanText(tokenPayload.data, 200);
        if (!/^[\w/+\-=]{20,200}$/.test(token)) return cached ?? options.fallback;
        writeToken(storage, token);
      }
      const response = await fetchImpl(JINRISHICI_SENTENCE_ENDPOINT, {
        headers: { Accept: "application/json", "X-User-Token": token },
        signal: controller.signal,
        cache: "no-store",
      });
      if (!response.ok) return cached ?? options.fallback;
      quote = fromJinrishici(await response.json() as JinrishiciPayload);
    } else {
      const endpoint = options.source === "quotable"
        ? quotableUrl(options.style)
        : hitokotoUrl(options.style);
      const response = await fetchImpl(endpoint, {
        headers: { Accept: "application/json" },
        signal: controller.signal,
        cache: "no-store",
      });
      if (!response.ok) return cached ?? options.fallback;
      const payload = await response.json() as HitokotoPayload | QuotablePayload | readonly QuotablePayload[];
      quote = options.source === "quotable"
        ? fromQuotable(payload as QuotablePayload | readonly QuotablePayload[])
        : fromHitokoto(payload as HitokotoPayload);
    }

    if (!quote) return cached ?? options.fallback;
    writeCache(storage, options.source, options.style, quote);
    return quote;
  } catch {
    return cached ?? options.fallback;
  } finally {
    clearTimeout(timer);
  }
}
