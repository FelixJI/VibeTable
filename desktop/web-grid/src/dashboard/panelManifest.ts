import type { DashboardManifestEntryPayload } from "@/contracts";
import type { DashboardPanel, DomainDiagnostic, PanelPosition } from "./types";

export function validatePanelManifest(
  panel: DashboardPanel,
  manifest: DashboardManifestEntryPayload | undefined,
): DomainDiagnostic[] {
  if (!manifest || manifest.rendererVersion !== "2") {
    return [error("dashboard.renderer_unavailable", "The panel renderer is unavailable.", "type")];
  }
  const diagnostics: DomainDiagnostic[] = [];
  if (panel.position.width < manifest.minSize.width || panel.position.height < manifest.minSize.height) {
    diagnostics.push(error("dashboard.panel_too_small", "The panel is smaller than its renderer minimum.", "position"));
  }
  const schema = manifest.optionsSchema;
  const properties = isRecord(schema.properties) ? schema.properties : {};
  if (schema.type !== "object") {
    diagnostics.push(error("dashboard.manifest_invalid", "The renderer options schema is invalid.", "options"));
    return diagnostics;
  }
  for (const [key, value] of Object.entries(panel.options)) {
    const rule = isRecord(properties[key]) ? properties[key] : null;
    if (!rule && schema.additionalProperties === false) {
      diagnostics.push(error("dashboard.option_unknown", `Option '${key}' is not supported by this renderer.`, `options.${key}`));
    } else if (rule && !matches(value, rule)) {
      diagnostics.push(error("dashboard.option_invalid", `Option '${key}' has an invalid value.`, `options.${key}`));
    }
  }
  return diagnostics;
}

export function enforceManifestMinimum(
  position: PanelPosition,
  manifest: DashboardManifestEntryPayload | undefined,
): PanelPosition {
  if (!manifest) return { ...position };
  return {
    ...position,
    width: Math.max(position.width, manifest.minSize.width),
    height: Math.max(position.height, manifest.minSize.height),
  };
}

function matches(value: unknown, rule: Readonly<Record<string, unknown>>): boolean {
  if (rule.type === "string") {
    return typeof value === "string" && (!Array.isArray(rule.enum) || rule.enum.includes(value));
  }
  if (rule.type === "array") {
    if (!Array.isArray(value)) return false;
    const itemRule = isRecord(rule.items) ? rule.items : null;
    return !itemRule || value.every((item) => matches(item, itemRule));
  }
  return false;
}

function error(code: string, message: string, path: string): DomainDiagnostic {
  return { code, message, path, severity: "error" };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
