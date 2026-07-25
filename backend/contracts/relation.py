"""C1 relation workspace contracts.

The relation workspace shows rows from a product table with their
declared relations expanded as display columns. Unlike a generic SQL join, it
only ever uses relations the capability manifest declares, and it cannot pick
arbitrary left/right tables, fields, or join types.

Wire conventions follow the rest of the C1 contracts (camelCase aliases,
``populate_by_name``, ``extra="forbid"``).
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel

from backend.contracts.query import TableQuery


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


class RelationProjectionParams(CamelModel):
    """Parameters for ``data.relationProjection``.

    Wire form::

        {"collection": "vibetable_demo", "query": {...},
         "relations": ["project", "owner"], "maxDepth": 1}

    ``relations`` lists the declared relation fields to expand (must be in the
    collection's :class:`RelationProfile` allow-list). ``max_depth`` bounds the
    nesting (1 = direct relations only). The result carries the base rows plus
    a ``relationColumns`` map describing the expanded display fields.
    """

    collection: str = Field(min_length=1, max_length=128)
    query: TableQuery = Field(default_factory=TableQuery)
    relations: list[str] = Field(default_factory=list, max_length=16)
    max_depth: int = Field(default=1, ge=1, le=3)


class RelationColumn(CamelModel):
    """One expanded relation display column."""

    relation: str = Field(min_length=1, max_length=128)
    field: str = Field(min_length=1, max_length=128)
    related_collection: str = Field(min_length=1, max_length=128)
    display_path: str = Field(min_length=1, max_length=256)


class RelationProjectionResult(CamelModel):
    """Result of ``data.relationProjection``.

    ``rows`` carry the base fields plus nested relation objects (e.g.
    ``row["project"]["code"]``). ``relation_columns`` lists the flattened
    display columns the UI should render. ``restricted_relations`` lists
    relations the current user cannot read (shown as a restricted state, not
    faked as empty).
    """

    collection: str = Field(min_length=1, max_length=128)
    rows: list[dict[str, Any]]
    relation_columns: list[RelationColumn] = Field(default_factory=list)
    restricted_relations: list[str] = Field(default_factory=list)
    capability_hash: str = Field(min_length=1)


__all__ = [
    "CamelModel",
    "RelationColumn",
    "RelationProjectionParams",
    "RelationProjectionResult",
]
