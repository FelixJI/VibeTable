"""Relation, Lookup, managed-file, and row-history product RPC module."""

from __future__ import annotations

import json
import re
import uuid

from backend.adapters.pocketbase.client import LookupViewQueryCommand
from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcHandler,
    _array,
    _integer,
    _lookup_revision,
    _object,
    _optional_text,
    _renderer_data_type,
    _result_object,
    _text,
    _text_any,
)
from backend.contracts.product_rpc import JsonObject, JsonValue, ProductParams

_ROW_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")


class ProductRelationLookupFileRpc:
    """Owns relation/Lookup semantics plus managed-file and history lifecycles."""

    def __init__(self, context: PocketBaseProductContext) -> None:
        self._context = context
        self._handlers: dict[str, ProductRpcHandler] = {
            "file.token": self._create_file_token,
            "file.applyHostChange": self._apply_host_attachment_change,
            "file.saveHostFile": self._save_attachment_to_host,
            "relation.searchTargets": self._search_relation_targets,
            "relation.createTarget": self._create_relation_target,
            "relation.updateSingle": self._update_single_relation,
            "relation.previewDelta": self._preview_relation_delta,
            "relation.applyDelta": self._apply_relation_delta,
            "lookup.list": self._list_lookups,
            "lookup.query": self._query_lookups,
            "lookup.valuePage": self._lookup_value_page,
            "history.read": self._read_history,
            "history.previewRestore": self._preview_history_restore,
            "history.applyRestore": self._apply_history_restore,
        }
        self.methods = frozenset(self._handlers)

    async def invoke(self, method: str, params: ProductParams) -> JsonObject:
        try:
            handler = self._handlers[method]
        except KeyError as exc:
            raise ValueError(f"unknown relation/Lookup/file RPC method: {method}") from exc
        return await handler(params)

    async def _create_file_token(self, params: ProductParams) -> JsonObject:
        query = {
            name: _text(params.root, name)
            for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = params.root.get("variant")
        if variant is not None:
            if not isinstance(variant, str):
                raise ValueError("variant must be a string")
            query["variant"] = variant
        return _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _apply_host_attachment_change(self, params: ProductParams) -> JsonObject:
        raw = params.root
        host_paths = _array(raw, "hostPaths")
        remove_names = _array(raw, "removeStoredNames")
        if (
            len(host_paths) > 32
            or len(remove_names) > 32
            or not all(isinstance(item, str) and item for item in host_paths)
            or not all(isinstance(item, str) and item for item in remove_names)
            or (not host_paths and not remove_names)
        ):
            raise ValueError("managed attachment change is invalid")
        expected_digest = _text(raw, "expectedDigest")
        if not _ROW_DIGEST.fullmatch(expected_digest):
            raise ValueError("expectedDigest is invalid")
        request_id = str(uuid.uuid4())
        upload_handles = [f"upload_{index}" for index in range(len(host_paths))]
        request = _result_object(
            {
                "contractVersion": "2.0",
                "requestId": request_id,
                "idempotencyKey": f"attachment:{request_id}",
                "tableId": _text(raw, "tableId"),
                "schemaRevision": _text(raw, "schemaRevision"),
                "operations": [
                    {
                        "kind": "setAttachments",
                        "recordId": _text(raw, "recordId"),
                        "fieldId": _text(raw, "fieldId"),
                        "uploadHandles": upload_handles,
                        "removeStoredNames": list(remove_names),
                    }
                ],
                "actor": {"type": "user", "id": "local-user", "displayName": None},
                "expectedRevision": None,
                "expectedDigest": expected_digest,
            }
        )
        if host_paths:
            result = await self._context.transport.request_multipart(
                "/api/vibetable/v1/mutations/apply",
                json_body=request,
                uploads=list(
                    zip(
                        upload_handles,
                        [item for item in host_paths if isinstance(item, str)],
                        strict=True,
                    )
                ),
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        else:
            result = await self._context.client.apply_mutation(request)
        return _result_object(result)

    async def _save_attachment_to_host(self, params: ProductParams) -> JsonObject:
        raw = params.root
        query = {
            name: _text(raw, name) for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = raw.get("variant")
        if variant is not None:
            if not isinstance(variant, str) or not variant:
                raise ValueError("variant must be a non-empty string")
            query["variant"] = variant
        token = _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )
        saved_bytes = await self._context.transport.download_to_file(
            "/api/vibetable/v1/attachments/download",
            query={"capability": _text(token, "downloadCapability")},
            target_path=_text(raw, "outputPath"),
            headers=dict(self._context.headers),
            expected_status=(200,),
        )
        return {"contractVersion": "2.0", "saved": True, "bytes": saved_bytes}

    async def _search_relation_targets(self, params: ProductParams) -> JsonObject:
        raw = params.root
        result = await self._context.post(
            "/api/vibetable/v1/relations/search-targets",
            {
                "relationId": _text(raw, "relationId"),
                "query": _optional_text(raw, "query"),
                "offset": _integer(raw, "offset", 0),
                "limit": _integer(raw, "limit", 50),
            },
        )
        items = result.get("items")
        if not isinstance(items, list):
            raise ValueError("PocketBase returned invalid relation targets")
        return {
            "items": [
                {
                    "collection": _text(item, "tableId"),
                    "itemId": _text(item, "recordId"),
                    "label": _text(item, "label"),
                }
                for item in items
                if isinstance(item, dict)
            ],
            "total": _integer(result, "total"),
        }

    async def _create_relation_target(self, params: ProductParams) -> JsonObject:
        raw = params.root
        request_id = _text(raw, "idempotencyKey")
        result = await self._context.post(
            "/api/vibetable/v1/relations/create-target",
            {
                "relationId": _text(raw, "relationId"),
                "label": raw.get("label") or "",
                "values": raw.get("values") or {},
                "requestId": request_id,
                "idempotencyKey": request_id,
                "actor": {"type": "user", "id": "local-user", "displayName": None},
            },
        )
        target = result.get("target")
        if not isinstance(target, dict):
            raise ValueError("PocketBase returned an invalid created relation target")
        return {
            "outcome": "committed",
            "target": _renderer_target(target),
            "requestId": request_id,
        }

    async def _preview_relation_delta(self, params: ProductParams) -> JsonObject:
        result = await self._context.post(
            "/api/vibetable/v1/relations/preview-delta",
            _translate_delta(params.root),
        )
        current = result.get("current")
        if not isinstance(current, list):
            raise ValueError("PocketBase returned invalid relation preview")
        return {
            "delta": params.root,
            "current": [_renderer_target(item) for item in current if isinstance(item, dict)],
            "diagnostics": [],
            "canApply": result.get("canApply") is True,
        }

    async def _apply_relation_delta(self, params: ProductParams) -> JsonObject:
        result = await self._context.post(
            "/api/vibetable/v1/relations/apply-delta",
            _translate_delta(params.root),
        )
        current = result.get("current")
        if not isinstance(current, list):
            raise ValueError("PocketBase returned invalid relation result")
        return {
            "outcome": "committed",
            "current": [_renderer_target(item) for item in current if isinstance(item, dict)],
            "schemaRevision": _text(params.root, "expectedSchemaRevision"),
            "requestId": _text(params.root, "idempotencyKey"),
        }

    async def _update_single_relation(self, params: ProductParams) -> JsonObject:
        raw = params.root
        relation_id = _text(raw, "relationId")
        source_record_id = _text_any(raw, "sourceRecordId", "sourceItemId")
        catalog = await self._context.client.describe_relations(relation_id.split(".", 1)[0])
        relations = catalog.get("relations")
        if not isinstance(relations, list):
            raise ValueError("PocketBase returned an invalid relation catalog")
        descriptor = next(
            (
                item
                for item in relations
                if isinstance(item, dict) and item.get("relationId") == relation_id
            ),
            None,
        )
        if not isinstance(descriptor, dict) or descriptor.get("cardinality") != "one":
            raise ValueError("single relation is unavailable")
        source_table = _text(descriptor, "sourceTableId")
        physical_name = _text(descriptor, "physicalName")
        target_table = _text(descriptor, "targetTableId")
        rows = await self._context.client.read_rows(
            table_id=source_table,
            row_ids=[source_record_id],
        )
        if len(rows) != 1:
            raise ValueError("relation source record was not found")
        current_ids = _relation_ids(rows[0].get(physical_name))
        target = raw.get("target")
        target_ref = None if target is None else _translate_target(target, "target")
        if target_ref is not None and target_ref["tableId"] != target_table:
            raise ValueError("relation target belongs to another table")
        desired_id = target_ref["recordId"] if target_ref is not None else None
        adds = [] if desired_id is None or desired_id in current_ids else [target_ref]
        removes = [
            {"tableId": target_table, "recordId": record_id, "label": record_id}
            for record_id in current_ids
            if record_id != desired_id
        ]
        request_id = _text_any(raw, "requestId", "idempotencyKey")
        result = await self._context.post(
            "/api/vibetable/v1/relations/apply-delta",
            _result_object(
                {
                    "relationId": relation_id,
                    "sourceRecordId": source_record_id,
                    "schemaRevision": _text_any(raw, "schemaRevision", "expectedSchemaRevision"),
                    "adds": adds,
                    "removes": removes,
                    "requestId": request_id,
                    "idempotencyKey": _text(raw, "idempotencyKey"),
                    "expectedDigest": raw.get("expectedDigest"),
                    "actor": raw.get(
                        "actor",
                        {"type": "user", "id": "local-user", "displayName": None},
                    ),
                }
            ),
        )
        return _result_object(
            {
                "outcome": "committed",
                "current": target,
                "schemaRevision": _text_any(raw, "schemaRevision", "expectedSchemaRevision"),
                "requestId": request_id,
                "receipt": result.get("receipt"),
            }
        )

    async def _list_lookups(self, params: ProductParams) -> JsonObject:
        table_id = _text(params.root, "collection")
        result = _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/lookups/describe",
                query={"tableId": table_id},
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )
        lookups = result.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        schema_revision = _text(result, "schemaRevision")
        return {
            "collection": table_id,
            "definitions": [_renderer_lookup(item) for item in lookups if isinstance(item, dict)],
            "lookupRevision": _lookup_revision(schema_revision, lookups),
        }

    async def _query_lookups(self, params: ProductParams) -> JsonObject:
        raw = params.root
        query = dict(_object(raw, "query"))
        groups = query.pop("groups", [])
        if not isinstance(groups, list):
            raise ValueError("query.groups must be an array")
        table_id = _text(raw, "collection")
        catalog = await self._context.client.describe_relations(table_id)
        lookups = catalog.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        current_schema_revision = _text(catalog, "schemaRevision")
        current_lookup_revision = _lookup_revision(current_schema_revision, lookups)
        if (
            _text(raw, "schemaRevision") != current_schema_revision
            or _text(raw, "permissionRevision") != current_schema_revision
            or _text(raw, "lookupRevision") != current_lookup_revision
        ):
            raise ValueError("Lookup query revisions are stale")
        group_specs: list[JsonObject] = []
        for group in groups:
            if not isinstance(group, dict):
                raise ValueError("Lookup groups must contain objects")
            direction = group.get("direction", "asc")
            if direction not in {"asc", "desc"}:
                raise ValueError("Lookup group direction is invalid")
            group_specs.append({"field": _text(group, "fieldRef"), "direction": direction})
        view = await self._context.client.query_lookup_view(
            LookupViewQueryCommand(
                table_id=table_id,
                schema_revision=current_schema_revision,
                query=query,
                groups=group_specs,
                group_limit=5000,
            )
        )
        if view.has_more_groups:
            raise ValueError("Lookup group result exceeds the bounded window")
        definitions = {
            item.get("physicalName"): _renderer_lookup(item)
            for item in lookups
            if isinstance(item, dict) and isinstance(item.get("physicalName"), str)
        }
        columns: list[JsonObject] = []
        for field_ref in _array(raw, "fieldRefs"):
            if not isinstance(field_ref, str) or field_ref not in definitions:
                raise ValueError("fieldRefs contains an unknown Lookup")
            definition = definitions[field_ref]
            columns.append(
                {
                    "fieldRef": field_ref,
                    "title": definition["displayName"],
                    "outputType": definition["outputType"],
                    "nullable": True,
                    "scale": definition["outputScale"],
                    "state": definition["state"],
                }
            )
        page = view.page
        return _result_object(
            {
                "contract": "vibetable.lookup-query.v1",
                "collection": table_id,
                "requestGeneration": _integer(raw, "requestGeneration"),
                "schemaRevision": current_schema_revision,
                "permissionRevision": current_schema_revision,
                "lookupRevision": current_lookup_revision,
                "columns": columns,
                "rows": page.rows,
                "groups": _lookup_group_nodes(view.group_rows),
                "offset": page.offset,
                "limit": page.limit,
                "filteredRows": page.filtered_rows,
                "totalRows": page.total_rows,
                "snapshot": page.snapshot,
            }
        )

    async def _lookup_value_page(self, params: ProductParams) -> JsonObject:
        raw = params.root
        table_id = _text(raw, "collection")
        catalog = await self._context.client.describe_relations(table_id)
        lookups = catalog.get("lookups")
        if not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid lookup catalog")
        schema_revision = _text(catalog, "schemaRevision")
        if (
            _text(raw, "schemaRevision") != schema_revision
            or _text(raw, "permissionRevision") != schema_revision
            or _text(raw, "lookupRevision") != _lookup_revision(schema_revision, lookups)
        ):
            raise ValueError("Lookup value page revisions are stale")
        field_ref = _text(raw, "fieldRef")
        lookup = next(
            (
                item
                for item in lookups
                if isinstance(item, dict) and item.get("physicalName") == field_ref
            ),
            None,
        )
        if not isinstance(lookup, dict):
            raise ValueError("fieldRef does not identify a Lookup")
        offset = _integer(raw, "offset")
        limit = _integer(raw, "limit")
        if offset < 0 or limit < 1 or limit > 500:
            raise ValueError("Lookup value page paging is invalid")
        return await self._context.client.lookup_value_page(
            table_id=table_id,
            schema_revision=schema_revision,
            source_record_id=_text(raw, "sourceRecordId"),
            field_id=_text(lookup, "fieldId"),
            offset=offset,
            limit=limit,
        )

    async def _read_history(self, params: ProductParams) -> JsonObject:
        raw = params.root
        query: JsonObject = {
            "collection": _text_any(raw, "collection", "tableId"),
            "limit": _integer(raw, "limit", 50),
            "offset": _integer(raw, "offset", 0),
            "scope": _optional_text(raw, "scope") or "row",
        }
        for source in ("itemId", "field", "search", "actorId", "dateFrom", "dateTo", "recordId"):
            value = raw.get(source)
            if value is not None:
                if not isinstance(value, str) or not value:
                    raise ValueError(f"{source} must be a non-empty string")
                query[source] = value
        actions = raw.get("actions", [])
        if not isinstance(actions, list) or not all(
            isinstance(item, str) and item for item in actions
        ):
            raise ValueError("actions must contain non-empty strings")
        if actions:
            query["action"] = actions
        return _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/history/change-sets",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _preview_history_restore(self, params: ProductParams) -> JsonObject:
        raw = params.root
        body: JsonObject = {
            "collection": _text_any(raw, "collection", "tableId"),
            "itemId": _text_any(raw, "itemId", "recordId"),
            "targetRevision": _text(raw, "targetRevision"),
            "scope": _optional_text(raw, "scope") or "row",
        }
        field = raw.get("field")
        if field is not None:
            if not isinstance(field, str) or not field:
                raise ValueError("field must be a non-empty string")
            body["field"] = field
        return await self._context.post("/api/vibetable/v1/history/restore-preview", body)

    async def _apply_history_restore(self, params: ProductParams) -> JsonObject:
        raw = params.root
        return await self._context.post(
            "/api/vibetable/v1/history/restore-apply",
            {
                "collection": _text_any(raw, "collection", "tableId"),
                "itemId": _text_any(raw, "itemId", "recordId"),
                "token": _text(raw, "token"),
            },
        )


def _translate_delta(raw: JsonObject) -> JsonObject:
    return _result_object(
        {
            "relationId": _text(raw, "relationId"),
            "sourceRecordId": _text_any(raw, "sourceRecordId", "sourceItemId"),
            "schemaRevision": _text_any(raw, "schemaRevision", "expectedSchemaRevision"),
            "adds": [_translate_target(item, "add") for item in _array(raw, "adds")],
            "removes": [_translate_target(item, "remove") for item in _array(raw, "removes")],
            "requestId": _text_any(raw, "requestId", "idempotencyKey"),
            "idempotencyKey": _text(raw, "idempotencyKey"),
            "expectedDigest": raw.get("expectedDigest"),
            "actor": raw.get(
                "actor",
                {"type": "user", "id": "local-user", "displayName": None},
            ),
        }
    )


def _translate_target(value: JsonValue, label: str) -> dict[str, str]:
    if not isinstance(value, dict):
        raise ValueError(f"{label} target must be an object")
    nested = value.get("target")
    target = nested if isinstance(nested, dict) else value
    return {
        "tableId": _text_any(target, "tableId", "collection"),
        "recordId": _text_any(target, "recordId", "itemId"),
        "label": _optional_text(target, "label") or _text_any(target, "recordId", "itemId"),
    }


def _renderer_target(value: JsonObject) -> JsonObject:
    return {
        "collection": _text(value, "tableId"),
        "itemId": _text(value, "recordId"),
        "label": _text(value, "label"),
        "secondaryLabel": value.get("secondaryLabel") or None,
    }


def _lookup_group_nodes(group_rows: list[JsonObject]) -> list[JsonObject]:
    result: list[JsonObject] = []
    parents: set[str] = set()
    for row in group_rows:
        key = row.get("key")
        count = row.get("count")
        if not isinstance(key, list) or len(key) not in {1, 2} or not isinstance(count, int):
            raise ValueError("Lookup group rows are invalid")
        if len(key) == 2:
            parent_count = row.get("parentCount")
            if not isinstance(parent_count, int):
                raise ValueError("Lookup parent group row is invalid")
            parent_identity = json.dumps(
                key[0],
                ensure_ascii=False,
                allow_nan=False,
                sort_keys=True,
                separators=(",", ":"),
            )
            if parent_identity not in parents:
                parents.add(parent_identity)
                result.append(
                    {
                        "path": [],
                        "key": key[0],
                        "count": parent_count,
                        "aggregates": {},
                        "childCursor": None,
                    }
                )
        result.append(
            {
                "path": key[:-1],
                "key": key[-1],
                "count": count,
                "aggregates": {},
                "childCursor": None,
            }
        )
    return result


def _renderer_lookup(value: JsonObject) -> JsonObject:
    result_cardinality = _text(value, "resultCardinality")
    if result_cardinality not in {"one", "many"}:
        raise ValueError("PocketBase returned an invalid Lookup result cardinality")
    output_type = _renderer_data_type(_text(value, "outputStorage"))
    if output_type not in {
        "text",
        "integer",
        "decimal",
        "boolean",
        "date",
        "datetime",
        "time",
        "json",
    }:
        raise ValueError("PocketBase returned an invalid Lookup output type")
    table_id = _text(value, "tableId")
    relation_field_id = _text(value, "relationFieldId")
    raw_path = value.get("path")
    if raw_path is None:
        renderer_path = [{"relationId": f"{table_id}.{relation_field_id}"}]
    elif not isinstance(raw_path, list) or not raw_path:
        raise ValueError("PocketBase returned an invalid Lookup path")
    else:
        renderer_path = []
        for step in raw_path:
            if not isinstance(step, dict):
                raise ValueError("PocketBase returned an invalid Lookup path")
            relation_id = step.get("relationId")
            if not isinstance(relation_id, str) or not relation_id:
                raise ValueError("PocketBase returned an invalid Lookup path")
            renderer_path.append({"relationId": relation_id})
    return _result_object(
        {
            "lookupId": _text(value, "lookupId"),
            "collection": table_id,
            "fieldKey": _text(value, "physicalName"),
            "displayName": _text(value, "displayName"),
            "path": renderer_path,
            "source": {"kind": "target_field", "fieldRef": _text(value, "targetFieldId")},
            "outputType": output_type,
            "outputScale": None,
            "revision": _integer(value, "revision"),
            "state": "valid",
            "diagnostics": [],
            "dependencies": [step["relationId"] for step in renderer_path],
        }
    )


def _relation_ids(value: JsonValue) -> list[str]:
    if value is None or value == "":
        return []
    if isinstance(value, str):
        return [value]
    if isinstance(value, list) and all(isinstance(item, str) and item for item in value):
        return [item for item in value if isinstance(item, str)]
    raise ValueError("PocketBase returned an invalid relation value")


__all__ = ["ProductRelationLookupFileRpc"]
