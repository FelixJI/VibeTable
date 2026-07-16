"""B3 selection-domain contracts: the query/selection snapshots that B3 produces
and B2 (multi-row paste) and E1 (plugin commands) consume.

A :class:`QuerySnapshot` is a stable digest of the Directus source identity,
collection, normalized query and schema/data revisions. It lets the host carry
a reference to "the view of the data I rendered" without holding the entire
collection, and lets the Directus-aware gateway reject stale paste operations.

A :class:`SelectionSnapshot` adds the ordered ``rowKeys`` the host currently has
selected, in the deterministic query order. B2 uses it to locate rows that are
not currently loaded in the web grid (remote paging) and to plan inserts past
the last row.

Wire conventions
----------------

* Field aliases use ``camelCase``; Python attributes stay ``snake_case``.
* ``populate_by_name=True`` accepts both forms.
* ``extra="forbid"`` rejects unknown keys so a stale client cannot silently
  drop a snapshot field.

Design notes
------------

* The snapshot digest NEVER hashes the entire table — only the identity +
  query + revision inputs. This keeps it cheap and stable.
* ``SnapshotValidation`` is the result the host consumes before proceeding:
  ``valid`` plus a ``reason`` tag (``query_changed``, ``schema_changed``,
  ``application_write``, ``external_write``) when invalid.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    """Shared base for selection-domain contracts."""

    model_config = _camel_config()


#: Why a snapshot is no longer valid. Consumed by B2 to decide refresh behavior.
SnapshotInvalidReason = Literal[
    "query_changed",
    "schema_changed",
    "application_write",
    "external_write",
]


class QuerySnapshot(CamelModel):
    """A stable reference to a query view of a table.

    Wire form::

        {"snapshotId": "ab12...", "digest": "cd34...",
         "databaseId": "c:/.../file.db", "table": "contracts",
         "schemaRevision": "a1b2...", "dataRevision": 42,
         "normalizedQuery": {...}}

    * ``snapshot_id`` is a short opaque handle the host carries in
      query results and passes back to preview/apply.
    * ``digest`` is the canonical SHA-256 (first 16 hex) of the snapshot inputs;
      the host MAY compare digests to detect change cheaply.
    * ``database_id`` identifies the configured logical source (``directus`` in
      the production gateway; the field name remains stable on the wire).
    * ``schema_revision`` mirrors :attr:`EditSchemaResult.schema_revision`.
    * ``data_revision`` is the gateway's source revision marker.
    * ``normalized_query`` is the canonical form of the query AST (sorted keys)
      so semantically-identical queries share a digest.
    """

    snapshot_id: str
    digest: str
    database_id: str
    table: str = Field(min_length=1, max_length=128)
    schema_revision: str = Field(min_length=1)
    data_revision: int
    normalized_query: dict[str, Any]


class SelectionSnapshot(CamelModel):
    """A selection bound to a query snapshot.

    Wire form::

        {"querySnapshot": {...}, "dataRevision": 42, "rowKeys": [1, 2, 3]}

    ``row_keys`` are sorted in the current deterministic query order so B2 can
    locate the first/last selected row even across remote pages.
    """

    query_snapshot: QuerySnapshot
    data_revision: int
    row_keys: list[int | str] = Field(default_factory=list, max_length=10000)


class SnapshotValidation(CamelModel):
    """Result of validating a snapshot against the current state.

    Wire form (valid)::

        {"valid": true, "reason": null, "currentDataRevision": 42}

    Wire form (invalid)::

        {"valid": false, "reason": "schema_changed",
         "currentDataRevision": 43, "currentSchemaRevision": "b2c3..."}

    Consumed by B2 to decide whether to proceed with paste preview/apply or
    require a re-read.
    """

    valid: bool
    reason: SnapshotInvalidReason | None = None
    current_data_revision: int | None = None
    current_schema_revision: str | None = None


class ValidateSnapshotParams(CamelModel):
    """Parameters for ``table.validateSnapshot``.

    Wire form::

        {"snapshot": {...}, "currentRevision": 42}

    ``current_revision`` is optional: when omitted, the service reads the live
    revision itself; when supplied, it is the host's best-known data revision
    (used for cheap client-side prechecks before a round trip).
    """

    snapshot: QuerySnapshot
    current_revision: int | None = None
