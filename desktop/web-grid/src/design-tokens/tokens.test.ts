import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const sourceRoot = join(process.cwd(), "src");
const tokenCatalog = readFileSync(join(sourceRoot, "design-tokens", "tokens.css"), "utf8");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = `${directory}/${entry.name}`;
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(css|ts|vue)$/.test(entry.name) ? [path] : [];
  });
}

describe("design token catalog", () => {
  it("defines every --vt-* token referenced by application styles", () => {
    const defined = new Set(
      [...tokenCatalog.matchAll(/(--vt-[a-zA-Z0-9-]+)\s*:/g)]
        .map((match) => match[1]),
    );
    const used = new Set(
      sourceFiles(sourceRoot).flatMap((path) =>
        [...readFileSync(path, "utf8").matchAll(/var\((--vt-[a-zA-Z0-9-]+)/g)]
          .map((match) => match[1]),
      ),
    );
    const missing = [...used].filter((token) => !defined.has(token)).sort();
    expect(missing).toEqual([]);
  });

  it("keeps the dark color scheme authoritative and provides readable accent text", () => {
    const lightScheme = tokenCatalog.indexOf("color-scheme: light");
    const darkScope = tokenCatalog.indexOf(":root.dark {");
    const darkScheme = tokenCatalog.indexOf("color-scheme: dark");
    expect(lightScheme).toBeGreaterThanOrEqual(0);
    expect(lightScheme).toBeLessThan(darkScope);
    expect(darkScheme).toBeGreaterThan(darkScope);
    expect(tokenCatalog).toContain("--vt-fg-accent: var(--vt-color-primary-300)");
  });

  it("uses the readable semantic accent foreground for compact UI copy", () => {
    const compactCopyFiles = [
      join(sourceRoot, "views", "FileWorkspaceView.vue"),
      join(sourceRoot, "components", "dashboard", "DashboardSidebar.vue"),
    ];
    for (const path of compactCopyFiles) {
      const source = readFileSync(path, "utf8");
      expect(source).not.toMatch(/color:\s*var\(--vt-color-primary-600\)/);
      expect(source).toMatch(/color:\s*var\(--vt-fg-accent\)/);
    }
    expect(contrast("#8aabff", "#1d2b4b")).toBeGreaterThanOrEqual(4.5);
  });
});

function contrast(foreground: string, background: string): number {
  const luminance = (hex: string): number => {
    const channels = hex.slice(1).match(/.{2}/g)!.map((part) => {
      const value = Number.parseInt(part, 16) / 255;
      return value <= 0.04045
        ? value / 12.92
        : ((value + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * channels[0]! + 0.7152 * channels[1]! + 0.0722 * channels[2]!;
  };
  const a = luminance(foreground);
  const b = luminance(background);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}
