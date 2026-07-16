"""Translate the stable VibeTable query AST into Directus query parameters."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from backend.adapters.directus.errors import DirectusQueryError
from backend.contracts.query import FilterCondition, TableQuery

_OPERATOR_MAP: dict[str, str] = {
    "contains": "_contains",
    "eq": "_eq",
    "ne": "_neq",
    "starts_with": "_starts_with",
    "ends_with": "_ends_with",
    "gt": "_gt",
    "lt": "_lt",
    "gte": "_gte",
    "lte": "_lte",
    "between": "_between",
    "in": "_in",
    "is_null": "_null",
    "is_not_null": "_nnull",
}

MAX_IN_VALUES = 200


class DirectusQueryPlan(BaseModel):
    """Structured parameters ready for a future Directus REST transport.

    ``params['filter']`` intentionally stays a nested object.  The HTTP layer
    will choose JSON or bracket serialization in one place instead of letting
    application callers build arbitrary Directus filter strings.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    params: dict[str, Any]
    referenced_fields: list[str] = Field(default_factory=list)
    semantic_gaps: list[str] = Field(default_factory=list)


def compile_directus_query(
    query: TableQuery,
    *,
    approved_fields: Mapping[str, str],
    primary_key: str,
) -> DirectusQueryPlan:
    """Compile a VibeTable :class:`TableQuery` into safe Directus parameters.

    ``approved_fields`` maps the client-visible field to the Directus field.
    Every filter/sort key is resolved through this allow-list.  Mixed logic is
    folded left-to-right, matching the contract's explicit connector semantics
    and avoiding reliance on server-side operator precedence.
    """

    if primary_key not in approved_fields.values():
        raise DirectusQueryError(
            "primary key must be present in approved Directus fields",
            field=primary_key,
        )

    referenced: list[str] = []

    def resolve(field: str) -> str:
        storage = approved_fields.get(field)
        if storage is None:
            raise DirectusQueryError(
                f"unknown field {field!r}; only schema-approved fields may be queried",
                field=field,
            )
        if field not in referenced:
            referenced.append(field)
        return storage

    params: dict[str, Any] = {
        "limit": query.limit,
        "offset": query.offset,
    }
    if query.keyword and query.keyword.strip():
        params["search"] = query.keyword.strip()

    filter_tree: dict[str, Any] | None = None
    for condition in query.filters:
        predicate = _compile_filter(condition, resolve)
        if filter_tree is None:
            filter_tree = predicate
        elif condition.logic == "OR":
            filter_tree = {"_or": [filter_tree, predicate]}
        else:
            filter_tree = {"_and": [filter_tree, predicate]}
    if filter_tree is not None:
        params["filter"] = filter_tree

    sort_fields: list[str] = []
    seen: set[str] = set()
    for sort_condition in query.sorts:
        field = resolve(sort_condition.field)
        if field in seen:
            continue
        seen.add(field)
        sort_fields.append(field if sort_condition.direction == "asc" else f"-{field}")
    if primary_key not in seen:
        sort_fields.append(primary_key)
    params["sort"] = sort_fields

    # Directus' documented sort syntax has no NULLS FIRST/LAST control.  Keep
    # the gap explicit so a future caller cannot assume exact SQLite parity.
    gaps = ["explicit_null_ordering"] if query.sorts else []
    return DirectusQueryPlan(
        params=params,
        referenced_fields=referenced,
        semantic_gaps=gaps,
    )


def _compile_filter(
    condition: FilterCondition,
    resolve: Callable[[str], str],
) -> dict[str, Any]:
    field = resolve(condition.field)
    operator = condition.operator
    if operator == "regex":
        raise DirectusQueryError(
            "regex filtering is not supported by the Directus adapter",
            field=condition.field,
        )

    directus_operator = _OPERATOR_MAP[operator]
    value = condition.value
    if operator in {"is_null", "is_not_null"}:
        if value is not None:
            raise DirectusQueryError(f"operator {operator!r} takes no value", field=condition.field)
        value = True
    elif operator == "between":
        value = _require_list(value, condition.field, operator, exact=2)
    elif operator == "in":
        value = _require_list(value, condition.field, operator)
        if len(value) > MAX_IN_VALUES:
            raise DirectusQueryError(
                f"'in' list exceeds {MAX_IN_VALUES} values", field=condition.field
            )
    elif value is None:
        raise DirectusQueryError(f"operator {operator!r} requires a value", field=condition.field)
    elif operator in {"contains", "starts_with", "ends_with"} and not isinstance(value, str):
        raise DirectusQueryError(
            f"operator {operator!r} requires a string value", field=condition.field
        )

    return {field: {directus_operator: value}}


def _require_list(
    value: Any,
    field: str,
    operator: str,
    *,
    exact: int | None = None,
) -> list[Any]:
    if not isinstance(value, (list, tuple)):
        raise DirectusQueryError(f"operator {operator!r} requires a list value", field=field)
    result = list(value)
    if exact is not None and len(result) != exact:
        raise DirectusQueryError(
            f"operator {operator!r} requires exactly {exact} values", field=field
        )
    if not result:
        raise DirectusQueryError(f"operator {operator!r} requires at least one value", field=field)
    return result
