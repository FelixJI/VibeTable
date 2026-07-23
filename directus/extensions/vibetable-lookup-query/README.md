# vibetable-lookup-query

`vibetable-lookup-query.v1` is a Directus 12 endpoint for live, read-only
VibeTable Lookup columns. It registers:

- `GET /vibetable-lookup-query/capabilities`
- `POST /vibetable-lookup-query/validate`
- `POST /vibetable-lookup-query/query`

All routes require an authenticated Directus accountability. The query route
constructs every `ItemsService` with that exact accountability, schema, and
database handle. It never reads a business table with raw Knex.

## Query plan

The JSON contract is defined by `src/contracts.ts`. A plan declares stable
field references, separate schema/permission/Lookup revisions, root fields,
Lookup definitions, filters, sorts, groups, and paging. Relation steps are
explicit and identifier-only:

- `m2o`: current `sourceField` foreign key to destination `targetField`.
- `o2m`: current `sourceField` key to destination `targetField` foreign key.
- `m2m`: the junction declares its collection and both foreign-key fields.
- `m2a`: the junction additionally declares its collection discriminator and
  the plan supplies an explicit target-collection allow-list, a primary-key
  field for every target collection, plus per-target source fields.

Paths have no logical hop limit. Runtime budgets cap root records,
intermediates, service calls, execution time, and response size. A caller's
`budgetHint` can only lower deployment limits. Exceeding any budget returns
`VIBETABLE_LOOKUP_TOO_EXPENSIVE` with no rows or aggregates.

An M2A step can continue through any finite path when the compiled step selects
one `toCollection` from its declared `targetCollections`. A terminal M2A may
omit that selection and use per-collection source mappings. Ambiguous
heterogeneous continuation is rejected as an invalid plan; it is never
truncated or simplified. Lookup-to-Lookup sources may likewise occur after a
relation path. The compiler includes the referenced target-collection Lookup
as a non-exposed definition, and the extension validates its DAG, collection,
primary key, and output type before recursive execution.

## Values and aggregate rules

Supported aggregates are `scalar`, `list`, `distinct`, `count`,
`count_non_null`, `sum`, `avg`, `min`, and `max`. Decimal values are canonical
strings at the declared output scale and are calculated with `BigInt`, not
binary floating point. Date-time values require an offset and are normalized
to UTC. Lists retain nulls, distinct lists retain at most one identical null,
and numeric/min/max aggregates ignore nulls and return null when no non-null
value exists. M2A lists contain `{ collection, itemId, value }` entries.

Full-dataset filtering, stable sorting, nested group-node creation, aggregate
cells, and paging happen only after all visible root rows and Lookup values are
materialized. The primary key is always the final sort tie-breaker. Responses
return `rootTotal` before filtering and `total` after filtering; page limits up
to 10,000 remain subject to the response-size budget.

## Stable errors

- `VIBETABLE_ACCOUNTABILITY_REQUIRED`
- `VIBETABLE_LOOKUP_PLAN_INVALID`
- `VIBETABLE_LOOKUP_UNSUPPORTED`
- `VIBETABLE_LOOKUP_TOO_EXPENSIVE`
- `VIBETABLE_LOOKUP_RESTRICTED`
- `VIBETABLE_LOOKUP_SCHEMA_INVALID`
- `VIBETABLE_LOOKUP_INTERNAL`

Field denial fails the complete request as restricted. Row-hidden targets are
omitted before all values and counts, so junction rows cannot reveal them.
