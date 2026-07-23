import type {
  LookupCellValue,
  LookupDefinition,
  NormalizedRelationDescriptor,
  RelationTargetRef,
} from "@/contracts";

export function relationFormatter(descriptor: NormalizedRelationDescriptor) {
  return (cell: { getValue(): unknown }): HTMLElement => {
    const root = element("div", "vt-relation-value");
    root.dataset.relationKind = descriptor.kind;
    if (descriptor.state !== "valid") {
      root.append(stateBadge(descriptor.state === "invalid" ? "关系无效" : "只读关系", descriptor.state));
      root.title = descriptor.diagnostics.map((item) => item.message).join("；");
      return root;
    }
    const targets = normalizeTargets(cell.getValue());
    if (targets.length === 0) {
      root.append(element("span", "vt-cell-empty", "—"));
      return root;
    }
    for (const target of targets.slice(0, 3)) {
      const token = element("span", "vt-relation-token");
      if (descriptor.kind === "m2a") {
        token.append(element("span", "vt-relation-collection", target.collection));
      }
      token.append(document.createTextNode(target.label || target.itemId));
      token.title = `${target.collection} · ${target.itemId}`;
      root.append(token);
    }
    if (targets.length > 3) root.append(element("span", "vt-relation-more", `+${targets.length - 3}`));
    return root;
  };
}

export function lookupFormatter(
  definition: LookupDefinition | undefined,
  lookupQueryAvailable: boolean,
  unavailableReason?: string | null,
) {
  return (cell: { getValue(): unknown }): HTMLElement => {
    const root = element("div", "vt-lookup-value");
    root.append(element("span", "vt-lookup-mark", "↳"));
    if (!lookupQueryAvailable) {
      const badge = stateBadge("不可用", "invalid");
      badge.title = unavailableReason ?? "Lookup 权威查询扩展不可用";
      root.append(badge);
      return root;
    }
    if (definition && definition.state !== "valid") {
      const label = definition.state === "restricted" ? "受限" : "无效";
      const badge = stateBadge(label, definition.state);
      badge.title = definition.diagnostics.map((item) => item.message).join("；");
      root.append(badge);
      return root;
    }
    const value = normalizeLookupCell(cell.getValue());
    if (value.state !== "ok") {
      const labels: Record<Exclude<LookupCellValue["state"], "ok">, string> = {
        restricted: "受限",
        invalid: "无效",
        too_expensive: "超出预算",
      };
      const badge = stateBadge(labels[value.state], value.state);
      badge.title = value.diagnostic?.message ?? labels[value.state];
      root.append(badge);
      return root;
    }
    const display = formatLookupValue(value.value);
    root.append(element("span", display === "" ? "vt-cell-empty" : "vt-lookup-text", display || "—"));
    if (value.provenance.length > 0) root.title = `${value.provenance.length} 个来源记录`;
    return root;
  };
}

export function normalizeTargets(value: unknown): RelationTargetRef[] {
  const candidates = Array.isArray(value) ? value : value == null ? [] : [value];
  return candidates.flatMap((candidate) => {
    if (typeof candidate === "string" || typeof candidate === "number") {
      const raw = String(candidate);
      return [{ collection: "", itemId: raw, label: raw, junctionValues: {} }];
    }
    if (!candidate || typeof candidate !== "object") return [];
    const record = candidate as Record<string, unknown>;
    const itemId = record.itemId;
    if (typeof itemId !== "string" && typeof itemId !== "number") return [];
    return [{
      collection: typeof record.collection === "string" ? record.collection : "",
      itemId: String(itemId),
      label: typeof record.label === "string" ? record.label : String(itemId),
      junctionId: typeof record.junctionId === "string" ? record.junctionId : null,
      junctionRevision: typeof record.junctionRevision === "string"
        ? record.junctionRevision
        : null,
      junctionValues: isRecord(record.junctionValues) ? record.junctionValues : {},
    }];
  });
}

function normalizeLookupCell(value: unknown): LookupCellValue {
  if (isRecord(value) && ["ok", "restricted", "invalid", "too_expensive"].includes(String(value.state))) {
    return {
      state: value.state as LookupCellValue["state"],
      value: value.value,
      provenance: Array.isArray(value.provenance) ? value.provenance as LookupCellValue["provenance"] : [],
      diagnostic: isRecord(value.diagnostic) ? value.diagnostic as unknown as LookupCellValue["diagnostic"] : null,
    };
  }
  // Query v1 may project a bare scalar. It is still authoritative server data.
  return { state: "ok", value, provenance: [] };
}

function formatLookupValue(value: unknown): string {
  if (value == null) return "";
  if (Array.isArray(value)) return value.map((item) => String(item)).join(" · ");
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

function stateBadge(label: string, state: string): HTMLElement {
  return element("span", `vt-lookup-state vt-lookup-state--${state}`, label);
}

function element(tag: string, className: string, text?: string): HTMLElement {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
