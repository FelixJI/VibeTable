import type {
  ContentProfile,
  ContentProfileSnapshot,
  RecordDocumentLink,
  RecordDocumentLinkListResult,
  RecordDocumentLinkSnapshot,
} from "@/contracts/generated/workbench";
import type { MutationReceipt } from "@/contracts";
import { useHostBridge } from "@/services/bridgeContext";

export class ContentModelClientError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
    this.name = "ContentModelClientError";
  }
}

export function useContentModelService() {
  const bridge = useHostBridge();
  return {
    async loadProfile(tableId: string): Promise<ContentProfileSnapshot | null> {
      const result = await bridge.request("contentProfile.load", { tableId }) as ContentProfileSnapshot;
      if (isError(result, "content_profile.not_found")) return null;
      return unwrap(result);
    },
    commitProfile(
      profile: ContentProfile,
      expectedRevision: string | null,
    ): Promise<ContentProfileSnapshot> {
      return checked(bridge.request("contentProfile.commit", {
        profile,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      }) as Promise<ContentProfileSnapshot>);
    },
    async deleteProfile(tableId: string, expectedRevision: string): Promise<void> {
      await checked(bridge.request("contentProfile.delete", {
        tableId,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      }));
    },
    commitRecord(input: {
      readonly tableId: string;
      readonly schemaRevision: string;
      readonly recordId: string;
      readonly values: Readonly<Record<string, unknown>>;
      readonly expectedDigest: string | null;
    }): Promise<MutationReceipt> {
      return checked(bridge.request("mutation.apply", {
        contractVersion: "2.0",
        requestId: crypto.randomUUID(),
        idempotencyKey: crypto.randomUUID(),
        tableId: input.tableId,
        schemaRevision: input.schemaRevision,
        operations: [{
          kind: "update",
          recordId: input.recordId,
          values: input.values,
        }],
        actor: { type: "user", id: "local", displayName: null },
        expectedRevision: null,
        expectedDigest: input.expectedDigest,
      }) as Promise<MutationReceipt>);
    },
    listLinks(tableId: string, recordId: string): Promise<RecordDocumentLinkListResult> {
      return checked(bridge.request("recordDocumentLink.list", {
        tableId, recordId,
      }) as Promise<RecordDocumentLinkListResult>);
    },
    commitLink(
      link: RecordDocumentLink,
      expectedRevision: string | null = null,
    ): Promise<RecordDocumentLinkSnapshot> {
      return checked(bridge.request("recordDocumentLink.commit", {
        link,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      }) as Promise<RecordDocumentLinkSnapshot>);
    },
    repairLink(
      linkId: string,
      documentId: string,
      expectedRevision: string,
    ): Promise<RecordDocumentLinkSnapshot> {
      return checked(bridge.request("recordDocumentLink.repair", {
        linkId,
        documentId,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      }) as Promise<RecordDocumentLinkSnapshot>);
    },
    async deleteLink(linkId: string, expectedRevision: string): Promise<void> {
      await checked(bridge.request("recordDocumentLink.delete", {
        linkId,
        expectedRevision,
        idempotencyKey: crypto.randomUUID(),
      }));
    },
  };
}

async function checked<T>(request: Promise<T>): Promise<T> {
  return unwrap(await request);
}

function unwrap<T>(value: T): T {
  const error = productError(value);
  if (error) throw error;
  return value;
}

function isError(value: unknown, code: string): boolean {
  return productError(value)?.code === code;
}

function productError(value: unknown): ContentModelClientError | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const envelope = value as { error?: unknown };
  if (!envelope.error || typeof envelope.error !== "object" || Array.isArray(envelope.error)) {
    return null;
  }
  const error = envelope.error as { code?: unknown; message?: unknown };
  if (typeof error.code !== "string" || typeof error.message !== "string") return null;
  return new ContentModelClientError(error.code, error.message);
}
