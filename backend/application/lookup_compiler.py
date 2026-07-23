"""Compile stable Lookup definitions into the private Directus execution plan."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Any

from backend.adapters.directus.relation_schema import normalize_directus_relations
from backend.contracts.lookup import (
    JunctionFieldSource,
    LookupDefinition,
    LookupQueryParams,
    LookupReferenceSource,
    TargetFieldSource,
)
from backend.contracts.relation_admin import NormalizedRelationDescriptor, SchemaSnapshot


class LookupCompileError(ValueError):
    pass


_AGGREGATE = {
    "single": "scalar",
    "values": "list",
    "distinct_values": "distinct",
    "related_count": "count",
    "non_null_count": "count_non_null",
    "sum": "sum",
    "average": "avg",
    "min": "min",
    "max": "max",
}

_OUTPUT = {
    "text": "string",
    "integer": "integer",
    "decimal": "decimal",
    "boolean": "boolean",
    "date": "date",
    "datetime": "datetime",
    "time": "time",
    "json": "json",
}

_FILTER = {
    "contains": "contains",
    "eq": "eq",
    "ne": "neq",
    "gt": "gt",
    "lt": "lt",
    "gte": "gte",
    "lte": "lte",
    "in": "in",
    "is_null": "is_null",
    "is_not_null": "not_null",
}


def compile_lookup_plan(
    *,
    params: LookupQueryParams,
    snapshot: SchemaSnapshot,
    definitions: Sequence[LookupDefinition],
    fields: Sequence[Mapping[str, Any]],
    relations: Sequence[Mapping[str, Any]],
) -> dict[str, Any]:
    if params.collection != snapshot.collection:
        raise LookupCompileError("Lookup query collection does not match the schema snapshot")
    if params.schema_revision != snapshot.schema_revision:
        raise LookupCompileError("schema changed before Lookup execution")
    if params.permission_revision != snapshot.permission_revision:
        raise LookupCompileError("permissions changed before Lookup execution")
    if params.lookup_revision != snapshot.lookup_revision:
        raise LookupCompileError("Lookup definitions changed before execution")

    field_index = {
        (str(item.get("collection")), str(item.get("field"))): item
        for item in fields
        if isinstance(item.get("collection"), str) and isinstance(item.get("field"), str)
    }
    primary_keys = _primary_keys(field_index)
    discovery = normalize_directus_relations(fields=fields, relations=relations)
    relation_index = {item.relation_id: item for item in discovery.relations}
    definitions_by_id = {item.lookup_id: item for item in definitions}
    requested_lookups: list[LookupDefinition] = []
    base_fields: list[dict[str, Any]] = []
    columns_by_ref: dict[str, Any] = {}
    for column in snapshot.columns:
        for ref in {column.name, column.field_id or ""} - {""}:
            columns_by_ref[ref] = column
    definitions_by_ref: dict[str, LookupDefinition] = {}
    for definition in definitions:
        for ref in {
            definition.lookup_id,
            definition.field_key,
            f"{definition.collection}.{definition.field_key}",
        }:
            definitions_by_ref[ref] = definition

    requested_refs: dict[str, str] = {}
    for field_ref in params.field_refs:
        requested_definition = definitions_by_ref.get(field_ref)
        if requested_definition is not None:
            if requested_definition.collection != params.collection:
                raise LookupCompileError("requested Lookup belongs to another collection")
            if requested_definition not in requested_lookups:
                requested_lookups.append(requested_definition)
            requested_refs[requested_definition.lookup_id] = field_ref
            continue
        requested_column = columns_by_ref.get(field_ref)
        if requested_column is None:
            raise LookupCompileError(f"unknown field reference {field_ref!r}")
        base_fields.append(
            {
                "ref": field_ref,
                "field": requested_column.name,
                "outputType": _column_output(requested_column.data_type, requested_column.scale),
            }
        )

    needed: dict[str, LookupDefinition] = {}

    def include(definition: LookupDefinition) -> None:
        if definition.lookup_id in needed:
            return
        needed[definition.lookup_id] = definition
        for dependency_id in definition.dependencies:
            dependency = definitions_by_id.get(dependency_id)
            if dependency is None:
                raise LookupCompileError(f"Lookup dependency {dependency_id!r} is unavailable")
            include(dependency)

    for definition in requested_lookups:
        include(definition)

    compiled = [
        _compile_definition(
            definition,
            exposed=definition in requested_lookups,
            response_ref=requested_refs.get(definition.lookup_id, definition.lookup_id),
            relation_index=relation_index,
            field_index=field_index,
            primary_keys=primary_keys,
            definitions=definitions_by_id,
        )
        for definition in needed.values()
    ]
    known_refs = {item["ref"] for item in base_fields} | {
        item["ref"] for item in compiled if item.get("expose", True)
    }
    filter_node = _compile_filters(params, known_refs)
    sort = []
    for item in params.query.sorts:
        if item.field not in known_refs:
            raise LookupCompileError(f"sort references unknown field {item.field!r}")
        sort.append({"fieldRef": item.field, "direction": item.direction})
    group_by = []
    for group in params.query.groups:
        if group.field_ref not in known_refs:
            raise LookupCompileError(f"group references unknown field {group.field_ref!r}")
        group_by.append(group.field_ref)
    return {
        "contract": "vibetable-lookup-query.v1",
        "generation": params.request_generation,
        "collection": params.collection,
        "primaryKey": snapshot.primary_key,
        "revisions": {
            "schema": params.schema_revision,
            "permission": params.permission_revision,
            "lookup": params.lookup_revision,
        },
        "baseFields": base_fields,
        "lookups": compiled,
        "filter": filter_node,
        "sort": sort,
        "groupBy": group_by,
        "groupAggregates": [],
        "page": {"offset": params.query.offset, "limit": params.query.limit},
    }


def _compile_definition(
    definition: LookupDefinition,
    *,
    exposed: bool,
    response_ref: str,
    relation_index: Mapping[str, NormalizedRelationDescriptor],
    field_index: Mapping[tuple[str, str], Mapping[str, Any]],
    primary_keys: Mapping[str, str],
    definitions: Mapping[str, LookupDefinition],
) -> dict[str, Any]:
    current = definition.collection
    path: list[dict[str, Any]] = []
    last_relation: NormalizedRelationDescriptor | None = None
    for index, path_step in enumerate(definition.path):
        relation = relation_index.get(path_step.relation_id)
        if relation is None or relation.source_collection != current:
            raise LookupCompileError(
                f"Lookup path relation {path_step.relation_id!r} is unavailable"
            )
        if relation.state != "valid":
            raise LookupCompileError(f"Lookup path relation {path_step.relation_id!r} is not valid")
        source_pk = _required_primary_key(primary_keys, current)
        step: dict[str, Any] = {
            "relationId": relation.relation_id,
            "kind": relation.kind,
            "fromCollection": current,
        }
        target: str | None
        if relation.kind == "m2o":
            target = _required_collection(relation.related_collection)
            target_primary_key = _required_primary_key(primary_keys, target)
            step.update(
                sourceField=_required_field(relation.many_field),
                targetField=target_primary_key,
                destinationPrimaryKey=target_primary_key,
                toCollection=target,
            )
        elif relation.kind == "o2m":
            target = _required_collection(relation.related_collection)
            step.update(
                sourceField=source_pk,
                targetField=_required_field(relation.many_field),
                destinationPrimaryKey=_required_primary_key(primary_keys, target),
                toCollection=target,
            )
        elif relation.kind == "m2m":
            target = _required_collection(relation.related_collection)
            target_primary_key = _required_primary_key(primary_keys, target)
            step.update(
                sourceField=source_pk,
                targetField=target_primary_key,
                destinationPrimaryKey=target_primary_key,
                toCollection=target,
                junction=_junction(relation),
            )
        else:
            selected = path_step.m2a_collection
            if selected is not None and selected not in relation.allowed_collections:
                raise LookupCompileError("M2A path selects an undeclared collection")
            if selected is None and index != len(definition.path) - 1:
                raise LookupCompileError(
                    "M2A traversal requires an explicit collection before the next hop"
                )
            target_primary_keys = {
                collection: _required_primary_key(primary_keys, collection)
                for collection in relation.allowed_collections
            }
            step.update(
                sourceField=source_pk,
                targetField=(
                    target_primary_keys[selected]
                    if selected is not None
                    else next(iter(target_primary_keys.values()))
                ),
                targetCollections=relation.allowed_collections,
                targetPrimaryKeys=target_primary_keys,
                junction=_junction(relation),
            )
            if selected is not None:
                step["toCollection"] = selected
            target = selected
        path.append(step)
        last_relation = relation
        if target is not None:
            current = target

    if isinstance(definition.source, TargetFieldSource):
        if (
            last_relation is not None
            and last_relation.kind == "m2a"
            and path[-1].get("toCollection") is None
        ):
            mapping = {item.collection: item.field_ref for item in definition.m2a_field_mapping}
            if set(mapping) != set(last_relation.allowed_collections):
                raise LookupCompileError(
                    "terminal M2A Lookup requires an explicit field for every collection"
                )
            for collection, field in mapping.items():
                _require_field(field_index, collection, field)
            source: dict[str, Any] = {"kind": "m2a", "fields": mapping}
        else:
            _require_field(field_index, current, definition.source.field_ref)
            source = {"kind": "field", "field": definition.source.field_ref}
    elif isinstance(definition.source, JunctionFieldSource):
        if last_relation is None or last_relation.junction is None:
            raise LookupCompileError("junction source requires a terminal junction relation")
        if definition.source.field_ref not in last_relation.junction.context_fields:
            raise LookupCompileError("junction source field is not declared as context")
        source = {
            "kind": "junction",
            "step": len(path) - 1,
            "field": definition.source.field_ref,
        }
    elif isinstance(definition.source, LookupReferenceSource):
        dependency = definitions.get(definition.source.lookup_id)
        if dependency is None or dependency.collection != current:
            raise LookupCompileError("Lookup dependency belongs to another path collection")
        source = {"kind": "lookup", "lookupId": definition.source.lookup_id}
    else:  # pragma: no cover - discriminated union is closed
        raise LookupCompileError("unsupported Lookup source")
    result: dict[str, Any] = {
        "lookupId": definition.lookup_id,
        "ref": response_ref,
        "path": path,
        "source": source,
        "aggregate": _AGGREGATE[definition.aggregation],
        "outputType": _lookup_output(definition),
    }
    if definition.collection != "":
        result["collection"] = definition.collection
        result["primaryKey"] = _required_primary_key(primary_keys, definition.collection)
    if not exposed:
        result["expose"] = False
    return result


def _compile_filters(params: LookupQueryParams, known_refs: set[str]) -> dict[str, Any] | None:
    nodes: list[dict[str, Any]] = []
    connectors: list[str] = []
    for condition in params.query.filters:
        if condition.field not in known_refs:
            raise LookupCompileError(f"filter references unknown field {condition.field!r}")
        operator = _FILTER.get(condition.operator)
        if operator is None:
            raise LookupCompileError(
                f"filter operator {condition.operator!r} is unsupported for Lookup queries"
            )
        node: dict[str, Any] = {
            "fieldRef": condition.field,
            "operator": operator,
        }
        if operator not in {"is_null", "not_null"}:
            node["value"] = condition.value
        nodes.append(node)
        connectors.append(condition.logic.lower())
    if not nodes:
        return None
    current = nodes[0]
    for connector, node in zip(connectors[1:], nodes[1:], strict=True):
        current = {"op": connector, "children": [current, node]}
    return current


def _primary_keys(
    field_index: Mapping[tuple[str, str], Mapping[str, Any]],
) -> dict[str, str]:
    output: dict[str, str] = {}
    duplicates: set[str] = set()
    for (collection, field), raw in field_index.items():
        schema = raw.get("schema")
        if isinstance(schema, Mapping) and schema.get("is_primary_key") is True:
            if collection in output:
                duplicates.add(collection)
            output[collection] = field
    for collection in duplicates:
        output.pop(collection, None)
    return output


def _junction(relation: NormalizedRelationDescriptor) -> dict[str, Any]:
    junction = relation.junction
    if junction is None:
        raise LookupCompileError("relation junction metadata is missing")
    value = {
        "collection": junction.collection,
        "sourceField": junction.source_field,
        "targetField": junction.target_field,
    }
    if junction.collection_field is not None:
        value["collectionField"] = junction.collection_field
    return value


def _required_collection(value: str | None) -> str:
    if not value:
        raise LookupCompileError("relation target collection is missing")
    return value


def _required_field(value: str | None) -> str:
    if not value:
        raise LookupCompileError("relation physical field is missing")
    return value


def _required_primary_key(primary_keys: Mapping[str, str], collection: str) -> str:
    value = primary_keys.get(collection)
    if value is None:
        raise LookupCompileError(f"collection {collection!r} has no visible primary key")
    return value


def _require_field(
    fields: Mapping[tuple[str, str], Mapping[str, Any]], collection: str, field: str
) -> None:
    if (collection, field) not in fields:
        raise LookupCompileError(f"field {collection}.{field} is unavailable")


def _lookup_output(definition: LookupDefinition) -> dict[str, Any]:
    output: dict[str, Any] = {"kind": _OUTPUT[definition.output_type]}
    if definition.output_type == "decimal":
        output["scale"] = definition.output_scale or 0
    return output


def _column_output(data_type: str, scale: int | None) -> dict[str, Any]:
    kind = {
        "text": "string",
        "integer": "integer",
        "decimal": "decimal",
        "boolean": "boolean",
        "date": "date",
        "datetime": "datetime",
        "time": "time",
        "json": "json",
    }.get(data_type, "string")
    output: dict[str, Any] = {"kind": kind}
    if kind == "decimal":
        output["scale"] = scale or 0
    return output


__all__ = ["LookupCompileError", "compile_lookup_plan"]
