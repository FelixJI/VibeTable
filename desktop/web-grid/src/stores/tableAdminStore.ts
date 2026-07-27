import { defineStore } from "pinia";
import { computed, reactive, ref } from "vue";
import type { TableFieldType } from "@/contracts";
import { validateFields, validateTableName } from "@/services/tableAdminValidation";
import {
  createSchemaFieldDraft,
  validateSchemaFieldDraft,
  type FieldDraftError,
  type SchemaFieldDraft,
} from "@/services/schemaFieldDraft";
import {
  createSchemaIndexDraft,
  validateSchemaIndexDrafts,
  type IndexDraftError,
  type SchemaIndexDraft,
} from "@/services/schemaIndexDraft";
import type { CollectionSummary } from "@/stores/workspaceStore";

/** Lifecycle of the create/delete admin flows. */
export type TableAdminPhase =
  | "idle"
  | "creating"
  | "submitting"
  | "deleting"
  | "failed";

/**
 * One editable field row in the create-table form. This is the STORE's own
 * shape: the field's identifier is stored as `name` (matching the UI label and
 * the legacy TableAdminWindow). On submit, `tableAdminService` maps each row to
 * the wire-level `TableAdminFieldInput { key, type }` (see `@/contracts`), so
 * the wire boundary keeps the host's `key` naming while the store keeps `name`.
 */
export interface FieldRow extends SchemaFieldDraft {}

function emptyField(type?: TableFieldType): FieldRow {
  return createSchemaFieldDraft(type);
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
  const serverFieldErrors = ref<Readonly<Record<string, string>>>({});
  const autoDateProducerEnabled = ref(false);

  // Form state lives in the store, NOT in the DOM (architecture fix #3).
  const form = reactive({
    name: "" as string,
    fields: [] as FieldRow[],
    indexes: [] as SchemaIndexDraft[],
    includeCreatedAt: true,
    includeUpdatedAt: true,
  });

  /**
   * The form is submittable when:
   *   - the form is editable (`creating`, or `failed` after a retryable error),
   *   - `form.name` passes the identifier rule, and
   *   - at least one field has a valid, non-blank name with no validation
   *     errors (blank-key rows are silently skipped by `validateFields`, so a
   *     single valid row among blank placeholders still counts).
   */
  const canSubmit = computed(() => {
    if (phase.value !== "creating" && phase.value !== "failed") return false;
    if (validateTableName(form.name) !== null) return false;
    const rows = form.fields.map((f) => ({ key: f.name, type: f.type }));
    const result = validateFields(rows);
    return result.fields.length >= 1
      && result.errors.length === 0
      && localFieldErrors.value.length === 0
      && localIndexErrors.value.length === 0;
  });
  const localFieldErrors = computed<readonly FieldDraftError[]>(() =>
    form.fields.flatMap((field, index) => validateSchemaFieldDraft(field, index)));
  const localIndexErrors = computed<readonly IndexDraftError[]>(() =>
    validateSchemaIndexDrafts(form.indexes, form.fields));

  function setCollections(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
  }

  function setAutoDateProducerEnabled(enabled: boolean): void {
    autoDateProducerEnabled.value = enabled;
  }

  function openCreate(): void {
    phase.value = "creating";
    form.name = "";
    form.fields = [emptyField()];
    form.indexes = [];
    form.includeCreatedAt = true;
    form.includeUpdatedAt = true;
    error.value = null;
    serverFieldErrors.value = {};
  }

  function addField(type?: TableFieldType): void {
    form.fields.push(emptyField(type));
  }

  function updateField(index: number, patch: Partial<FieldRow>): void {
    if (index < 0 || index >= form.fields.length) return;
    form.fields[index] = { ...form.fields[index], ...patch };
  }

  function removeField(index: number): void {
    if (index < 0 || index >= form.fields.length) return;
    const removedClientId = form.fields[index]?.clientId;
    form.fields.splice(index, 1);
    if (removedClientId) {
      for (const draft of form.indexes) {
        draft.fieldClientIds = draft.fieldClientIds
          .filter((clientId) => clientId !== removedClientId);
      }
    }
  }

  function addIndex(): void {
    form.indexes.push(createSchemaIndexDraft());
  }

  function removeIndex(index: number): void {
    if (index < 0 || index >= form.indexes.length) return;
    form.indexes.splice(index, 1);
  }

  function beginSubmit(): void {
    phase.value = "submitting";
    error.value = null;
    serverFieldErrors.value = {};
  }

  function requestDelete(name: string): void {
    pendingDelete.value = name;
    phase.value = "deleting";
    error.value = null;
    serverFieldErrors.value = {};
  }

  function succeed(): void {
    phase.value = "idle";
    form.name = "";
    form.fields = [];
    form.indexes = [];
    form.includeCreatedAt = true;
    form.includeUpdatedAt = true;
    pendingDelete.value = null;
    error.value = null;
    serverFieldErrors.value = {};
  }

  function fail(message: string, path?: string | null): void {
    phase.value = "failed";
    error.value = message;
    serverFieldErrors.value = path ? { [path]: message } : {};
  }

  function close(): void {
    phase.value = "idle";
    pendingDelete.value = null;
    error.value = null;
    serverFieldErrors.value = {};
  }

  return {
    phase,
    collections,
    pendingDelete,
    error,
    serverFieldErrors,
    autoDateProducerEnabled,
    form,
    canSubmit,
    localFieldErrors,
    localIndexErrors,
    setCollections,
    setAutoDateProducerEnabled,
    openCreate,
    addField,
    updateField,
    removeField,
    addIndex,
    removeIndex,
    beginSubmit,
    requestDelete,
    succeed,
    fail,
    close,
  };
});
