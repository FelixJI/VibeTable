import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { InMemorySurfaceRepository } from "@/surfaces/surfaceCore";
import { useSurfaceStore } from "@/stores/surfaceStore";
import { useSurfaceService } from "./surfaceService";

describe("surfaceService", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps the global navigation guard in sync with new/save/discard", async () => {
    const service = useSurfaceService(new InMemorySurfaceRepository());
    const shell = useSurfaceStore();

    service.create("Operations");
    expect(shell.dirty).toBe(true);
    await service.save();
    expect(shell.dirty).toBe(false);
    expect(service.list.value).toHaveLength(1);

    service.dispatch({ type: "rename", name: "Changed" });
    expect(shell.dirty).toBe(true);
    await service.discard();
    expect(shell.dirty).toBe(false);
  });

  it("synchronously clears a draft and pending list state when the workspace changes", async () => {
    const service = useSurfaceService(new InMemorySurfaceRepository());
    const shell = useSurfaceStore();
    service.create("Operations");
    await service.save();
    service.dispatch({ type: "rename", name: "Unsaved" });

    service.reset();

    expect(service.state.value.phase).toBe("idle");
    expect(service.state.value.draft).toBeNull();
    expect(service.list.value).toEqual([]);
    expect(shell.dirty).toBe(false);
  });

  it("keeps controller snapshots cloneable after Vue exposes them to a component", () => {
    const service = useSurfaceService(new InMemorySurfaceRepository());
    service.create("Operations");
    const exposed = service.state.value.draft!;

    expect(() => service.replace({
      ...exposed,
      pages: [...exposed.pages, {
        pageId: "page-secondary",
        title: "Secondary",
        elements: [],
      }],
    })).not.toThrow();
    expect(service.state.value.draft?.pages).toHaveLength(2);
  });
});
