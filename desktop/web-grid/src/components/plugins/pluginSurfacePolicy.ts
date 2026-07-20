import type { PluginSurfaceEventPayload } from "@/contracts";

export const PLUGIN_SURFACE_CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-ancestors https://app.vibetable.local",
].join("; ");

export function validatePluginSurfaceUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:"
      && /^[a-f0-9]{32}\.[a-f0-9]{32}\.plugins\.vibetable\.local$/.test(url.hostname)
      && url.username === ""
      && url.password === "";
  } catch {
    return false;
  }
}

interface SurfaceMessageCandidate {
  readonly origin: string;
  readonly source: MessageEventSource | null;
  readonly data: unknown;
}

interface SurfaceMessageExpectation {
  readonly expectedOrigin: string;
  readonly expectedSource: MessageEventSource | null;
  readonly surfaceToken: string;
}

export function isTrustedSurfaceMessage(
  event: SurfaceMessageCandidate,
  expected: SurfaceMessageExpectation,
): event is SurfaceMessageCandidate & { readonly data: PluginSurfaceEventPayload } {
  if (event.origin !== expected.expectedOrigin || event.source !== expected.expectedSource) return false;
  if (typeof event.data !== "object" || event.data === null || Array.isArray(event.data)) return false;
  const data = event.data as Partial<PluginSurfaceEventPayload>;
  return data.contract === "vibetable.plugin-surface.v1"
    && data.surfaceToken === expected.surfaceToken
    && (data.event === "ready" || data.event === "close" || data.event === "action")
    && typeof data.payload === "object"
    && data.payload !== null
    && !Array.isArray(data.payload);
}
