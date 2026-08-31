import { describe, expect, it } from "vitest";

import { PRODUCT_RPC_PUBLIC_METHODS } from "./productRpcCapabilities";

describe("generated Product RPC capability adapter", () => {
  it("keeps L1 public types closed without exposing internal plugin mutations", () => {
    expect(PRODUCT_RPC_PUBLIC_METHODS).toContain("schema.getTable");
    expect(PRODUCT_RPC_PUBLIC_METHODS).not.toContain("plugin.upgrade");
    expect(PRODUCT_RPC_PUBLIC_METHODS).not.toContain("file.saveHostFile");
  });
});
