# vibetable-bulk-mutation

The first approved Directus custom extension for VibeTable. Implements the
`vibetable-bulk-mutation.v1` endpoint that applies a batch of create/update
operations against a single collection in **one database transaction**
(all-or-nothing), under the requesting user's own permissions, with
idempotency-key deduplication for safe retries.

## Why this exists

The Python data plane (`DirectusClient`) only has single-item create/update.
A client-side loop cannot guarantee atomicity (a crash mid-batch leaves a
partial write) and amplifies permission/validation races. This endpoint wraps
the batch in a single transaction so any failure rolls everything back.

## Contract

### Request

```
POST /vibetable-bulk-mutation/apply
Idempotency-Key: <uuid>
Content-Type: application/json

{
  "contract": "vibetable-bulk-mutation.v1",
  "collection": "vibetable_contracts",
  "primaryKey": "id",
  "idempotencyKey": "<uuid>",
  "operations": [
    {"kind":"update","primaryKey":"1","expectedDateUpdated":"2026-07-14T...","values":{"number":"B-1"}},
    {"kind":"create","values":{"number":"C-9"}}
  ]
}
```

### Success response (200)

```json
{
  "data": {
    "createdRowKeys": ["<new-id>"],
    "updatedRowKeys": ["1"],
    "skippedRowKeys": [],
    "conflicts": []
  }
}
```

### Conflict response (200, whole batch rolled back)

```json
{
  "data": {
    "createdRowKeys": [],
    "updatedRowKeys": [],
    "skippedRowKeys": [],
    "conflicts": [
      {"primaryKey":"1","currentValue":{"id":"1","number":"A-2"},"expectedDateUpdated":"2026-07-14T..."}
    ]
  }
}
```

The client must re-preview after a conflict; it must never overwrite.

## Relation delta safety

`POST /vibetable-bulk-mutation/relation-delta` requires a canonical SHA-256
`schemaProof` for the exact physical relation plan. Before opening the write
transaction, the endpoint recomputes that proof from Directus `getSchema()` and
fails with `EDIT_CONFLICT` if a source/target PK, FK, junction field, M2A
allow-list, or relation has drifted. Source `date_updated` mismatches use the
same sanitized 409 response. Completed idempotency results are retained in a
bounded TTL/LRU cache; in-flight mutations are never evicted.

## Relation-aware import

`POST /vibetable-bulk-mutation/relation-import` accepts the private
`vibetable-relation-import.v1` execution plan compiled and validated by the
Python data plane. The request names every source/target collection and field,
provides field, unique-field, and relation allow-lists in `schemaProof`, and
contains only exact relation resolutions (`matched` or `create`). No display or
match field is inferred by the extension.

The endpoint revalidates fields and relations against the live Directus schema
and verifies every claimed unique field through the database schema inspector, resolves
each relation through a permission-aware `ItemsService` exact query, and then
creates or upserts the source rows. A `create` resolution repeats the exact
lookup inside the transaction: zero matches creates the target, one reuses the
concurrent target, and more than one fails closed. Target creation uses a
savepoint so a concurrent unique-key winner can be re-read without poisoning
the outer transaction. Target creation and all
source writes share one transaction, so any error rolls the whole import back.

The capability is advertised by the authenticated capabilities endpoint:

```json
{
  "data": {
    "relationImport": "vibetable-relation-import.v1"
  }
}
```

Idempotency entries are isolated by the current Directus user. Reusing a key
with another payload returns `IDEMPOTENCY_KEY_MISMATCH`.

## Safety properties

- **Never bypasses permissions.** Runs under `accountability` for the requesting
  user; Directus enforces field/row policy per item.
- **Atomic.** All operations commit together or roll back together.
- **Idempotent.** A repeated `Idempotency-Key` returns the original result.
- **Bounded.** Rejects more than `MAX_OPERATIONS` (10,000) operations.
- **No SQL surface.** Rejects unknown `kind`, missing primary keys, raw SQL,
  arbitrary filter JSON, and `*.*` field wildcards.

## Build & deploy

> **Environment note (2026-07-14):** this workstation has no Directus or Docker
> runtime (see `docs/handoffs/B4.json` → `validationBoundary`). The endpoint is
> delivered as source; live integration testing is deferred to a deployment with
> a running Directus 11 instance.

```bash
# From this directory:
npm ci
npm test
npm run typecheck
npm run build      # produces dist/index.js (a bundled ESM module)
```

Copy the built `dist/index.js` into the target Directus instance's
`extensions/vibetable-bulk-mutation/` directory and restart Directus. The endpoint
registers the `/apply`, `/relation-delta`, and `/relation-import` routes.

## Verification status

- Source compiles against `@directus/extensions-sdk` 18.0.1 and the dependency
  graph is locked by `package-lock.json`; test, strict typecheck and production
  build all pass locally.
- Unit tests for validation, transaction binding, exact matching, create races,
  ambiguity handling, rollback, and sanitized errors live in `src/__tests__/`.
- `npm audit --omit=dev` reports zero production vulnerabilities. Remaining
  audit notices are confined to the SDK development/build graph and are not
  copied into the bundled extension output.
- End-to-end transaction/integration testing requires a live Directus 11
  instance and is tracked as `environment-required` in the B2 handoff.
