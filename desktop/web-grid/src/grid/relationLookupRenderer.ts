import type {
  LookupCellValue,
  LookupDefinition,
  LookupValueProvenance,
  NormalizedRelationDescriptor,
  RelationTargetRef,
} from "@/contracts";
import { t } from "@/i18n";

export function relationFormatter(descriptor: NormalizedRelationDescriptor) {
  return (cell: { getValue(): unknown }): HTMLElement => {
    const root = element("div", "vt-relation-value");
    root.dataset.relationKind = descriptor.kind;
    if (descriptor.state !== "valid") {
      root.append(stateBadge(
        descriptor.state === "invalid"
          ? t("grid.relation.invalid")
          : t("grid.relation.readOnly"),
        descriptor.state,
      ));
      root.title = descriptor.diagnostics.map((item) => item.message).join(t("grid.diagnosticSeparator"));
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
  onSourceRequested?: (source: LookupValueProvenance) => void,
	onSourcePageRequested?: (intent: import("@/contracts").LookupSourcePageIntent) => void,
) {
	return (cell: {
		getValue(): unknown;
		getRow?(): { getData(): Record<string, unknown> };
	}): HTMLElement => {
    const root = element("div", "vt-lookup-value");
    root.append(element("span", "vt-lookup-mark", "↳"));
    if (!lookupQueryAvailable) {
      const badge = stateBadge(t("grid.lookup.unavailable"), "invalid");
      badge.title = unavailableReason ?? t("grid.lookup.unavailableHint");
      root.append(badge);
      return root;
    }
    if (definition && definition.state !== "valid") {
      const label = definition.state === "restricted"
        ? t("grid.lookup.restricted")
        : t("grid.lookup.invalid");
      const badge = stateBadge(label, definition.state);
      badge.title = definition.diagnostics.map((item) => item.message).join(t("grid.diagnosticSeparator"));
      root.append(badge);
      return root;
    }
    const value = normalizeLookupCell(cell.getValue());
    if (value.state !== "ok") {
      const labels: Record<Exclude<LookupCellValue["state"], "ok">, string> = {
        restricted: t("grid.lookup.restricted"),
        invalid: t("grid.lookup.invalid"),
        too_expensive: t("grid.lookup.tooExpensive"),
      };
      const label = value.diagnostic?.code === "lookup.value.source_missing"
        ? "#REF!"
        : labels[value.state];
      const badge = stateBadge(label, value.state);
      badge.title = value.diagnostic?.message ?? labels[value.state];
      root.append(badge);
      return root;
    }
    const display = formatLookupValue(value.value);
    root.append(element("span", display === "" ? "vt-cell-empty" : "vt-lookup-text", display || "—"));
    if (value.provenance.length > 0) {
      root.title = t("grid.lookup.sourceCount", {
        count: value.provenanceTotalKnown ? value.provenanceTotal : `${value.provenanceTotal}+`,
      });
      for (const source of value.provenance.slice(0, 3)) {
        const button = document.createElement("button");
        button.className = "vt-lookup-source";
        button.textContent = `${source.collectionLabel} · ${source.recordLabel}`;
        button.type = "button";
        button.title = t("grid.lookup.openSource", {
          collection: source.collectionLabel,
          itemId: source.recordLabel,
        });
        button.addEventListener("click", (event) => {
          event.stopPropagation();
          onSourceRequested?.(source);
        });
        root.append(button);
      }
      if (value.provenanceHasMore || value.provenanceTotal > 3) {
        const remainder = document.createElement("button");
        remainder.className = "vt-lookup-source-more";
        remainder.textContent = value.provenanceTotalKnown
          ? `+${value.provenanceTotal - 3}`
          : "…";
        remainder.type = "button";
        remainder.title = t("grid.lookup.openSources");
        remainder.addEventListener("click", (event) => {
          event.stopPropagation();
          const sourceRecordId = cell.getRow?.().getData().rowKey;
          if (
            definition
            && (typeof sourceRecordId === "string" || typeof sourceRecordId === "number")
          ) {
            onSourcePageRequested?.({
              sourceRecordId: String(sourceRecordId),
              fieldRef: definition.fieldKey,
              cell: value,
            });
          }
        });
        root.append(remainder);
      }
    }
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
      provenanceTotal: typeof value.provenanceTotal === "number"
        ? value.provenanceTotal
        : Array.isArray(value.provenance) ? value.provenance.length : 0,
      provenanceTotalKnown: value.provenanceTotalKnown !== false,
      provenanceOffset: typeof value.provenanceOffset === "number" ? value.provenanceOffset : 0,
      provenanceLimit: typeof value.provenanceLimit === "number" ? value.provenanceLimit : 100,
      provenanceHasMore: value.provenanceHasMore === true,
      diagnostic: isRecord(value.diagnostic) ? value.diagnostic as unknown as LookupCellValue["diagnostic"] : null,
    };
  }
  // Query v1 may project a bare scalar. It is still authoritative server data.
  return {
    state: "ok", value, provenance: [], provenanceTotal: 0, provenanceTotalKnown: true,
    provenanceOffset: 0, provenanceLimit: 100, provenanceHasMore: false,
  };
}

function formatLookupValue(value: unknown): string {
  if (value == null) return "";
  if (Array.isArray(value)) {
    return value.map((item) => String(item)).join(t("grid.valueSeparator"));
  }
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
