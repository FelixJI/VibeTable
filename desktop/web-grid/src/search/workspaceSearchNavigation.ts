import { nextTick, ref, watch, type Ref } from "vue";
import type { SearchHit } from "@/contracts/generated/workbench";
import type {
  DocumentEntry,
  DocumentWorkspacePhase,
} from "@/stores/documentWorkspaceStore";
import type { DocumentWorkspaceIntent } from "@/services/documentWorkspaceService";
import type { LookupValueProvenance } from "@/contracts";

export interface WorkspaceSearchLookupNavigation {
  readonly source: LookupValueProvenance;
  readonly queryRequested: false;
  readonly open: "content" | "attachment";
  readonly fieldId: string | null;
}

export interface WorkspaceSearchNavigationPorts {
  resolveHit(hit: SearchHit): Promise<SearchHit | null>;
  getDocuments(): readonly DocumentEntry[];
  getDocumentPhase(): DocumentWorkspacePhase;
  dispatchDocument(intent: DocumentWorkspaceIntent): void;
  selectDocument(index: number): void;
  showDocumentHistory(): void;
  readDocumentHistory(documentId: string): void;
  setLookupNavigation(target: WorkspaceSearchLookupNavigation): void;
  selectTable(tableId: string): void;
  navigate(view: "files" | "tables"): void;
  warnStale(): void;
  reportInvalid(): void;
}

/**
 * Resolves a derived search hit against authority, then coordinates the stable
 * product target.  The page container only wires ports; it owns no search
 * navigation state or stale-result policy.
 */
export function createWorkspaceSearchNavigation(
  ports: WorkspaceSearchNavigationPorts,
): {
  readonly requestedRevisionId: Ref<string | null>;
  open(hit: SearchHit): Promise<void>;
  openDocument(documentId: string, sourceRevision?: string | null): void;
} {
  const pendingDocument = ref<{
    readonly documentId: string;
    readonly sourceRevision: string | null;
  } | null>(null);
  const requestedRevisionId = ref<string | null>(null);
  let openEpoch = 0;

  watch(
    () => [ports.getDocuments(), ports.getDocumentPhase()] as const,
    ([entries, phase]) => {
      const pending = pendingDocument.value;
      if (!pending || phase !== "ready") return;
      const index = entries.findIndex((entry) => entry.documentId === pending.documentId);
      if (index < 0) {
        if (phase === "ready") {
          ports.warnStale();
          pendingDocument.value = null;
          requestedRevisionId.value = null;
        }
        return;
      }
      ports.selectDocument(index);
      const entry = entries[index];
      if (entry && (pending.sourceRevision === null || pending.sourceRevision !== entry.effectiveRevisionId)) {
        requestedRevisionId.value = pending.sourceRevision;
        ports.showDocumentHistory();
        ports.readDocumentHistory(pending.documentId);
      }
      pendingDocument.value = null;
    },
  );

  async function open(indexedHit: SearchHit): Promise<void> {
    const epoch = ++openEpoch;
    pendingDocument.value = null;
    requestedRevisionId.value = null;
    const hit = await ports.resolveHit(indexedHit);
    if (epoch !== openEpoch) return;
    if (!hit) {
      ports.warnStale();
      return;
    }
    const target = hit.openTarget;
    if (target.kind === "file") {
      if (!target.documentId) {
        ports.reportInvalid();
        return;
      }
      openDocument(target.documentId, hit.sourceRevision);
      return;
    }
    if (!target.tableId || !target.recordId ||
        (target.kind === "attachment" && !target.fieldId)) {
      ports.reportInvalid();
      return;
    }
    ports.setLookupNavigation({
      source: {
        collection: target.tableId,
        collectionLabel: target.tableId,
        itemId: target.recordId,
        recordLabel: target.recordId,
        fieldId: target.fieldId ?? "",
        fieldLabel: target.fieldId ?? "",
        value: null,
      },
      queryRequested: false,
      open: target.kind === "attachment" ? "attachment" : "content",
      fieldId: target.fieldId,
    });
    ports.selectTable(target.tableId);
    ports.navigate("tables");
  }

  function openDocument(documentId: string, sourceRevision: string | null = null): void {
    const epoch = ++openEpoch;
    pendingDocument.value = null;
    requestedRevisionId.value = null;
    ports.navigate("files");
    void nextTick(() => {
      if (epoch !== openEpoch) return;
      pendingDocument.value = { documentId, sourceRevision };
      ports.dispatchDocument({
        type: "document.listRequested",
        scope: { kind: "global" },
        authority: "workspace",
        query: {
          logic: "and",
          filters: [{ field: "documentId", operator: "eq", value: documentId }],
          sort: [{ field: "documentId", direction: "asc" }],
          limit: 1,
          cursor: null,
        },
      });
    });
  }

  return { requestedRevisionId, open, openDocument };
}
