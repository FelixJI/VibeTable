"""Shared column-schema contract for product table views.

Field aliases use ``camelCase`` (the language-neutral wire contract), while the
Python attribute names stay ``snake_case``. ``populate_by_name=True`` lets us
validate raw payloads that use either form — consistent with
:mod:`backend.contracts.system`.

``ColumnSchema`` is consumed by the product schema adapter and product RPC
contracts. The obsolete local-database open/list/read DTOs intentionally do
not live here: the WPF host owns its transport models and production reads go
through the table gateway.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel

FilterOperator = Literal[
    "eq",
    "ne",
    "contains",
    "starts_with",
    "ends_with",
    "gt",
    "gte",
    "lt",
    "lte",
    "between",
    "in",
    "is_null",
    "is_not_null",
]


class CamelModel(BaseModel):
    """Shared base for table-domain contracts.

    Mirrors the camelCase / ``populate_by_name`` config used by
    :mod:`backend.contracts.system` so all backend DTOs serialize consistently.
    """

    model_config = ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class ColumnSchema(CamelModel):
    """One product table field in the grid-facing schema.

    Wire form::

        {"name": "amount", "title": "Amount",
         "dataType": "decimal", "editable": false, "nullable": true,
         "scale": 2, "precision": 10}
    """

    name: str
    title: str
    field_id: str | None = None
    kind: Literal["scalar", "relation", "lookup", "formula", "attachment", "system"] = "scalar"
    relation_id: str | None = None
    lookup_id: str | None = None
    data_type: Literal[
        "text",
        "integer",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "time",
        "json",
    ]
    editable: bool = False
    nullable: bool = True
    # Numeric precision/scale from the authoritative schema. ``None`` for
    # non-numeric fields or when the source does not report them. Decimal
    # display precision and write-side scale
    # validation both key off ``scale``.
    scale: int | None = None
    precision: int | None = None
    filter_operators: list[FilterOperator] = Field(default_factory=list)
