# VibeTable Product Contracts v1

`contracts/v1` is the language-neutral source of truth for VibeTable product
messages. Runtime implementations may generate or hand-write language-specific
types, but those types must round-trip the schemas and fixtures in this
directory without adding storage-provider concepts to the wire format.

## Contents

- `contracts.schema.json` — JSON Schema 2020-12 bundle. Public definitions live
  under `$defs`.
- `fixtures/` — representative golden payloads. Each fixture is mapped to one
  public definition by `tests/contract/test_v1_contracts.py`.
- `fixtures/rpc-catalog.json` contains one request, method-specific success
  result, executable result schema, and error golden for every registered
  product RPC. `resultModel` names the actual DTO used by that method.

The v1 bundle freezes:

- normalized `TableDefinition` and `FieldDefinition`;
- every field kind and the versioned field-constraint union;
- `ProductError` and `FormulaError`;
- `MutationRequest` and `MutationReceipt`;
- `ManagedAttachmentRef`;
- `data.changed` and `task.changed` events.

## Compatibility rules

1. `contractVersion` is exactly `"1.0"` for this directory. A breaking wire
   change requires a new version directory.
2. Existing property names, meanings, enum values, required fields, and error
   codes must not be removed or reinterpreted within v1.
3. A backward-compatible v1 change may add an optional property or a new enum
   value only after all consumers tolerate unknown optional properties and
   enum values. The schema and representative fixtures must change together.
4. IDs are opaque UTF-8 strings. Consumers must not parse storage details from
   an ID or physical name.
5. Timestamps use RFC 3339 UTC text. JSON numbers must be finite; exact decimal
   behavior is described by `precisionScale` and verified by implementation
   tests rather than inferred by clients.
6. Formula previews and stored formula results use the same `cel-v1` language
   and server evaluator. Clients may display optimistic values but must replace
   them with returned values.
7. `ProductError.code` and `FormulaError.code` are stable machine identifiers.
   `message` is for people and may be localized. `path` is a product-contract
   path, not a provider path.
8. Events are at-least-once notifications. Consumers deduplicate by `eventId`
   and reconcile with `sequence` plus the supplied revision.
9. Golden fixture changes require round-trip checks in every supported language
   before merge. Python's dependency-free check is the minimum repository gate.
10. Every registered RPC method has an exhaustive response entry in
    `generate_rpc_catalog.py`. Pydantic-backed entries are generated and
    validated through their runtime DTO. Provider-neutral Go response types
    reuse the frozen v1 schema/fixture or an explicit closed method schema.
