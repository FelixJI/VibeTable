import { computed, inject, provide, readonly, ref, shallowRef, type InjectionKey } from "vue";
import type { InterfaceDefinition, InterfacePage } from "@/contracts/generated/workbench";
import {
  SurfaceEditorController,
  type SurfaceEditorCommand,
  type SurfaceListEntry,
  type SurfaceRepository,
} from "@/surfaces/surfaceCore";
import { useSurfaceStore } from "@/stores/surfaceStore";

export function useSurfaceService(repository: SurfaceRepository) {
  const shell = useSurfaceStore();
  const controller = new SurfaceEditorController(repository);
  const list = ref<readonly SurfaceListEntry[]>([]);
  const loadingList = ref(false);
  const listError = ref<string | null>(null);
  // Controller snapshots are immutable plain data. A deep Vue proxy is both
  // unnecessary and invalid input to structuredClone when a draft is sent
  // back through the controller's replace seam.
  const state = shallowRef(controller.state);
  let listController: AbortController | null = null;

  function sync(): void {
    state.value = { ...controller.state };
    shell.setDraftState(state.value.draft?.interfaceId ?? null, state.value.dirty);
  }

  async function refreshList(): Promise<void> {
    listController?.abort();
    const request = new AbortController();
    listController = request;
    loadingList.value = true;
    listError.value = null;
    try {
      list.value = await repository.list(request.signal);
    } catch (error) {
      if (!request.signal.aborted) listError.value = message(error);
    } finally {
      if (listController === request) loadingList.value = false;
    }
  }

  async function open(interfaceId: string): Promise<void> {
    await controller.open(interfaceId);
    sync();
  }

  function create(name: string): void {
    const interfaceId = `interface-${crypto.randomUUID()}`;
    controller.start({
      contractVersion: "1.0",
      interfaceId,
      name: name.trim(),
      bindings: [],
      actions: [],
      pages: [blankPage()],
    });
    sync();
  }

  function dispatch(command: SurfaceEditorCommand): void {
    controller.dispatch(command);
    sync();
  }

  function replace(definition: InterfaceDefinition): void {
    dispatch({ type: "replace", definition });
  }

  async function save(): Promise<void> {
    await controller.save(crypto.randomUUID());
    sync();
    if (!state.value.dirty) await refreshList();
  }

  async function reload(): Promise<void> {
    await controller.reload();
    sync();
  }

  async function remove(): Promise<void> {
    const current = state.value;
    if (!current.draft || !current.revision) return;
    const cancellation = new AbortController();
    await repository.delete(current.draft.interfaceId, current.revision, cancellation.signal);
    controller.state = {
      phase: "idle", draft: null, revision: null, dirty: false, error: null, diagnostics: [],
    };
    sync();
    await refreshList();
  }

  async function discard(): Promise<void> {
    if (state.value.revision) await reload();
    else {
      controller.state = {
        phase: "idle", draft: null, revision: null, dirty: false, error: null, diagnostics: [],
      };
      sync();
    }
  }

  function reset(): void {
    listController?.abort();
    listController = null;
    list.value = [];
    loadingList.value = false;
    listError.value = null;
    controller.state = {
      phase: "idle", draft: null, revision: null, dirty: false, error: null, diagnostics: [],
    };
    sync();
  }

  function dispose(): void {
    reset();
    shell.clear();
  }

  return {
    list: readonly(list),
    loadingList: readonly(loadingList),
    listError: readonly(listError),
    state: computed(() => state.value),
    refreshList,
    open,
    create,
    dispatch,
    replace,
    save,
    reload,
    remove,
    discard,
    reset,
    dispose,
  };
}

function blankPage(): InterfacePage {
  return { pageId: `page-${crypto.randomUUID()}`, title: "主页", elements: [] };
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export type SurfaceService = ReturnType<typeof useSurfaceService>;
const SURFACE_SERVICE_KEY: InjectionKey<SurfaceService> = Symbol("vibetable-surface-service");

export function provideSurfaceService(service: SurfaceService): void {
  provide(SURFACE_SERVICE_KEY, service);
}

export function useProvidedSurfaceService(): SurfaceService {
  const service = inject(SURFACE_SERVICE_KEY, null);
  if (!service) throw new Error("Surface service was not provided by WorkspaceView.");
  return service;
}
