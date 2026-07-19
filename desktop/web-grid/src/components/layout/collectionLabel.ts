import type { CollectionSummary } from "@/stores/workspaceStore";

/** Keep physical identifiers on events while presenting the best available label. */
export function collectionLabel(
  collection: CollectionSummary,
  displayNames?: Readonly<Record<string, string>>,
): string {
  const item = collection as CollectionSummary & {
    displayName?: string;
    title?: string;
  };
  const metadata = collection.metadata as
    | { displayName?: unknown; title?: unknown }
    | undefined;
  return (
    displayNames?.[collection.collection] ??
    item.displayName ??
    item.title ??
    (typeof metadata?.displayName === "string" ? metadata.displayName : undefined) ??
    (typeof metadata?.title === "string" ? metadata.title : undefined) ??
    collection.collection
  );
}
