"""Directus table-query contracts shared by the backend and protocol fixtures.

The web layer maps grid sorts, filters and search onto this small typed AST.
The Directus adapter validates every field against the capability manifest and
translates the AST to supported Directus query parameters. Raw filter JSON and
SQL fragments never cross the workspace boundary.

Wire conventions
----------------

* Field aliases use ``camelCase`` (the language-neutral wire contract); Python
  attribute names stay ``snake_case``. ``populate_by_name=True`` accepts both.
* ``extra="forbid"`` rejects unknown keys so a stale client cannot silently
  drop a new operator value.
* The closed ``FilterOperator`` set is a protocol contract. Adding an operator
  requires synchronized C# / TypeScript fixture updates.

Design notes
------------

* Filters carry an explicit ``logic`` connector (``AND``/``OR``).
* ``regex`` remains in the stable contract but the Directus adapter rejects it
  explicitly because Directus 11 has no equivalent portable operator.
* Stable ordering appends the collection primary key as the final sort so
  paginated reads remain deterministic.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    """Return the shared configdict mirroring backend.contracts.table.CamelModel."""
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    """Shared base for query-domain contracts.

    Mirrors the camelCase / ``populate_by_name`` config used by
    :mod:`backend.contracts.table` so all backend DTOs serialize consistently.
    """

    model_config = _camel_config()


#: The closed set of filter ``operator`` values. Adding a value here is a
#: protocol change — C# / TypeScript fixtures must reject unknown operators.
FilterOperator = Literal[
    "contains",
    "eq",
    "ne",
    "starts_with",
    "ends_with",
    "gt",
    "lt",
    "gte",
    "lte",
    "between",
    "in",
    "is_null",
    "is_not_null",
    "regex",
]

#: Sort direction values.
SortDirection = Literal["asc", "desc"]

#: Logic connector between sibling filters.
FilterLogic = Literal["AND", "OR"]


class FilterCondition(CamelModel):
    """One filter condition in the query AST.

    Wire form::

        {"field": "amount", "operator": "gt", "value": 100, "logic": "AND"}

    * ``field`` is a schema-approved column name (verified by the compiler).
    * ``operator`` is one of :data:`FilterOperator`.
    * ``value`` is the comparison value; omitted for ``is_null`` /
      ``is_not_null``; a 2-element list for ``between``; a non-empty list for
      ``in``.
    * ``logic`` joins this filter to its predecessor at the same level
      (default ``AND``).
    """

    field: str = Field(min_length=1, max_length=128)
    operator: FilterOperator
    value: Any = None
    logic: FilterLogic = "AND"


class SortCondition(CamelModel):
    """One sort condition in the query AST.

    Wire form::

        {"field": "amount", "direction": "desc", "nullsLast": true}

    * ``field`` is a schema-approved column name (verified by the compiler).
    * ``direction`` is ``asc`` or ``desc``.
    * ``nullsLast`` controls NULL placement (default ``true``).
    """

    field: str = Field(min_length=1, max_length=128)
    direction: SortDirection = "asc"
    nulls_last: bool = True


class TableQuery(CamelModel):
    """The typed query AST compiled to Directus query parameters.

    Wire form::

        {"keyword": "abc",
         "filters": [{"field": "name", "operator": "contains", "value": "abc"}],
         "sorts": [{"field": "amount", "direction": "desc"}],
         "offset": 0, "limit": 100}

    * ``keyword`` is an optional Directus full-text search term.
    * ``filters`` is an ordered list of conditions (see :class:`FilterCondition`).
    * ``sorts`` is ordered; the adapter appends the collection primary key as
      a final tie-breaker.
    * ``offset`` / ``limit`` are validated page bounds.
    """

    keyword: str | None = Field(default=None, max_length=256)
    filters: list[FilterCondition] = Field(default_factory=list, max_length=64)
    sorts: list[SortCondition] = Field(default_factory=list, max_length=16)
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=100, ge=1, le=500)
