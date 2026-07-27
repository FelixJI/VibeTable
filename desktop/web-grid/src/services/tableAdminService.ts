import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import type { CollectionSummary } from "@/stores/workspaceStore";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
  ProductErrorPayload,
  ProductTableDefinition,
  SchemaChangePayload,
} from "@/contracts";
import { buildProductFieldDefinition } from "./schemaFieldDraft";
import { createSchemaFieldDraft } from "./schemaFieldDraft";
import { buildProductIndexDefinitions } from "./schemaIndexDraft";
import { t } from "@/i18n";

/**
 * Translate the wire-level `database.opened` payload (separate `tables`/
 * `views` lists) into the store's `CollectionSummary[]` shape. Mirrors
 * `workspaceService.toCollections` — both stores maintain their own view of
 * the collection list (see the duplication note in the task-9 report).
 */
function toCollections(
  payload: DatabaseOpenedPayload,
): readonly CollectionSummary[] {
  const labels = payload.displayNames ?? {};
  const tables = payload.tables.map((t) => ({
    collection: t,
    displayName: labels[t],
    metadata: {},
  }));
  const views = payload.views.map((v) => ({
    collection: v,
    metadata: { kind: "view" } as const,
  }));
  return [...tables, ...views];
}

/**
 * Translate a `database.collectionsChanged` payload (carrying `tables` and
 * optional `capabilityHashes`) into `CollectionSummary[]`.
 */
function toCollectionsFromChanged(
  payload: CollectionsChangedPayload,
): readonly CollectionSummary[] {
  const hashes = payload.capabilityHashes ?? {};
  const labels = payload.displayNames ?? {};
  return payload.tables.map((t) => ({
    collection: t,
    displayName: labels[t],
    metadata: t in hashes ? { capabilityHash: hashes[t] } : {},
  }));
}

/**
 * tableAdminService wires `tableAdminStore` to the host bridge:
 *   - inbound `database.opened` / `database.collectionsChanged` populate the
 *     store's collection list (the sidebar reads this to list tables),
 *   - outbound `tableAdmin.createRequested` / `tableAdmin.deleteRequested` /
 *     `admin.openRequested` carry the user's create/delete/open-admin intent.
 *
 * Note on field naming: the store's `FieldRow.name` is mapped to the wire
 * `TableAdminFieldInput.key` here, keeping the host's `key`-based contract
 * boundary clean while the UI/store uses the legacy `name` label.
 */
