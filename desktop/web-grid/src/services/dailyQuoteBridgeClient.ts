import type { HostBridge } from "@/bridge/hostBridge";
import type {
  DailyQuoteFetchRequest,
  DailyQuoteFetchResult,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";

export interface DailyQuoteBridgeClient {
  fetch(request: DailyQuoteFetchRequest): Promise<DailyQuoteFetchResult>;
}

export function createDailyQuoteBridgeClient(
  bridge: HostBridge = useHostBridge(),
): DailyQuoteBridgeClient {
  return {
    async fetch(request): Promise<DailyQuoteFetchResult> {
      const payload = await bridge.request("dailyQuote.fetch", request);
      if (!isDailyQuoteFetchResult(payload)) {
        throw new Error("Host returned an invalid daily quote response.");
      }
      return payload;
    },
  };
}

function isDailyQuoteFetchResult(
  value: unknown,
): value is DailyQuoteFetchResult {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Partial<DailyQuoteFetchResult>;
  return (
    typeof candidate.text === "string"
    && candidate.text.length >= 2
    && candidate.text.length <= 140
    && typeof candidate.attribution === "string"
    && candidate.attribution.length <= 120
    && typeof candidate.url === "string"
    && candidate.url.length <= 256
    && /^https:\/\//.test(candidate.url)
  );
}
