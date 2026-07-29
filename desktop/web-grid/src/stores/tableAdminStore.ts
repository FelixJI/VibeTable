import { computed, reactive, ref } from "vue";
import { defineStore } from "pinia";
import { validateTableName } from "@/services/tableAdminValidation";
import type { CollectionSummary } from "@/stores/workspaceStore";

export type TableAdminPhase =
  | "idle"
  | "creating"
  | "submitting"
  | "deleting"
  | "failed";

export const useTableAdminStore = defineStore("tableAdmin", () => {
  const phase = ref<TableAdminPhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const pendingDelete = ref<string | null>(null);
  const error = ref<string | null>(null);
  const autoDateProducerEnabled = ref(false);
  const form = reactive({ name: "" });

  const canSubmit = computed(() =>
    (phase.value === "creating" || phase.value === "failed")
    && validateTableName(form.name) === null);

  function setCollections(values: readonly CollectionSummary[]): void {
    collections.value = values;
  }

  function setAutoDateProducerEnabled(enabled: boolean): void {
    autoDateProducerEnabled.value = enabled;
  }

  function openCreate(): void {
    phase.value = "creating";
    form.name = "";
    error.value = null;
  }

  function beginSubmit(): void {
    phase.value = "submitting";
    error.value = null;
  }

  function requestDelete(name: string): void {
    pendingDelete.value = name;
    phase.value = "deleting";
    error.value = null;
  }

  function succeed(): void {
    phase.value = "idle";
    form.name = "";
    pendingDelete.value = null;
    error.value = null;
  }

  function fail(message: string): void {
    phase.value = "failed";
    error.value = message;
  }

  function close(): void {
    phase.value = "idle";
    pendingDelete.value = null;
    error.value = null;
  }

  return {
    phase,
    collections,
    pendingDelete,
    error,
    autoDateProducerEnabled,
    form,
    canSubmit,
    setCollections,
    setAutoDateProducerEnabled,
    openCreate,
    beginSubmit,
    requestDelete,
    succeed,
    fail,
    close,
  };
});
