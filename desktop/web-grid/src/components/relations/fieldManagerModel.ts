import type { LookupDefinition, LookupPathStep, NormalizedRelationDescriptor } from "@/contracts";

export function resolveLookupPathCollection(
  rootCollection: string,
  path: readonly LookupPathStep[],
  relations: readonly NormalizedRelationDescriptor[],
): string | null {
  let current = rootCollection;
  for (const step of path) {
    const relation = relations.find((item) => item.relationId === step.relationId && item.sourceCollection === current);
    if (!relation) return null;
    current = relation.relatedCollection ?? "";
    if (!current) return null;
  }
  return current;
}

export function lookupSourceOptions(
  definitions: readonly LookupDefinition[],
  targetCollection: string | null,
): Array<{ label: string; value: string }> {
  if (!targetCollection) return [];
  return definitions
    .filter((item) => item.collection === targetCollection)
    .map((item) => ({ label: `${item.displayName} · ${item.fieldKey}`, value: item.lookupId }));
}
