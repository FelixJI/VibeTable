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

interface DailyQuoteCache {
  readonly date: string;
  readonly quote: Omit<DailyQuote, "origin">;
}

export const DAILY_QUOTE_STORAGE_KEY = "vt:daily-quote:v1";
export const DAILY_QUOTE_ENDPOINT = "https://v1.hitokoto.cn/?c=d&c=e&c=i&c=k&encode=json&charset=utf-8&max_length=48";

function dateKey(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function cleanText(value: unknown, maxLength: number): string {
  if (typeof value !== "string") return "";
  return value.replace(/\s+/g, " ").trim().slice(0, maxLength);
}

function readCache(storage: Storage, today: string): DailyQuote | null {
  try {
    const parsed = JSON.parse(storage.getItem(DAILY_QUOTE_STORAGE_KEY) ?? "null") as DailyQuoteCache | null;
    const candidate = parsed?.quote;
    if (
      parsed?.date !== today
      || !candidate
      || typeof candidate.text !== "string"
      || candidate.text.length < 2
      || candidate.text.length > 80
      || typeof candidate.attribution !== "string"
      || typeof candidate.url !== "string"
    ) return null;
    return {
      text: candidate.text,
      attribution: candidate.attribution,
      url: candidate.url,
      origin: "cache",
    };
  } catch {
    return null;
  }
}

function writeCache(storage: Storage, today: string, quote: DailyQuote): void {
  try {
    const { text, attribution, url } = quote;
    storage.setItem(DAILY_QUOTE_STORAGE_KEY, JSON.stringify({
      date: today,
      quote: { text, attribution, url },
    } satisfies DailyQuoteCache));
  } catch {
    // An unavailable cache must never prevent the quote from rendering.
  }
}

function fromHitokoto(payload: HitokotoPayload): DailyQuote | null {
  const text = cleanText(payload.hitokoto, 80);
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

export interface LoadDailyQuoteOptions {
  readonly fallback: DailyQuote;
  readonly locale: "zh-CN" | "en-US";
  readonly now?: Date;
  readonly timeoutMs?: number;
  readonly fetchImpl?: typeof fetch;
  readonly storage?: Storage;
}

export async function loadDailyQuote(options: LoadDailyQuoteOptions): Promise<DailyQuote> {
  if (options.locale !== "zh-CN") return options.fallback;
  const now = options.now ?? new Date();
  const today = dateKey(now);
  const storage = options.storage ?? localStorage;
  const cached = readCache(storage, today);
  if (cached) return cached;

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), options.timeoutMs ?? 2500);
  try {
    const response = await (options.fetchImpl ?? fetch)(DAILY_QUOTE_ENDPOINT, {
      headers: { Accept: "application/json" },
      signal: controller.signal,
      cache: "no-store",
    });
    if (!response.ok) return options.fallback;
    const quote = fromHitokoto(await response.json() as HitokotoPayload);
    if (!quote) return options.fallback;
    writeCache(storage, today, quote);
    return quote;
  } catch {
    return options.fallback;
  } finally {
    clearTimeout(timer);
  }
}
