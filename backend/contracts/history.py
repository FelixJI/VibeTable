"""G1 full-field history contracts: ChangeSet aggregation and safe restore.

These contracts extend the C2 Activity/Revisions surface with a unified
``HistoryChangeSet`` that aggregates scalar + relation field changes from
the root Activity and its recursive parent-chain child revisions.

Design notes (implementation plan §6.3):
* ``HistoryChangeSet`` groups one root revision with its relation child
  revisions, exposing field-level before/after for both scalar and relation
  fields.
* ``ScalarFieldChange`` carries the field name, old value and new value.
* ``RelationFieldChange`` includes the collection, item id and a
  human-readable display value, but never leaks fields the user cannot read.
* ``RestorePreview`` is a two-phase safe-restore preview bound to
  ``(collection, item, current_hash, target_revision, schema_revision)``.
* ``RestoreDiagnostic`` classifies fields as recoverable / readonly-system /
  derived / sensitive / schema-retired.
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


class HistoryModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# Actor
# ---------------------------------------------------------------------------


class HistoryActor(HistoryModel):
    """The user who triggered a ChangeSet.

    ``display_name`` is a safe, readable name; ``user_id`` is the Directus
    user id (or None for system actions).
    """

    user_id: str | None = Field(default=None, max_length=128)
    display_name: str | None = Field(default=None, max_length=256)


# ---------------------------------------------------------------------------
# Field-level changes
# ---------------------------------------------------------------------------


class ScalarFieldChange(HistoryModel):
    """A scalar field change (text, number, date, boolean, enum, JSON).

    ``before`` / ``after`` are the values as seen in adjacent snapshots, not
    just Directus's single-sided delta.
    """

    field: str = Field(min_length=1, max_length=128)
    before: Any = None
    after: Any = None


class RelationFieldChange(HistoryModel):
    """A relation field change (M2O, O2M, M2M, M2A).

    ``related_collection`` and ``related_item_id`` identify the target.
    ``display_value`` is a human-readable rendering cropped to readable fields.
    The change never includes fields the user cannot read.
    """

    field: str = Field(min_length=1, max_length=128)
    kind: Literal["m2o", "o2m", "m2m", "m2a", "file"] = "m2o"
    related_collection: str | None = Field(default=None, max_length=128)
    related_item_id: str | None = Field(default=None, max_length=128)
    display_value: str | None = Field(default=None, max_length=512)
    before_item_id: str | None = Field(default=None, max_length=128)
    after_item_id: str | None = Field(default=None, max_length=128)


# ---------------------------------------------------------------------------
# ChangeSet
# ---------------------------------------------------------------------------


class HistoryChangeSet(HistoryModel):
    """One logical business change aggregating a root revision and its
    relation child revisions.

    ``root_revision_id`` / ``activity_id`` / ``action`` / ``timestamp`` come
    from the root Directus Activity/Revision. ``changes`` is the full set of
    scalar + relation field changes, cropped to readable fields.
    """

    root_revision_id: str = Field(min_length=1, max_length=128)
    activity_id: str | None = Field(default=None, max_length=128)
    action: str = Field(min_length=1, max_length=32)
    timestamp: str = Field(min_length=1, max_length=64)
    actor: HistoryActor = Field(default_factory=HistoryActor)
    scalar_changes: list[ScalarFieldChange] = Field(default_factory=list)
    relation_changes: list[RelationFieldChange] = Field(default_factory=list)

    @property
    def total_changes(self) -> int:
        return len(self.scalar_changes) + len(self.relation_changes)


# ---------------------------------------------------------------------------
# Paging
# ---------------------------------------------------------------------------


class ReadChangeSetsParams(HistoryModel):
    """Parameters for ``history.readChangeSets``.

    Wire form::
        {"collection": "vibetable_demo", "itemId": "uuid", "limit": 50, "offset": 0}
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    limit: int = Field(default=50, ge=1, le=100)
    offset: int = Field(default=0, ge=0)


class HistoryPage(HistoryModel):
    """Paged result of ``history.readChangeSets``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    change_sets: list[HistoryChangeSet] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)
    capability_hash: str = Field(min_length=1)
    schema_revision: str = Field(min_length=1)


# ---------------------------------------------------------------------------
# Safe restore (two-phase)
# ---------------------------------------------------------------------------

#: Restore field classifications (from capability restore_classification).
RestoreClassification = Literal[
    "recoverable",
    "readonly_system",
    "derived",
    "sensitive",
    "schema_retired",
]


class RestoreDiagnostic(HistoryModel):
    """A diagnostic for a single field in a restore preview.

    ``severity`` is ``error`` (restore blocked) or ``warning`` (user can
    proceed but should be aware). ``classification`` maps to the capability
    ``restore_classification`` vocabulary.
    """

    field: str = Field(min_length=1, max_length=128)
    classification: RestoreClassification = "recoverable"
    severity: Literal["warning", "error"] = "warning"
    code: str = Field(min_length=1, max_length=64)
    message: str = Field(min_length=1, max_length=512)


class PreviewRestoreParams(HistoryModel):
    """Parameters for ``history.previewRestore``.

    Wire form::
        {"collection": "vibetable_demo", "itemId": "uuid", "targetRevision": "rev-42"}
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    target_revision: str = Field(min_length=1, max_length=128)


class RestorePreview(HistoryModel):
    """Result of ``history.previewRestore``.

    ``current_hash`` binds the preview to the item's current state; apply
    rejects if it changed. ``schema_revision`` binds to the capability
    manifest revision. ``token`` is a single-use HMAC token.
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    target_revision: str = Field(min_length=1, max_length=128)
    current_hash: str = Field(min_length=1)
    schema_revision: str = Field(min_length=1)
    scalar_changes: list[ScalarFieldChange] = Field(default_factory=list)
    relation_changes: list[RelationFieldChange] = Field(default_factory=list)
    diagnostics: list[RestoreDiagnostic] = Field(default_factory=list)
    token: str = Field(min_length=1, max_length=2048)
    expires_at: str = Field(default="", max_length=64)


class ApplyRestoreParams(HistoryModel):
    """Parameters for ``history.applyRestore``.

    Wire form:: ``{"collection": "...", "itemId": "...", "token": "..."}`
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    token: str = Field(min_length=1, max_length=2048)


class RestoreResult(HistoryModel):
    """Result of ``history.applyRestore``.

    ``new_revision_id`` is the revision created by the restore itself
    (restoring must produce a new Revision; old history is not deleted).
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    restored_to_revision: str = Field(min_length=1, max_length=128)
    new_revision_id: str | None = Field(default=None, max_length=128)
    item: dict[str, Any] = Field(default_factory=dict)
