import type {
  DailyQuoteFetchRequest,
  DailyQuoteFetchResult,
} from "@/contracts";
import type { DailyQuoteSource, DailyQuoteStyle } from "@/stores/uiStore";
import {
  createDailyQuoteBridgeClient,
  type DailyQuoteBridgeClient,
} from "./dailyQuoteBridgeClient";

export type DailyQuoteOrigin = "online" | "cache" | "builtin";

export interface DailyQuote {
  readonly text: string;
  readonly attribution: string;
  readonly url: string;
  readonly origin: DailyQuoteOrigin;
}

interface DailyQuoteCache {
  readonly source: DailyQuoteSource;
  readonly style: DailyQuoteStyle;
  readonly quote: Omit<DailyQuote, "origin">;
}

export const DAILY_QUOTE_STORAGE_KEY = "vt:daily-quote:v2";

function readCache(
  storage: Storage,
  source: DailyQuoteSource,
  style: DailyQuoteStyle,
): DailyQuote | null {
  try {
    const parsed = JSON.parse(
      storage.getItem(DAILY_QUOTE_STORAGE_KEY) ?? "null",
    ) as DailyQuoteCache | null;
    const candidate = parsed?.quote;
    if (
      parsed?.source !== source
      || parsed.style !== style
      || !candidate
      || typeof candidate.text !== "string"
      || candidate.text.length < 2
      || candidate.text.length > 140
      || typeof candidate.attribution !== "string"
      || typeof candidate.url !== "string"
    ) {
      return null;
    }
    return { ...candidate, origin: "cache" };
  } catch {
    return null;
  }
}

function writeCache(
  storage: Storage,
  source: DailyQuoteSource,
  style: DailyQuoteStyle,
  quote: DailyQuote,
): void {
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

export interface LoadDailyQuoteOptions {
  readonly fallback: DailyQuote;
  readonly locale: DailyQuoteFetchRequest["locale"];
  readonly source: DailyQuoteSource;
  readonly style: DailyQuoteStyle;
  readonly bridgeClient?: DailyQuoteBridgeClient;
  readonly storage?: Storage;
}

export async function loadDailyQuote(
  options: LoadDailyQuoteOptions,
): Promise<DailyQuote> {
  // Scenario01 and offline users remain network-closed: builtin must return
  // before a bridge client is created or any host request is posted.
  if (options.source === "builtin") return options.fallback;

  const storage = options.storage ?? localStorage;
  const cached = readCache(storage, options.source, options.style);
  const request: DailyQuoteFetchRequest = {
    provider: options.source,
    style: options.style,
    locale: options.locale,
  };
  try {
    const client = options.bridgeClient ?? createDailyQuoteBridgeClient();
    const result: DailyQuoteFetchResult = await client.fetch(request);
    const quote: DailyQuote = { ...result, origin: "online" };
    writeCache(storage, options.source, options.style, quote);
    return quote;
  } catch {
    return cached ?? options.fallback;
  }
}
