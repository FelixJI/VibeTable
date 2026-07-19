import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useIdentifierMappingService } from "./identifierMappingService";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";

const result = {
  mappings: [{
    id: "m1",
    entityKind: "collection" as const,
    physicalName: "vt_t_01",
    displayName: "客户清单",
    locale: "zh-CN",
    aliases: ["客户"],
    origin: "vibetable" as const,
    status: "active" as const,
  }],
};

describe("identifierMappingService", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("loads and stores a correlated mapping result", async () => {
    const request = vi.fn().mockResolvedValue(result);
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const service = useIdentifierMappingService();
    const store = useIdentifierMappingStore();

    await service.load();

    expect(request).toHaveBeenCalledWith("identifierMappings.listRequested", { search: null });
    expect(store.mappings[0]?.displayName).toBe("客户清单");
    expect(store.phase).toBe("idle");
  });

  it("keeps an actionable error when alias update is rejected", async () => {
    setHostBridgeForTesting({
      request: vi.fn().mockRejectedValue(new Error("别名已被使用")),
    } as unknown as HostBridge);
    const service = useIdentifierMappingService();
    const store = useIdentifierMappingStore();

    await service.updateAliases("m1", ["客户"]);

    expect(store.phase).toBe("failed");
    expect(store.error).toBe("别名已被使用");
  });
});
