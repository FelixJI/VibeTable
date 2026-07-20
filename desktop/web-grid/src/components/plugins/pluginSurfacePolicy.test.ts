import { describe, expect, it } from "vitest";
import { PLUGIN_SURFACE_CSP, isTrustedSurfaceMessage, validatePluginSurfaceUrl } from "./pluginSurfacePolicy";

const revision = `${"a".repeat(32)}.${"b".repeat(32)}`;

describe("plugin surface policy", () => {
  it("accepts only immutable package origins and requires a no-network CSP", () => {
    expect(validatePluginSurfaceUrl(`https://${revision}.plugins.vibetable.local/index.html`)).toBe(true);
    expect(validatePluginSurfaceUrl(`https://${"a".repeat(64)}.plugins.vibetable.local/index.html`)).toBe(false);
    expect(validatePluginSurfaceUrl("https://plugins.vibetable.local/index.html")).toBe(false);
    expect(validatePluginSurfaceUrl(`http://${revision}.plugins.vibetable.local/index.html`)).toBe(false);
    expect(validatePluginSurfaceUrl(`https://${revision}.plugins.vibetable.local.evil.test/index.html`)).toBe(false);
    expect(PLUGIN_SURFACE_CSP).toContain("default-src 'none'");
    expect(PLUGIN_SURFACE_CSP).toContain("connect-src 'none'");
  });

  it("rejects messages unless origin, window, contract and token all match", () => {
    const source = {} as MessageEventSource;
    const data = {
      contract: "vibetable.plugin-surface.v1",
      surfaceToken: "surface-1",
      event: "ready",
      payload: {},
    };
    expect(isTrustedSurfaceMessage({ origin: `https://${revision}.plugins.vibetable.local`, source, data }, {
      expectedOrigin: `https://${revision}.plugins.vibetable.local`,
      expectedSource: source,
      surfaceToken: "surface-1",
    })).toBe(true);
    expect(isTrustedSurfaceMessage({ origin: "https://evil.test", source, data }, {
      expectedOrigin: `https://${revision}.plugins.vibetable.local`, expectedSource: source, surfaceToken: "surface-1",
    })).toBe(false);
    expect(isTrustedSurfaceMessage({ origin: `https://${revision}.plugins.vibetable.local`, source: {} as MessageEventSource, data }, {
      expectedOrigin: `https://${revision}.plugins.vibetable.local`, expectedSource: source, surfaceToken: "surface-1",
    })).toBe(false);
    expect(isTrustedSurfaceMessage({ origin: `https://${revision}.plugins.vibetable.local`, source, data: { ...data, surfaceToken: "stale" } }, {
      expectedOrigin: `https://${revision}.plugins.vibetable.local`, expectedSource: source, surfaceToken: "surface-1",
    })).toBe(false);
  });
});
