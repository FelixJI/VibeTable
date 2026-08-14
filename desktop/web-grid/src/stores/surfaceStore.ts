import { defineStore } from "pinia";
import { ref } from "vue";

export const useSurfaceStore = defineStore("surfaces", () => {
  const dirty = ref(false);
  const selectedInterfaceId = ref<string | null>(null);

  function setDraftState(interfaceId: string | null, isDirty: boolean): void {
    selectedInterfaceId.value = interfaceId;
    dirty.value = isDirty;
  }

  function clear(): void {
    selectedInterfaceId.value = null;
    dirty.value = false;
  }

  return { dirty, selectedInterfaceId, setDraftState, clear };
});
