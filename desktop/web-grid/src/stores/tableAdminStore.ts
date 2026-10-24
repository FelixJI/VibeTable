import { computed, reactive, ref } from "vue";
import { defineStore } from "pinia";
import { validateTableName } from "@/services/tableAdminValidation";

export type TableAdminPhase =
  | "idle"
  | "creating"
  | "submitting"
  | "deleting"
  | "failed";

/**
 * Owns only the create/delete form and submission state. The collections
 * catalog itself lives in `workspaceStore` (#509): this store no longer keeps
 * a second, independently updated copy that can drift from the sidebar.
 */
export const useTableAdminStore = defineStore("tableAdmin", () => {
  const phase = ref<TableAdminPhase>("idle");
  const pendingDelete = ref<string | null>(null);
  const error = ref<string | null>(null);
  const form = reactive({ name: "" });

  const canSubmit = computed(() =>
    (phase.value === "creating" || phase.value === "failed")
    && validateTableName(form.name) === null);

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
    pendingDelete,
    error,
    form,
    canSubmit,
    openCreate,
    beginSubmit,
    requestDelete,
    succeed,
    fail,
    close,
  };
});
