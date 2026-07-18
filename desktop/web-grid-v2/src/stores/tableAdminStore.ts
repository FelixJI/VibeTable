import { defineStore } from "pinia";
import { computed, reactive, ref } from "vue";
import { TABLE_FIELD_TYPES } from "@/contracts";
import type { TableFieldType } from "@/contracts";
import { validateFields, validateTableName } from "@/services/tableAdminValidation";
import type { CollectionSummary } from "@/stores/workspaceStore";

/** Lifecycle of the create/delete admin flows. */
export type TableAdminPhase = "idle" | "creating" | "deleting" | "failed";

/**
 * One editable field row in the create-table form. This is the STORE's own
 * shape: the field's identifier is stored as `name` (matching the UI label and
 * the legacy TableAdminWindow). On submit, `tableAdminService` maps each row to
 * the wire-level `TableAdminFieldInput { key, type }` (see `@/contracts`), so
 * the wire boundary keeps the host's `key` naming while the store keeps `name`.
 */
export interface FieldRow {
  name: string;
  type: TableFieldType;
}

function emptyField(): FieldRow {
  return { name: "", type: TABLE_FIELD_TYPES[0] };
}

/**
 * tableAdminStore holds the create-table form state (name + fields[]) IN THE
 * STORE, not in the DOM — this is architecture-debt fix #3. It also tracks the
 * delete-table pending state. `tableAdminService` wires this store to the host
 * bridge; the form/phase are consumed by the sidebar (CreateTableModal,
 * DeleteConfirmModal, AppSidebar).
 */
export const useTableAdminStore = defineStore("tableAdmin", () => {
  const phase = ref<TableAdminPhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const pendingDelete = ref<string | null>(null);
  const error = ref<string | null>(null);

  // Form state lives in the store, NOT in the DOM (architecture fix #3).
  const form = reactive({
    name: "" as string,
    fields: [] as FieldRow[],
  });

  /**
   * The form is submittable when:
   *   - we are in the `creating` phase,
   *   - `form.name` passes the identifier rule, and
   *   - at least one field has a valid, non-blank name with no validation
   *     errors (blank-key rows are silently skipped by `validateFields`, so a
   *     single valid row among blank placeholders still counts).
   */
  const canSubmit = computed(() => {
    if (phase.value !== "creating") return false;
    if (validateTableName(form.name) !== null) return false;
    const rows = form.fields.map((f) => ({ key: f.name, type: f.type }));
    const result = validateFields(rows);
    return result.fields.length >= 1 && result.errors.length === 0;
  });

  function setCollections(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
  }

  function openCreate(): void {
    phase.value = "creating";
    form.name = "";
    form.fields = [emptyField()];
    error.value = null;
  }

  function addField(): void {
    form.fields.push(emptyField());
  }

  function updateField(index: number, patch: Partial<FieldRow>): void {
    if (index < 0 || index >= form.fields.length) return;
    form.fields[index] = { ...form.fields[index], ...patch };
  }

  function removeField(index: number): void {
    if (index < 0 || index >= form.fields.length) return;
    form.fields.splice(index, 1);
  }

  function beginSubmit(): void {
    // phase stays "creating" until success/fail; the service drives the notify.
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
    form.fields = [];
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
    form,
    canSubmit,
    setCollections,
    openCreate,
    addField,
    updateField,
    removeField,
    beginSubmit,
    requestDelete,
    succeed,
    fail,
    close,
  };
});