export function useTableAdminService(): {
  init: () => void;
  createTable: () => Promise<void>;
  deleteTable: (name: string) => void;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();
  const ui = useUiStore();

  /**
   * Resolve an in-flight create/delete when the host signals the collection
   * list changed (or a fresh `database.opened` arrived). The host emits
   * `database.collectionsChanged` for ANY collection change — including the
   * initial load — so we ONLY transition + close the modal when `phase` is
   * actually `submitting`/`deleting` (meaning the user kicked off an op and is
   * waiting on its round-trip). This restores the auto-close-on-success
   * behavior that the pre-rewrite `main.ts` had (the rewrite dropped it; see
   * issue I3 in the final review).
   */
  function resolveIfPending(): void {
    if (store.phase === "submitting") {
      store.succeed();
      ui.closeCreate();
    } else if (store.phase === "deleting") {
      store.succeed();
      ui.closeDelete();
    }
  }

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setCollections(toCollections(payload));
      store.setAutoDateProducerEnabled(payload.features?.autoDateFields === true);
      // A `database.opened` after a create/delete also implies success: the
      // host re-announces the full collection list once the new schema lands.
      resolveIfPending();
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(toCollectionsFromChanged(payload));
      resolveIfPending();
    });
  }

  async function createTable(): Promise<void> {
    if (!store.canSubmit) return;
    store.beginSubmit();
    const fields = store.form.fields.map((field, index) =>
      buildProductFieldDefinition(field, index));
    if (store.autoDateProducerEnabled && store.form.includeCreatedAt) {
      const conflictIndex = fields.findIndex(({ physicalName }) =>
        physicalName === "created_at");
      if (conflictIndex >= 0) {
        store.fail(
          t("schema.autoDate.physicalNameConflict", { name: "created_at" }),
          `fields[${conflictIndex}].physicalName`,
        );
        return;
      }
      const createdAt = createSchemaFieldDraft("autoDate", "createdAt");
      createdAt.name = t("schema.autoDate.createdAt");
      fields.push(buildProductFieldDefinition(createdAt, fields.length));
    }
    if (store.autoDateProducerEnabled && store.form.includeUpdatedAt) {
      const conflictIndex = fields.findIndex(({ physicalName }) =>
        physicalName === "updated_at");
      if (conflictIndex >= 0) {
        store.fail(
          t("schema.autoDate.physicalNameConflict", { name: "updated_at" }),
          `fields[${conflictIndex}].physicalName`,
        );
        return;
      }
      const updatedAt = createSchemaFieldDraft("autoDate", "updatedAt");
      updatedAt.name = t("schema.autoDate.updatedAt");
      fields.push(buildProductFieldDefinition(updatedAt, fields.length));
    }
    const definition = buildProductTableDefinition(
      store.form.name,
      fields,
      buildProductIndexDefinitions(store.form.indexes, store.form.fields, fields),
    );
    const change: SchemaChangePayload = { definition, expectedRevision: 0 };
    try {
      const validation = await bridge.request("schema.validate", change);
      const validationError = productError(validation);
      if (validationError) {
        store.fail(localizedSchemaError(validationError), validationError.path);
        return;
      }
      const normalized = (
        validation as { readonly definition?: ProductTableDefinition }
      )?.definition ?? definition;
      const applied = await bridge.request("schema.apply", {
        definition: normalized,
        expectedRevision: 0,
      });
      const applyError = productError(applied);
      if (applyError) {
        store.fail(localizedSchemaError(applyError), applyError.path);
        return;
      }
      store.succeed();
      ui.closeCreate();
    } catch (error) {
      const mapped = error as Error & { readonly path?: string };
      store.fail(mapped.message || "创建数据表失败。", mapped.path ?? null);
    }
  }

  function deleteTable(name: string): void {
    store.requestDelete(name);
    // Wire contract is { collection }, NOT { name }.
    bridge.notify("tableAdmin.deleteRequested", { collection: name });
  }

  function openAdmin(): void {
    bridge.notify("admin.openRequested", {
      floatingButtonEnabled: ui.adminFloatingButton,
      confirmClose: ui.adminConfirmClose,
      releaseWhenIdle: ui.adminReleaseWhenIdle,
    });
  }

  return { init, createTable, deleteTable, openAdmin };
}

function localizedSchemaError(error: ProductErrorPayload): string {
  if (error.code === "schema.field.autodate_backfill_required") {
    return t("schema.autoDate.backfillRequired");
  }
  return error.message;
}

export function buildProductTableDefinition(
  displayName: string,
  fields: ProductTableDefinition["fields"],
  indexes: ProductTableDefinition["indexes"],
): ProductTableDefinition {
  const physicalName = slug(displayName) || `table_${Date.now().toString(36)}`;
  return {
    contractVersion: "1.0",
    tableId: `tbl_${physicalName}`,
    physicalName,
    displayName: displayName.trim(),
    kind: "base",
    schemaRevision: "schema_0000",
    archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
    fields,
    indexes,
  };
}

function productError(value: unknown): ProductErrorPayload | null {
  if (!value || typeof value !== "object") return null;
  const error = (value as { readonly error?: unknown }).error;
  if (!error || typeof error !== "object") return null;
  const candidate = error as Partial<ProductErrorPayload>;
  if (typeof candidate.code !== "string"
      || typeof candidate.path !== "string"
      || typeof candidate.message !== "string") return null;
  return candidate as ProductErrorPayload;
}

function slug(value: string): string {
  return value.trim().normalize("NFKC").toLocaleLowerCase()
    .replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "");
}
