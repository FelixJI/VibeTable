import { createCapabilityClient } from "../../src/index.js";
import type { CapabilityAdapter } from "../../src/index.js";

// A host built against the original v1 adapter contract has no v2 methods.
export const adapter: CapabilityAdapter = {
  dataRead: async () => ({ items: [], nextCursor: null, totalRows: 0, rowGuards: {} }),
  dataMutate: async () => { throw new Error("Unsupported direct mutation"); },
  filePickRead: async () => null,
  filePickWrite: async () => null,
  storageGet: async () => null,
  storageSet: async () => {},
  storageDelete: async () => {},
  uiEmitResult: async () => { throw new Error("Unsupported direct result emission"); },
  uiReportProgress: async () => ({ cancelRequested: false }),
  contextRead: async () => ({
    contract: "vibetable.command-context.v1",
    projectKey: "fixture",
    collection: "articles",
    selectedKeys: [],
    querySnapshot: null,
    locale: "en-US",
    theme: "light",
    density: "compact",
    user: {},
    hostVersion: "1.0.0",
  }),
};

export const client = createCapabilityClient(adapter);