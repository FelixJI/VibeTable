"""Version-controlled Directus schema diff/apply contracts (G0.3).

Defines the data model for reviewing and safely applying schema changes to
an existing Directus instance. The diff classifier distinguishes additive
(create) from potentially destructive (drop, type-narrow, non-null-without-
default) changes, and the apply pipeline requires a plan hash to prevent
plan drift between preview and execution.

Key invariants:

* ``plan_diff`` is a PURE function: it never touches the network or disk.
  It takes two snapshot dicts and returns a classified plan.
* Destructive actions default to ``rejected``; only an explicit
  ``allow_destructive=True`` (with a reason) lets them through.
* The apply pipeline generates a plan hash from the canonical JSON of the
  classified plan; the caller must supply the same hash at apply time.
* Schema rollback restores ONLY schema, never business data.
"""

from __future__ import annotations

from enum import StrEnum

from pydantic import BaseModel, ConfigDict


class SchemaActionKind(StrEnum):
    """What kind of schema object the action targets."""

    COLLECTION = "collection"
    FIELD = "field"
    RELATION = "relation"


class SchemaActionType(StrEnum):
    """Whether the action creates, updates or drops a schema object."""

    CREATE = "create"
    UPDATE = "update"
    DROP = "drop"


class DestructiveClassification(StrEnum):
    """Safety classification of a single schema action.

    * ``safe``: purely additive or non-breaking (new collection, new nullable
      field, widening a type).
    * ``destructive``: would lose data or break existing reads (drop
      collection, drop field, type narrowing, adding NOT NULL without
      default).
    * ``rejected``: destructive and not explicitly allowed (the default for
      destructive actions).
    """

    SAFE = "safe"
    DESTRUCTIVE = "destructive"
    REJECTED = "rejected"


class SchemaAction(BaseModel):
    """A single classified change in a schema diff plan."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    kind: SchemaActionKind
    action: SchemaActionType
    target: str
    #: ``collection`` for collection/field/relation; ``collection.field`` for fields.
    detail: str = ""
    classification: DestructiveClassification
    reason: str = ""


class SchemaDiffPlan(BaseModel):
    """The full classified diff between a current and desired snapshot."""

    model_config = ConfigDict(extra="forbid")

    actions: list[SchemaAction]
    plan_hash: str
    has_rejected: bool
    has_destructive: bool
    summary: dict[str, int]


class SchemaDeploymentError(ValueError):
    """Raised when a schema deployment plan is invalid, drifted or unsafe."""


class ApplyOptions(BaseModel):
    """Options controlling how a plan is applied.

    ``allow_destructive`` must be set to ``True`` with a ``reason`` for any
    destructive action to proceed. By default destructive actions are
    rejected and the apply is a no-op.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    plan_hash: str
    allow_destructive: bool = False
    destructive_reason: str = ""
    dry_run: bool = False


class ApplyResult(BaseModel):
    """Outcome of applying (or dry-running) a schema diff plan."""

    model_config = ConfigDict(extra="forbid")

    dry_run: bool
    plan_hash: str
    applied: list[SchemaAction]
    skipped: list[SchemaAction]
    pre_apply_snapshot_hash: str = ""
    errors: list[str] = []
