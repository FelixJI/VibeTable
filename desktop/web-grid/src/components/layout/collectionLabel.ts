import type { CollectionSummary } from "@/stores/workspaceStore";

/** Present the canonical display name while physical identifiers stay on events. */
export function collectionLabel(
  collection: CollectionSummary,
  displayNames: Readonly<Record<string, string>>,
): string {
  const label = displayNames[collection.collection];
  if (typeof label !== "string" || !label.trim()) {
    throw new Error(`Missing canonical display name for ${collection.collection}`);
  }
  return label;
}
